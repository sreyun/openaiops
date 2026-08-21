package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"aiops-monitor/shared"
)

// ---------------------------------------------------------------------------
// Sreyun 硬件 / 流量工具
//
// 此前 AI 的 10 个工具里没有一个能看到硬件或流量：机器硬件报错时，AI 只能看到
// CPU/内存这类 OS 侧指标，看不到 BMC 说的"2 号电源掉了"。这几个工具把带外硬件
// 数据和流量数据接进会话，让诊断能直接落到"换哪个件"。
//
// 输出面向 LLM：纯文本、紧凑、把结论摆在最前面，不返回原始 JSON——
// 让模型省下 token 去推理，而不是去解析。
// ---------------------------------------------------------------------------

// hwResolve maps a user-supplied host reference to (id, name, snapshots).
func (h *SreyunCore) hwResolve(args map[string]any) (string, string, []shared.HardwareSnapshot, string) {
	ref, _ := args["host_id"].(string)
	if ref == "" {
		return "", "", nil, "请指定 host_id"
	}
	hostID, name := ref, ref
	if hst := h.resolveHostRef(ref); hst != nil {
		hostID, name = hst.ID, hst.Hostname
	}
	snaps := h.s.hw.snapsOf(hostID)
	if len(snaps) == 0 && h.s.pg != nil {
		// 内存里没有（服务端刚重启）就回落到 PG 的最新快照
		if rows, err := h.s.pg.getHardwareSnapshots(hostID); err == nil && len(rows) > 0 {
			for _, r := range rows {
				if snap, ok := hwSnapFromRow(r); ok {
					snaps = append(snaps, snap)
				}
			}
		}
	}
	if len(snaps) == 0 {
		return hostID, name, nil, fmt.Sprintf(
			"主机 %s 没有硬件数据。可能原因：未配置 Redfish/OceanStor 采集目标，或 Agent 未上报。", name)
	}
	return hostID, name, snaps, ""
}

func hwSnapFromRow(r map[string]any) (shared.HardwareSnapshot, bool) {
	raw, ok := r["snapshot"]
	if !ok {
		return shared.HardwareSnapshot{}, false
	}
	// getHardwareSnapshots 把 JSONB 解成了 any，这里再走一趟 JSON 还原成结构体
	b, err := json.Marshal(raw)
	if err != nil {
		return shared.HardwareSnapshot{}, false
	}
	var snap shared.HardwareSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return shared.HardwareSnapshot{}, false
	}
	if tn, ok := r["target_name"].(string); ok && snap.TargetName == "" {
		snap.TargetName = tn
	}
	return snap, true
}

