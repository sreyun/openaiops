package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// ---------------------------------------------------------------------------
// 华为 OceanStor 采集器（DeviceManager REST）
//
// **OceanStor 不支持 Redfish**——全系（V3/V5/Dorado V3/V6/Pacific）都没有
// /redfish/v1 端点。华为的 Redfish 文档只覆盖服务器侧（iBMC/iRM/MM920/Atlas），
// 存储线走的是另一套 “REST Interface Reference”。所以磁盘柜必须单独用这个采集器，
// 往 Redfish 采集器里加任何路径都救不了它。
//
// 协议要点（均取自可运行的开源实现，非猜测）：
//   - 基址   https://{ip}:8088/deviceManager/rest/
//   - 登录   POST .../rest/xxxxx/sessions  {"username","password","scope":"0"}
//            → data.deviceid + data.iBaseToken，同时**下发 cookie**
//   - 取数   GET  .../rest/{deviceid}/{resource}，需同时带 iBaseToken 头 **和** cookie
//            （只带 token 会被拒——这是最容易踩的坑）
//   - 登出   DELETE .../rest/{deviceid}/sessions
//   - 响应   {"data": ..., "error": {"code": 0, "description": "0"}}
//   - 取值   绝大多数字段是**字符串**（"HEALTHSTATUS":"1"），少数（告警 level）是数字，
//            因此统一按 any 解析再转换。
//
// 采集结果复用 shared.HardwareSnapshot：磁盘柜因此和 Redfish 服务器共用同一套
// 前端详情弹窗、告警链路与导出能力，不需要第二套 UI。
// ---------------------------------------------------------------------------

// OceanStorTarget is one OceanStor DeviceManager endpoint (from config.json).
type OceanStorTarget struct {
	Name          string `json:"name"`
	URL           string `json:"url"` // https://ip:8088 —— 端口可变，现场也见过 443
	Username      string `json:"username"`
	PasswordEnv   string `json:"password_env"`
	Password      string `json:"password,omitempty"`
	SkipTLSVerify bool   `json:"skip_tls_verify"`
	IntervalSec   int    `json:"interval_sec"`
}

func (t OceanStorTarget) resolvePassword() string {
	pw := ""
	if t.PasswordEnv != "" {
		pw = os.Getenv(t.PasswordEnv)
	}
	if pw == "" && t.Password != "" {
		pw = t.Password
	}
	if pw == "" {
		slog.Error("OceanStor 密码为空，认证将失败", "target", t.Name, "password_env", t.PasswordEnv)
	}
	return pw
}

type osSession struct {
	deviceID string
	token    string
	client   *http.Client // 持有 cookie jar —— cookie 与 token 缺一不可
}

type oceanStorCollector struct {
	targets []OceanStorTarget
	hostID  string
	fp      string

	mu        sync.Mutex
	snapshots []shared.HardwareSnapshot
	sessions  map[string]*osSession
}

func newOceanStorCollector(targets []OceanStorTarget, hostID, fp string) *oceanStorCollector {
	return &oceanStorCollector{
		targets:  targets,
		hostID:   hostID,
		fp:       fp,
		sessions: make(map[string]*osSession),
	}
}

func (oc *oceanStorCollector) run(reporter func(shared.HardwareReport)) {
	for _, t := range oc.targets {
		go oc.pollLoop(t, reporter)
	}
}

func (oc *oceanStorCollector) pollLoop(t OceanStorTarget, reporter func(shared.HardwareReport)) {
	interval := time.Duration(t.IntervalSec) * time.Second
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	slog.Info("OceanStor 采集器启动", "target", t.Name, "url", t.URL, "interval", interval)

	emit := func() {
		runSafe("oceanstor:"+t.Name, func() {
			snap := oc.collectOne(t)
			if snap.Error != "" {
				slog.Warn("OceanStor 采集失败", "target", t.Name, "err", snap.Error)
			}
			oc.storeAndReport(snap, reporter)
		})
	}
	emit()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		emit()
	}
}

func (oc *oceanStorCollector) storeAndReport(snap shared.HardwareSnapshot, reporter func(shared.HardwareReport)) {
	oc.mu.Lock()
	found := false
	// Match by TargetURL (stable across renames) first, then fall back to TargetName.
	for i, s := range oc.snapshots {
		if s.TargetURL == snap.TargetURL && snap.TargetURL != "" {
			oc.snapshots[i] = snap
			found = true
			break
		}
		if s.TargetURL == "" && s.TargetName == snap.TargetName {
			oc.snapshots[i] = snap
			found = true
			break
		}
	}
	if !found {
		oc.snapshots = append(oc.snapshots, snap)
	}
	all := make([]shared.HardwareSnapshot, len(oc.snapshots))
	copy(all, oc.snapshots)
	oc.mu.Unlock()

	reporter(shared.HardwareReport{HostID: oc.hostID, Fingerprint: oc.fp, Snapshots: all})
}

/* ---------------- 会话 ---------------- */

func (oc *oceanStorCollector) login(t OceanStorTarget) (*osSession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: redfishTransport(t.SkipTLSVerify), // 同款 TLS 兼容配置（存储控制器证书同样是自签）
		Jar:       jar,
	}
	body, _ := json.Marshal(map[string]string{
		"username": t.Username,
		"password": t.resolvePassword(),
		"scope":    "0",
	})
	// 登录时 deviceId 还不知道，占位段就是字面量 "xxxxx"（不是脱敏），服务端会忽略。
	req, err := http.NewRequest("POST", strings.TrimRight(t.URL, "/")+"/deviceManager/rest/xxxxx/sessions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("登录请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Data struct {
			DeviceID   string `json:"deviceid"`
			IBaseToken string `json:"iBaseToken"`
		} `json:"data"`
		Error osError `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("登录响应解析失败: %w", err)
	}
	if out.Error.Code != 0 {
		return nil, fmt.Errorf("登录被拒绝: code=%d %s", out.Error.Code, out.Error.Description)
	}
	if out.Data.DeviceID == "" || out.Data.IBaseToken == "" {
		return nil, fmt.Errorf("登录响应缺少 deviceid/iBaseToken")
	}
	slog.Info("OceanStor 登录成功", "target", t.Name, "device_id", out.Data.DeviceID)
	return &osSession{deviceID: out.Data.DeviceID, token: out.Data.IBaseToken, client: client}, nil
}

// osError is the OceanStor response envelope's error object.
type osError struct {
	Code        int    `json:"code"`
	Description string `json:"description"`
}

// session returns a live session, logging in on first use or after expiry.
func (oc *oceanStorCollector) session(t OceanStorTarget) (*osSession, error) {
	oc.mu.Lock()
	s := oc.sessions[t.Name]
	oc.mu.Unlock()
	if s != nil {
		return s, nil
	}
	s, err := oc.login(t)
	if err != nil {
		return nil, err
	}
	oc.mu.Lock()
	oc.sessions[t.Name] = s
	oc.mu.Unlock()
	return s, nil
}

func (oc *oceanStorCollector) dropSession(t OceanStorTarget) {
	oc.mu.Lock()
	delete(oc.sessions, t.Name)
	oc.mu.Unlock()
}

// get fetches one DeviceManager resource, re-logging in once if the token expired.
func (oc *oceanStorCollector) get(t OceanStorTarget, resource string, dst any) error {
	for attempt := 0; attempt < 2; attempt++ {
		s, err := oc.session(t)
		if err != nil {
			return err
		}
		url := fmt.Sprintf("%s/deviceManager/rest/%s/%s",
			strings.TrimRight(t.URL, "/"), s.deviceID, strings.TrimLeft(resource, "/"))
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("iBaseToken", s.token) // cookie 由 jar 自动带上，两者缺一不可

		resp, err := s.client.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			oc.dropSession(t) // 会话过期：丢掉重登一次
			continue
		}
		var env struct {
			Data  json.RawMessage `json:"data"`
			Error osError         `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("%s 响应解析失败: %w", resource, err)
		}
		// -401 / 1077949061 之类的鉴权错误同样走重登
		if env.Error.Code != 0 {
			if attempt == 0 && isOSAuthError(env.Error.Code) {
				oc.dropSession(t)
				continue
			}
			return fmt.Errorf("%s 返回错误: code=%d %s", resource, env.Error.Code, env.Error.Description)
		}
		if len(env.Data) == 0 || string(env.Data) == "null" {
			return nil // 该资源无数据（例如没有告警），不算错误
		}
		return json.Unmarshal(env.Data, dst)
	}
	return fmt.Errorf("%s: 重新登录后仍失败", resource)
}