// hwBadParts lists every unhealthy component with enough detail to act on.
// 与前端"需要关注"用同一套判定口径，避免 AI 和界面说法不一致。
func hwBadParts(snap shared.HardwareSnapshot) []string {
	var out []string
	bad := func(hs string) bool { return hs == "Warning" || hs == "Critical" }
	add := func(kind, name, detail, health string) {
		out = append(out, fmt.Sprintf("  [%s] %s — %s（%s）", kind, name, detail, health))
	}
	for _, t := range snap.Temps {
		over := ""
		switch {
		case t.UpperCritical > 0 && t.Reading >= t.UpperCritical:
			over = "Critical"
		case t.UpperCaution > 0 && t.Reading >= t.UpperCaution:
			over = "Warning"
		case bad(t.Status):
			over = t.Status
		}
		if over != "" {
			add("温度", t.Name, fmt.Sprintf("%.0f°C（告警阈值 %.0f / 严重阈值 %.0f）",
				t.Reading, t.UpperCaution, t.UpperCritical), over)
		}
	}
	for _, f := range snap.Fans {
		if bad(f.Health) || bad(f.Status) {
			add("风扇", f.Name, fmt.Sprintf("%d RPM", f.RPM), firstNonEmptyOrDash(f.Health, f.Status))
		}
	}
	for _, p := range snap.Power.PSUs {
		if bad(p.Health) {
			add("电源", p.Name, joinNonEmpty(p.Model, fmt.Sprintf("%.0fW", p.InputWatts)), p.Health)
		}
	}
	for _, d := range snap.Storage {
		if bad(d.Health) || d.SMARTWarn {
			detail := joinNonEmpty(d.Model, d.SerialNumber, fmt.Sprintf("%.0fGB", d.CapacityGB))
			if d.SMARTWarn {
				detail += " · SMART 预测故障"
			}
			if d.Location != "" {
				detail = "槽位 " + d.Location + " · " + detail
			}
			health := d.Health
			if d.SMARTWarn {
				health = "Critical"
			}
			add("硬盘", d.Name, detail, health)
		}
	}
	for _, d := range snap.Memory.DIMMs {
		if bad(d.Health) {
			add("内存", firstNonEmptyOrDash(d.Slot, d.Name),
				joinNonEmpty(d.PartNumber, d.SerialNumber, fmt.Sprintf("%.0fGB", d.CapacityGB)), d.Health)
		}
	}
	for _, c := range snap.CPUs {
		if bad(c.Health) {
			add("CPU", c.Name, c.Model, c.Health)
		}
	}
	for _, g := range snap.GPUs {
		if bad(g.Health) {
			add("GPU", g.Name, g.Model, g.Health)
		}
	}
	for _, r := range snap.RAID {
		if bad(r.Health) {
			add("RAID卡", r.Name, joinNonEmpty(r.Model, r.FirmwareVersion), r.Health)
		}
		for _, v := range r.Volumes {
			if bad(v.Health) {
				add("逻辑卷", r.Name+"/"+v.Name, v.RAIDType, v.Health)
			}
		}
	}
	for _, e := range snap.Enclosures {
		if bad(e.Health) {
			add("磁盘框", firstNonEmptyOrDash(e.Location, e.Name), joinNonEmpty(e.Model, e.SerialNumber), e.Health)
		}
	}
	return out
}

// firstNonEmptyOrDash 取第一个非空串，**全空时返回 "-"**。
//
// 名字里的 OrDash 是后补的，而这正是重点：它原来就叫 firstNonEmpty，是给硬件报表
// 「字段空着就显示一个短横」用的展示辅助，却被当成通用的"取第一个非空值"在全仓用了 70 多处。
// 于是任何"全空"的场合都会悄悄拿到一个字符串 "-" 而不是 ""，后续再判 `!= ""` 就永远为真。
//
// 这不是假设——SQL 工作台就栽在这上面：库名解析写的是
//
//	schema := firstNonEmpty(req.Schema, req.Database)
//	if schema == "" { schema = 从 SQL 里推断 }
//	if schema == "" { schema = 连接自带的库名 }
//	if schema != "" && !reSafeIdent.MatchString(schema) { return 非法库名 }
//
// 请求里不带库名时 schema 直接变成 "-"，两个兜底一个都轮不到，最后被库名正则挡下——
// **只要用户没在界面上显式选库，查询就一定报"非法库名"**，而报错还完全不指向真正的原因。
//
// 展示场景请继续用这个函数（表格里空字段显示短横是对的）；凡是结果要当**值**用的
// （标识符、键、过滤条件、库名），一律用下面的 firstNonEmpty。
func firstNonEmptyOrDash(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return "-"
}

// firstNonEmpty 取第一个非空串，全空时返回空串——即"取值"该有的语义。
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func (h *SreyunCore) execQueryHardware(args map[string]any) (string, error) {
	_, name, snaps, errMsg := h.hwResolve(args)
	if errMsg != "" {
		return errMsg, nil
	}
	section := strings.ToLower(strings.TrimSpace(fmt.Sprint(args["section"])))
	if section == "" || section == "<nil>" {
		section = "summary"
	}

	var b strings.Builder
	for _, snap := range snaps {
		sys := snap.System
		fmt.Fprintf(&b, "设备 %s（主机 %s）\n", firstNonEmptyOrDash(snap.TargetName, snap.TargetURL), name)
		fmt.Fprintf(&b, "  型号: %s / 序列号: %s / BIOS: %s / BMC: %s %s\n",
			joinNonEmpty(sys.Manufacturer, sys.Model), firstNonEmptyOrDash(sys.SerialNumber, sys.SKU),
			firstNonEmptyOrDash(sys.BIOSVersion), firstNonEmptyOrDash(sys.BMCModel), sys.BMCFirmware)
		fmt.Fprintf(&b, "  整机健康: %s / 电源状态: %s\n", firstNonEmptyOrDash(snap.Health), firstNonEmptyOrDash(sys.PowerState))
		if snap.Error != "" {
			fmt.Fprintf(&b, "  ⚠ 采集错误: %s（以下数据可能是上一次成功采集的缓存）\n", snap.Error)
		}

		bads := hwBadParts(snap)
		if len(bads) > 0 {
			fmt.Fprintf(&b, "  异常部件 %d 个:\n%s\n", len(bads), strings.Join(bads, "\n"))
		} else {
			b.WriteString("  异常部件: 无\n")
		}

		if section == "summary" {
			fmt.Fprintf(&b, "  规模: CPU %d / 内存 %.0fGB(%d条) / 硬盘 %d / RAID卡 %d / GPU %d / 电源 %d / 风扇 %d / 磁盘框 %d\n",
				len(snap.CPUs), snap.Memory.TotalGB, len(snap.Memory.DIMMs), len(snap.Storage),
				len(snap.RAID), len(snap.GPUs), len(snap.Power.PSUs), len(snap.Fans), len(snap.Enclosures))
			continue
		}
		hwWriteSection(&b, snap, section)
	}
	if b.Len() == 0 {
		return "无硬件数据", nil
	}
	return b.String(), nil
}

func hwWriteSection(b *strings.Builder, snap shared.HardwareSnapshot, section string) {
	want := func(s string) bool { return section == "all" || section == s }

	if want("cpu") {
		for _, c := range snap.CPUs {
			fmt.Fprintf(b, "  [CPU] %s %s %dC/%dT %s\n", c.Name, c.Model, c.Cores, c.Threads, c.Health)
		}
	}
	if want("memory") {
		for _, d := range snap.Memory.DIMMs {
			fmt.Fprintf(b, "  [内存] 槽位 %s %.0fGB %s %dMHz %s SN=%s %s\n",
				firstNonEmptyOrDash(d.Slot, d.Name), d.CapacityGB, d.Type, d.SpeedMHz,
				d.Manufacturer, firstNonEmptyOrDash(d.SerialNumber), d.Health)
		}
	}
	if want("disk") {
		for _, d := range snap.Storage {
			life := "-"
			if d.LifeLeftPct >= 0 {
				life = fmt.Sprintf("%.0f%%", d.LifeLeftPct)
			}
			smart := "正常"
			if d.SMARTWarn {
				smart = "⚠预测故障"
			}
			fmt.Fprintf(b, "  [硬盘] 槽位 %s %s %.0fGB %s SN=%s 固件=%s 剩余寿命=%s SMART=%s %s\n",
				firstNonEmptyOrDash(d.Location), d.Model, d.CapacityGB, firstNonEmptyOrDash(d.MediaType, d.Protocol),
				firstNonEmptyOrDash(d.SerialNumber), firstNonEmptyOrDash(d.Revision), life, smart, d.Health)
		}
	}
	if want("raid") {
		for _, r := range snap.RAID {
			fmt.Fprintf(b, "  [RAID卡] %s %s 固件=%s 缓存=%.0fMB 挂盘=%d %s\n",
				r.Name, r.Model, firstNonEmptyOrDash(r.FirmwareVersion), r.CacheMB, r.DriveCount, r.Health)
			for _, v := range r.Volumes {
				fmt.Fprintf(b, "    [逻辑卷] %s %s %.0fGB %s\n", v.Name, v.RAIDType, v.CapacityGB, v.Health)
			}
		}
	}
	if want("gpu") {
		for _, g := range snap.GPUs {
			fmt.Fprintf(b, "  [GPU] %s %s %s %s\n", g.Name, g.Model, g.Manufacturer, g.Health)
		}
	}
	if want("psu") {
		fmt.Fprintf(b, "  电源冗余: %s\n", firstNonEmptyOrDash(snap.Power.Redundancy))
		for _, p := range snap.Power.PSUs {
			fmt.Fprintf(b, "  [电源] %s %s 输入=%.0fW 额定=%.0fW SN=%s %s\n",
				p.Name, p.Model, p.InputWatts, p.CapacityWatts, firstNonEmptyOrDash(p.SerialNumber), p.Health)
		}
	}
	if want("fan") {
		for _, f := range snap.Fans {
			fmt.Fprintf(b, "  [风扇] %s %d RPM %s\n", f.Name, f.RPM, f.Health)
		}
	}
	if want("temp") {
		for _, t := range snap.Temps {
			fmt.Fprintf(b, "  [温度] %s %.0f°C（阈值 %.0f/%.0f）%s\n",
				t.Name, t.Reading, t.UpperCaution, t.UpperCritical, t.Status)
		}
	}
	if want("enclosure") {
		for _, e := range snap.Enclosures {
			fmt.Fprintf(b, "  [磁盘框] %s %s SN=%s %.0f°C %s %s\n",
				firstNonEmptyOrDash(e.Location, e.Name), e.Model, firstNonEmptyOrDash(e.SerialNumber),
				e.TemperatureC, e.State, e.Health)
		}
	}
	if want("firmware") {
		for _, f := range snap.Firmware {
			fmt.Fprintf(b, "  [固件] %s = %s\n", f.Name, f.Version)
		}
	}
}