// isOSAuthError reports whether an OceanStor error code means "session invalid".
func isOSAuthError(code int) bool {
	switch code {
	case -401, 401, 403, 1077949061, 1077987874:
		return true
	}
	return false
}

/* ---------------- 取值助手（字段几乎全是字符串） ---------------- */

func osStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			if s := strings.TrimSpace(x); s != "" && s != "--" {
				return s
			}
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(x)
		}
	}
	return ""
}

func osNum(m map[string]any, keys ...string) float64 {
	s := osStr(m, keys...)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

/* ---------------- 枚举映射（取自华为官方 valuemap，不是猜的） ---------------- */

// osHealth maps OceanStor HEALTHSTATUS to our OK/Warning/Critical vocabulary.
//
//	0 Unknown  1 Normal   2 Fault            3 Pre-fail        4 Partially broken
//	5 Degraded 6 Bad sectors found           7 Bit errors      8 Consistent
//	9 Inconsistent       10 Busy            11 No input       12 Low battery
//	13 Single link fault 14 Invalid         15 Write protect
//
// 只把 Fault / Partially broken 判为 Critical——这是商用产品，把 Degraded、
// Pre-fail 一律升级成 Critical 会让告警失去意义。判不准的（Unknown/Busy/
// Invalid）返回空串＝不产生告警，宁可漏报也不误报。
func osHealth(code string) string {
	switch code {
	case "1", "8":
		return "OK"
	case "2", "4":
		return "Critical"
	case "3", "5", "6", "7", "9", "11", "12", "13":
		return "Warning"
	}
	return ""
}

// osRunning maps RUNNINGSTATUS to a readable state (前端有对应 i18n 词条)。
func osRunning(code string) string {
	switch code {
	case "1":
		return "Normal"
	case "2":
		return "Running"
	case "5":
		return "HighTempSleep"
	case "10":
		return "LinkUp"
	case "11":
		return "LinkDown"
	case "12":
		return "PoweringOn"
	case "13":
		return "PoweredOff"
	case "14":
		return "PreCopy"
	case "16":
		return "Reconstruction"
	case "27":
		return "Online"
	case "28":
		return "Offline"
	case "32":
		return "Balancing"
	case "48":
		return "Charging"
	case "49":
		return "ChargingCompleted"
	case "50":
		return "Discharging"
	case "53":
		return "Initializing"
	case "103":
		return "PowerOnFailed"
	}
	return ""
}

// osAlarmSeverity maps DeviceManager alarm level → our severity.
// 3=warning 4=major 5=critical（华为定义）。我们只有两档，按 ITU X.733 的惯例
// 把 major 并入 Critical——存储的 major 告警该叫醒人。
func osAlarmSeverity(level float64) string {
	switch {
	case level >= 4:
		return "Critical"
	case level == 3:
		return "Warning"
	}
	return "OK"
}

/* ---------------- 采集 ---------------- */

func (oc *oceanStorCollector) collectOne(t OceanStorTarget) shared.HardwareSnapshot {
	snap := shared.HardwareSnapshot{
		TargetName: t.Name,
		TargetURL:  t.URL,
		Timestamp:  time.Now().Unix(),
	}

	// 1. 阵列本体
	var sys map[string]any
	if err := oc.get(t, "system/", &sys); err != nil {
		snap.Error = fmt.Sprintf("system: %v", err)
		return snap
	}
	ver := osStr(sys, "PRODUCTVERSION", "pointRelease")
	snap.System = shared.RedfishSystem{
		Manufacturer:    "Huawei",
		Model:           osStr(sys, "PRODUCTMODESTRING", "productModeString", "productmodestring", "MODEL", "PRODUCTMODE"),
		SerialNumber:    osStr(sys, "SN", "ID"),
		HostName:        osStr(sys, "NAME"),
		BMCModel:        "OceanStor DeviceManager",
		BMCFirmware:     ver,
		SoftwareVersion: ver, // 阵列软件版本，如 V300R003C20
		PatchVersion:    osStr(sys, "PATCHVERSION", "patchVersion", "hotPatchVersion", "SPCVersion"),
		Location:        osStr(sys, "LOCATION", "location"),
		TotalCapacityGB: osCapacityGB(osNum(sys, "TOTALCAPACITY", "totalCapacity")),
		UsedCapacityGB:  osCapacityGB(osNum(sys, "USEDCAPACITY", "usedCapacity", "USERCONSUMEDCAPACITY")),
		PowerState:      "On",
	}
	snap.Health = osHealth(osStr(sys, "HEALTHSTATUS"))
	snap.State = osRunning(osStr(sys, "RUNNINGSTATUS"))
	if snap.Health == "" {
		snap.Health = "OK"
	}

	// 2. 磁盘框（本次的主角）
	var encs []map[string]any
	if err := oc.get(t, "enclosure", &encs); err == nil {
		for _, e := range encs {
			temp := osNum(e, "TEMPERATURE")
			name := osStr(e, "NAME", "LOCATION", "ID")
			snap.Enclosures = append(snap.Enclosures, shared.StorageEnclosure{
				Name:         name,
				Model:        osStr(e, "MODEL"),
				SerialNumber: osStr(e, "SERIALNUM", "ELABEL"),
				Location:     osStr(e, "LOCATION"),
				// LOGICTYPE/TYPE 是华为的内部数字编码，没有可靠的公开枚举可查，
				// 直接显示 "0"/"1" 对运维毫无意义 —— 宁可不展示。框的性质
				// （控制框 / 硬盘框）MODEL 里本来就写着。
				Health:       osHealth(osStr(e, "HEALTHSTATUS")),
				State:        osRunning(osStr(e, "RUNNINGSTATUS")),
				TemperatureC: temp,
			})
			// 磁盘框温度没有独立传感器资源，就挂在框对象上；提到 Temps 里
			// 才能进温度曲线和"最高温度"KPI。阈值 BMC 不给，留 0（前端显示 "-"）。
			if temp > 0 {
				snap.Temps = append(snap.Temps, shared.SensorReading{
					Name:    name,
					Reading: temp,
					Unit:    "Celsius",
					Status:  osHealth(osStr(e, "HEALTHSTATUS")),
				})
			}
		}
	}

	// 3. 硬盘
	var disks []map[string]any
	if err := oc.get(t, "disk", &disks); err == nil {
		for _, d := range disks {
			snap.Storage = append(snap.Storage, shared.RedfishStorage{
				Name:         osStr(d, "MODEL", "ID"),
				Model:        osStr(d, "MODEL"),
				Manufacturer: osStr(d, "MANUFACTURER"),
				SerialNumber: osStr(d, "SERIALNUMBER", "BARCODE"),
				Revision:     osStr(d, "FIRMWAREVER"),
				Location:     osStr(d, "LOCATION"),
				CapacityGB:   osDiskCapacityGB(d),
				MediaType:    osDiskType(osStr(d, "DISKTYPE")),
				Health:       osHealth(osStr(d, "HEALTHSTATUS")),
				Status:       osHealth(osStr(d, "HEALTHSTATUS")),
				State:        osRunning(osStr(d, "RUNNINGSTATUS")),
				RotationRPM:  int(osNum(d, "SPEEDRPM")),
				// 华为给的是"已用寿命百分比"，我们的字段是"剩余寿命"，要反过来。
				LifeLeftPct: osDiskLifeLeft(d),
			})
		}
	}

	// 4. 控制器（复用 RAID/存储控制器区块）；并把控制器的 CPU / 内存 提到 CPU / 内存段——
	//    否则存储阵列在硬件页看不到 CPU / 内存（服务器 BMC 才有独立 Processor/Memory 资源，
	//    OceanStor 把这些挂在控制器对象上）。
	var ctrls []map[string]any
	if err := oc.get(t, "controller", &ctrls); err == nil {
		var totalMemMB float64
		for _, c := range ctrls {
			cname := osStr(c, "NAME", "LOCATION", "ID")
			chealth := osHealth(osStr(c, "HEALTHSTATUS"))
			snap.RAID = append(snap.RAID, shared.RedfishRAID{
				Name:            cname,
				Model:           osStr(c, "MODEL"),
				Manufacturer:    "Huawei",
				FirmwareVersion: osStr(c, "SOFTVER"),
				SerialNumber:    osStr(c, "BARCODE", "ELABEL"),
				Health:          chealth,
				State:           osRunning(osStr(c, "RUNNINGSTATUS")),
				CacheMB:         osNum(c, "MEMORYSIZE"),
			})
			// 内存：每控制器一条（MEMORYSIZE 单位 MB）
			if memMB := osNum(c, "MEMORYSIZE"); memMB > 0 {
				snap.Memory.DIMMs = append(snap.Memory.DIMMs, shared.MemoryDIMM{
					Name:       cname + " 内存",
					Slot:       cname,
					CapacityGB: memMB / 1024,
					Health:     chealth,
				})
				totalMemMB += memMB
			}
			// CPU：控制器 CPU 信息，字段名随阵列型号/版本而异，尽力抓；抓不到就不显示，绝不编造。
			if cpuModel := osStr(c, "CPUINFO", "cpuInfo", "CPUMODEL", "PROCESSOR"); cpuModel != "" {
				snap.CPUs = append(snap.CPUs, shared.RedfishCPU{
					Name:   cname + " CPU",
					Model:  cpuModel,
					Cores:  int(osNum(c, "CPUCORES", "cpuCores", "CORECOUNT")),
					Health: chealth,
				})
			}
		}
		if totalMemMB > 0 {
			snap.Memory.TotalGB = totalMemMB / 1024
		}
	}

	// 5. 电源。额定功率优先取字段；DeviceManager 常不单列，则从华为型号(PAC900S12→900W)解析兜底。
	var powers []map[string]any
	if err := oc.get(t, "power", &powers); err == nil {
		for _, p := range powers {
			model := osStr(p, "MODEL")
			rated := osNum(p, "MAXPOWER", "maxPower", "RATINGPOWER", "ratedPower")
			if rated <= 0 {
				rated = osPSUWatts(model)
			}
			snap.Power.PSUs = append(snap.Power.PSUs, shared.PSUReading{
				Name:          osStr(p, "NAME", "LOCATION", "ID"),
				Model:         model,
				Manufacturer:  osStr(p, "MANUFACTURER"),
				SerialNumber:  osStr(p, "BARCODE", "ELABEL"),
				Health:        osHealth(osStr(p, "HEALTHSTATUS")),
				State:         osRunning(osStr(p, "RUNNINGSTATUS")),
				InputWatts:    osNum(p, "INPUTPOWER", "inputPower", "POWER"),
				CapacityWatts: rated,
			})
		}
	}

	// 6. 风扇
	var fans []map[string]any
	if err := oc.get(t, "fan", &fans); err == nil {
		for _, f := range fans {
			snap.Fans = append(snap.Fans, shared.FanReading{
				Name:   osStr(f, "NAME", "LOCATION", "ID"),
				RPM:    int(osNum(f, "ROTATIONALSPEED", "SPEED")),
				Health: osHealth(osStr(f, "HEALTHSTATUS")),
				Status: osRunning(osStr(f, "RUNNINGSTATUS")),
			})
		}
	}

	// 7. 当前告警 —— 这是"哪个部件出的问题"的答案，等价于服务器侧的 SEL
	var alarms []map[string]any
	if err := oc.get(t, "alarm/currentalarm", &alarms); err == nil {
		for _, a := range alarms {
			sev := osAlarmSeverity(osNum(a, "level"))
			snap.Events = append(snap.Events, shared.HardwareEvent{
				ID:        osStr(a, "sequence", "eventID"),
				Created:   osAlarmTime(a),
				Severity:  sev,
				Message:   osStr(a, "description", "name", "detail"),
				MessageID: osStr(a, "eventID"),
				Component: osStr(a, "location", "resourceName", "name"),
			})
		}
		sort.SliceStable(snap.Events, func(i, j int) bool { return snap.Events[i].Created > snap.Events[j].Created })
		if len(snap.Events) > hwEventCap {
			snap.Events = snap.Events[:hwEventCap]
		}
	}

	return snap
}

// osCapacityGB 把 OceanStor 系统/池级容量（512B 扇区数）转为 GB。系统 TOTALCAPACITY 同样以扇区计。
func osCapacityGB(sectors float64) float64 {
	if sectors <= 0 {
		return 0
	}
	return sectors * 512 / (1024 * 1024 * 1024)
}

// osPSUWatts 从华为电源型号解析额定功率（PAC900S12-B → 900W）：取型号里第一段 3~4 位连续数字。
// DeviceManager 常不单列电源功率字段，用型号兜底总好过一片空白。取不到返回 0（前端显示 "-"）。
func osPSUWatts(model string) float64 {
	digits := ""
	for _, r := range model {
		if r >= '0' && r <= '9' {
			digits += string(r)
			if len(digits) >= 4 {
				break
			}
		} else if digits != "" {
			break // 只取第一段连续数字
		}
	}
	if len(digits) >= 3 { // 3~4 位才像瓦数，避免把 "12" 之类小编号误当功率
		if w, err := strconv.Atoi(digits); err == nil {
			return float64(w)
		}
	}
	return 0
}

// osDiskCapacityGB converts OceanStor's sector-count capacity to GB.
// CAPACITY 是**扇区数**不是字节数，直接当字节会把 3.6T 的盘算成 3.6K。
func osDiskCapacityGB(d map[string]any) float64 {
	sectors := osNum(d, "CAPACITY")
	if sectors <= 0 {
		return 0
	}
	sectorSize := osNum(d, "SECTORSIZE")
	if sectorSize <= 0 {
		sectorSize = 512
	}
	return sectors * sectorSize / (1024 * 1024 * 1024)
}

// osDiskLifeLeft turns Huawei's "used life %" into our "life left %".
// 返回 -1 表示阵列没给这个字段（多数机械盘），前端会显示 "-" 而不是 0%。
func osDiskLifeLeft(d map[string]any) float64 {
	s := osStr(d, "REMAINLIFE", "remainLife")
	if s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			return v
		}
	}
	s = osStr(d, "USEDLIFE", "usedLife", "HEALTHMARK")
	if s == "" {
		return -1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 100 {
		return -1
	}
	return 100 - v
}

// osDiskType maps DISKTYPE to a readable media type.
// 0=SAS 1=SATA 2=SSD 3=NL-SAS（华为定义）；已经是文字的就原样返回。
func osDiskType(v string) string {
	switch v {
	case "0":
		return "SAS"
	case "1":
		return "SATA"
	case "2":
		return "SSD"
	case "3":
		return "NL-SAS"
	}
	return v
}

// osAlarmTime normalises the alarm timestamp to RFC3339 for the UI.
// startTime 是 Unix 秒。
func osAlarmTime(a map[string]any) string {
	ts := osNum(a, "startTime", "starttime")
	if ts <= 0 {
		return ""
	}
	return time.Unix(int64(ts), 0).Format(time.RFC3339)
}