func (h *SreyunCore) execQueryHardwareEvents(args map[string]any) (string, error) {
	hostID, name, snaps, errMsg := h.hwResolve(args)
	if errMsg != "" {
		return errMsg, nil
	}
	limit := 20
	if n, ok := args["limit"].(float64); ok && n > 0 {
		limit = int(n)
	}

	var b strings.Builder
	total := 0
	for _, snap := range snaps {
		if len(snap.Events) == 0 {
			continue
		}
		fmt.Fprintf(&b, "设备 %s 的 BMC 事件日志:\n", firstNonEmptyOrDash(snap.TargetName, snap.TargetURL))
		for i, e := range snap.Events {
			if i >= limit {
				break
			}
			total++
			fmt.Fprintf(&b, "  %s [%s] 触发部件=%s | %s\n",
				firstNonEmptyOrDash(e.Created), firstNonEmptyOrDash(e.Severity), firstNonEmptyOrDash(e.Component), e.Message)
		}
	}
	// 平台侧记录的状态变化（BMC 没给事件日志时，这是唯一的时间线）
	if h.s.pg != nil {
		if evs, err := h.s.pg.getHardwareEvents(hostID, "", limit); err == nil && len(evs) > 0 {
			b.WriteString("监控侧记录的健康状态变化:\n")
			for _, e := range evs {
				fmt.Fprintf(&b, "  %v [%v] %v\n", e["created_at"], e["severity"], e["message"])
				total++
			}
		}
	}
	if total == 0 {
		return fmt.Sprintf("主机 %s 无硬件事件记录（BMC 未上报事件日志，且监控期间健康状态未发生变化）。", name), nil
	}
	return b.String(), nil
}

func (h *SreyunCore) execQueryHardwareHistory(args map[string]any) (string, error) {
	ref, _ := args["host_id"].(string)
	metric, _ := args["metric"].(string)
	if ref == "" || metric == "" {
		return "请指定 host_id 和 metric", nil
	}
	hostID, name := ref, ref
	if hst := h.resolveHostRef(ref); hst != nil {
		hostID, name = hst.ID, hst.Hostname
	}
	rangeStr, _ := args["range"].(string)
	if rangeStr == "" {
		rangeStr = "24h"
	}
	if !h.s.vm.enabled() {
		return "时序库未启用，无法查询硬件历史趋势。", nil
	}

	names := map[string]string{
		"temperature":  "aiops_hardware_temperature",
		"fan_rpm":      "aiops_hardware_fan_rpm",
		"power":        "aiops_hardware_power_watts",
		"health_score": "aiops_hardware_health_score",
	}
	m, ok := names[metric]
	if !ok {
		return "metric 只支持 temperature / fan_rpm / power / health_score", nil
	}
	from, to := parseTimeRange(rangeStr)
	// 直接要统计量而不是原始点：让模型读 min/avg/max 比读几百个采样点有效得多，也省 token
	q := fmt.Sprintf(`%s{host="%s"}`, m, hostID)
	points := h.s.vm.queryRawRange(q, from, to)
	if len(points) == 0 {
		return fmt.Sprintf("主机 %s 在最近 %s 内没有 %s 的历史数据。", name, rangeStr, metric), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "主机 %s 最近 %s 的 %s 趋势:\n", name, rangeStr, metric)
	for _, p := range points {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		series := ""
		if lbl, ok := pm["metric"].(map[string]any); ok {
			series = firstNonEmptyOrDash(fmt.Sprint(lbl["sensor"]), fmt.Sprint(lbl["fan_name"]), fmt.Sprint(lbl["target"]))
		}
		vals, _ := pm["values"].([]any)
		mn, mx, sum, n := 0.0, 0.0, 0.0, 0
		for _, v := range vals {
			pair, ok := v.([]any)
			if !ok || len(pair) < 2 {
				continue
			}
			f := parseFloatAny(pair[1])
			if n == 0 || f < mn {
				mn = f
			}
			if n == 0 || f > mx {
				mx = f
			}
			sum += f
			n++
		}
		if n == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %s: 最小 %.1f / 平均 %.1f / 最大 %.1f（%d 个采样点）\n",
			firstNonEmptyOrDash(series, "value"), mn, sum/float64(n), mx, n)
	}
	return b.String(), nil
}

func (h *SreyunCore) execQueryHardwareChanges(args map[string]any) (string, error) {
	ref, _ := args["host_id"].(string)
	if ref == "" {
		return "请指定 host_id", nil
	}
	hostID, name := ref, ref
	if hst := h.resolveHostRef(ref); hst != nil {
		hostID, name = hst.ID, hst.Hostname
	}
	if h.s.pg == nil {
		return "未配置 PostgreSQL，无法查询硬件变更历史。", nil
	}
	limit := 30
	if n, ok := args["limit"].(float64); ok && n > 0 {
		limit = int(n)
	}
	rows, err := h.s.pg.getHardwareChanges(hostID, "", limit)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return fmt.Sprintf("主机 %s 自纳管以来没有检测到硬件资产变更（未换过盘/内存/电源，固件也未升级）。", name), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "主机 %s 的硬件变更历史（新→旧）:\n", name)
	for _, r := range rows {
		fmt.Fprintf(&b, "  %v [%v] %v %v: %v → %v\n",
			r["created_at"], r["action"], r["kind"], r["component"],
			firstNonEmptyOrDash(fmt.Sprint(r["old_value"])), firstNonEmptyOrDash(fmt.Sprint(r["new_value"])))
	}
	return b.String(), nil
}

func (h *SreyunCore) execQueryNetFlow(args map[string]any) (string, error) {
	ref, _ := args["host_id"].(string)
	if ref == "" {
		return "请指定 host_id", nil
	}
	hostID, name := ref, ref
	if hst := h.resolveHostRef(ref); hst != nil {
		hostID, name = hst.ID, hst.Hostname
	}
	if h.s.pg == nil {
		return "未配置 PostgreSQL，无法查询流量明细。", nil
	}
	dim, _ := args["dimension"].(string)
	if dim == "" {
		dim = "dst_ip"
	}
	rangeStr, _ := args["range"].(string)
	if rangeStr == "" {
		rangeStr = "1h"
	}
	top := 10
	if n, ok := args["top"].(float64); ok && n > 0 {
		top = int(n)
	}
	from, to := parseTimeRange(rangeStr)
	rows, err := h.s.pg.getFlowSummary(hostID, dim, from, to, top)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return fmt.Sprintf("主机 %s 在最近 %s 内没有流量记录（未配置 NetFlow/抓包采集，或该时段无流量）。", name, rangeStr), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "主机 %s 最近 %s 按 %s 聚合的流量 Top%d:\n", name, rangeStr, dim, top)
	for i, r := range rows {
		fmt.Fprintf(&b, "  %d. %v — %s（%v 个包，%v 条流）\n",
			i+1, r["key"], humanBytes(toInt64(r["bytes"])), r["packets"], r["flows"])
	}
	return b.String(), nil
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

func parseFloatAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(x, "%g", &f)
		return f
	}
	return 0
}
