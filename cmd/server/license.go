package main

// 授权与计量层。
//
// 私有化交付一旦把镜像交出去，主机数、有效期就再没有任何约束——续费只能靠人情。
// 这一层给出可离线验证的授权文件：Ed25519 签名的 JSON，锁「最大主机数 + 到期日 +
// 部署指纹」，服务端只用内置公钥验签，**不需要联网回调许可服务器**（客户内网大多
// 也不允许）。
//
// 三条刻意的设计边界：
//
//  1. **超限降级，不停服**。授权过期或主机超限时，正在跑的采集、告警、Agent 上报
//     一律照常——监控产品在客户产线上瞎眼，比欠费严重得多。降级只落在「人发起的
//     写操作」上（非 GET 的 /api/v1 请求），并始终放行登录、改密和授权上传本身，
//     否则客户拿到新授权也没地方贴。
//  2. **默认不强制**。开源自建部署不该被授权层拦住，所以 licenseEnforceDefault 是
//     "0"，商业交付镜像用 -X main.licenseEnforceDefault=1 或 AIOPS_LICENSE_ENFORCE=1
//     打开。关闭强制时这一层只做「显示 + 计量」。
//  3. **绑部署而不是绑机器**。容器换一次调度 machine-id 就变了，绑机器指纹等于让
//     客户每次重建都来要授权。这里绑的是首次启动生成、随 PG 一起持久化的 install_id，
//     搬迁数据库=同一套部署，重装数据库=新部署，与商业口径一致。
//
// 授权文件形如：AIOPS-LIC1.<base64url(payload)>.<base64url(sig)>

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	licenseTokenPrefix  = "AIOPS-LIC1"
	licenseKVKey        = "license"
	licenseInstallKV    = "install_id"
	licenseMeterKV      = "license_meter"
	licenseDefaultGrace = 30 // 天：过期后仍全功能的宽限期，避免续费流程卡在采购
)

// licenseVendorPubKey 是签发方公钥（base64）。发行版可用
// -ldflags "-X main.licenseVendorPubKey=<b64>" 替换成自己的签发密钥；
// 环境变量 AIOPS_LICENSE_PUBKEY 优先级更高，便于私有分发方自签。
var licenseVendorPubKey = "IZiOaHiEPiVVd1cL5YIYt//B38FeN3Hifstir/GsZZc="

// licenseEnforceDefault 决定这个二进制是否强制授权。"0" = 社区/自建（只显示不拦截）。
var licenseEnforceDefault = "0"

// 授权状态是全局单例：Server 结构体上不再新增字段（往那上面加字段会打断开源镜像仓
// 的发版构建，见仓库里的历史教训），所以状态放包级变量。
var (
	licMu        sync.RWMutex
	licRaw       string          // 授权原文（回显给管理员核对用）
	licPayload   *licensePayload // 验签通过的载荷；nil = 未授权
	licLoadErr   string          // 最近一次解析/验签失败原因
	licInstallID string          // 部署指纹
	licPeakHosts int             // 计量：历史峰值主机数
	licPeakTS    int64           //
	licRemindDay string          // 到期提醒去重（按天）
	// licMeterLoaded 表示历史峰值已经从 PG 成功读回来了。没读到就不许回写——
	// 否则一次读失败会把库里的峰值改写成当前值（见 loadLicense 里的说明）。
	licMeterLoaded bool
)

type licensePayload struct {
	ID        string `json:"id"`
	Customer  string `json:"customer"`
	Edition   string `json:"edition,omitempty"`
	MaxHosts  int    `json:"max_hosts"` // 0 = 不限
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"` // 0 = 永久
	GraceDays int    `json:"grace_days,omitempty"`
	InstallID string `json:"install_id,omitempty"` // 空 = 不绑部署
	Notes     string `json:"notes,omitempty"`
}

// licenseStatusView 是 /api/v1/license 的响应，也是控制台横幅的唯一数据源。
type licenseStatusView struct {
	State         string `json:"state"` // unlicensed|active|over_quota|grace|expired|invalid
	Enforced      bool   `json:"enforced"`
	Licensed      bool   `json:"licensed"`
	Customer      string `json:"customer,omitempty"`
	Edition       string `json:"edition,omitempty"`
	LicenseID     string `json:"license_id,omitempty"`
	InstallID     string `json:"install_id"`
	MaxHosts      int    `json:"max_hosts"`
	UsedHosts     int    `json:"used_hosts"`
	PeakHosts     int    `json:"peak_hosts"`
	IssuedAt      int64  `json:"issued_at,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	DaysLeft      int    `json:"days_left"`       // 负数=已过期天数
	GraceDaysLeft int    `json:"grace_days_left"` // 过期后剩余宽限天数
	OverQuota     bool   `json:"over_quota"`
	ReadOnly      bool   `json:"read_only"`       // 是否已降级为只读
	BlockNewHosts bool   `json:"block_new_hosts"` // 是否拒绝新主机注册
	Error         string `json:"error,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

// --- 解析与验签 -------------------------------------------------------------

func licensePubKey() (ed25519.PublicKey, error) {
	b64 := strings.TrimSpace(os.Getenv("AIOPS_LICENSE_PUBKEY"))
	if b64 == "" {
		b64 = strings.TrimSpace(licenseVendorPubKey)
	}
	if b64 == "" {
		return nil, errors.New("no license public key configured")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("bad license public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("bad license public key size %d", len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// licenseParse 校验签名并返回载荷。授权文件常被邮件/微信转发一圈，所以先把换行、
// 空格、BEGIN/END 包装行全部剥掉再解析——用户复制多了一个换行不该报"格式错误"。
func licenseParse(raw string) (*licensePayload, error) {
	cleaned := licenseCleanToken(raw)
	parts := strings.Split(cleaned, ".")
	if len(parts) != 3 || parts[0] != licenseTokenPrefix {
		return nil, errors.New("license format")
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("license format")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("license format")
	}
	pub, err := licensePubKey()
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(pub, payloadRaw, sig) {
		return nil, errors.New("license signature")
	}
	var p licensePayload
	if err := json.Unmarshal(payloadRaw, &p); err != nil {
		return nil, errors.New("license format")
	}
	if p.ID == "" || p.MaxHosts < 0 {
		return nil, errors.New("license format")
	}
	return &p, nil
}

func licenseCleanToken(raw string) string {
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-----") {
			continue
		}
		b.WriteString(strings.Join(strings.Fields(line), ""))
	}
	return b.String()
}

func licenseEnforced() bool {
	if v := strings.TrimSpace(os.Getenv("AIOPS_LICENSE_ENFORCE")); v != "" {
		return truthyEnv(v)
	}
	return truthyEnv(licenseEnforceDefault)
}

func truthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// --- 部署指纹 ---------------------------------------------------------------

// newInstallID 生成人眼可读、可口述的部署指纹（AIO-XXXX-XXXX-XXXX）。
func newInstallID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("AIO-%d", time.Now().UnixNano())
	}
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return "AIO-" + s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// --- 加载与持久化 -----------------------------------------------------------

// loadLicense 在启动时装配：部署指纹 → 计量 → 授权原文（kv_state 优先，
// 其次 AIOPS_LICENSE_FILE 指定的文件，首次读到即落库）。
func (s *Server) loadLicense() {
	licMu.Lock()
	if licInstallID == "" {
		licInstallID = newInstallID()
	}
	licMu.Unlock()

	if s.pg != nil {
		s.loadInstallID()
		// 计量同理不能"读失败就当没有"：历史峰值是续费谈判的依据，若读不到就从 0 重新
		// 起算，第一次 licenseObserve 就会把库里那个 500 台的峰值改写成今天的 100 台——
		// 数字被悄悄改小，而且没人会发现。读失败时**只在内存里跟踪、不落库**，
		// 等下次启动读成功再恢复持久化。
		if raw, err := s.loadKVRetry(licenseMeterKV); err != nil {
			slog.Error("读取授权计量失败：本次启动不会改写已持久化的历史峰值", "err", err)
		} else {
			licMu.Lock()
			licMeterLoaded = true
			licMu.Unlock()
			if len(raw) > 0 {
				var m struct {
					Peak int   `json:"peak_hosts"`
					TS   int64 `json:"peak_ts"`
				}
				if json.Unmarshal(raw, &m) == nil {
					licMu.Lock()
					licPeakHosts, licPeakTS = m.Peak, m.TS
					licMu.Unlock()
				}
			}
		}
	}

	stored := ""
	if s.pg != nil {
		if raw, err := s.loadKVRetry(licenseKVKey); err == nil && len(raw) > 0 {
			var v struct {
				License string `json:"license"`
			}
			if json.Unmarshal(raw, &v) == nil {
				stored = v.License
			}
		}
	}
	fromFile := false
	if stored == "" {
		if path := strings.TrimSpace(os.Getenv("AIOPS_LICENSE_FILE")); path != "" {
			if b, err := os.ReadFile(path); err == nil {
				stored, fromFile = string(b), true
			} else {
				slog.Warn("读取授权文件失败", "path", path, "err", err)
			}
		}
	}
	if stored == "" {
		if licenseEnforced() {
			slog.Warn("未安装授权文件：写操作将降级为只读，Agent 上报与告警不受影响（在控制台「系统设置 → 授权」上传授权文件）")
		}
		return
	}
	s.applyLicense(stored, fromFile)
}

// loadKVRetry 给授权层的两次 kv 读加一层重试。
//
// 启动时 PG 抖一下的代价在这里特别大：读不到就会被当成"这是一套全新部署"。
// 主循环连 PG 已经重试过 10 次，走到这里再失败属于异常，短重试足够覆盖
// 主备切换那几秒。
func (s *Server) loadKVRetry(key string) ([]byte, error) {
	var lastErr error
	for i := 0; i < 3; i++ {
		raw, err := s.pg.loadKV(key)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	return nil, lastErr
}

// loadInstallID 装配部署指纹，并且**只在确认库里没有时**才写入新的。
//
// 这里曾经把"读失败"和"没有这条记录"合并成同一个 else 分支：PG 在启动那一刻抖一下，
// 就会生成一个新指纹并 UPSERT 覆盖掉库里原来那条——客户按旧指纹签发的授权从此
// install mismatch，平台降级只读，而旧指纹已经被写没了，连重新签发都无从谈起。
// 一次瞬时读错误不该有这种破坏力，所以：读失败 → 什么都不写；确认没有 → 用
// INSERT ... DO NOTHING 写，写不进去说明别的进程刚写过，把它读回来用。
func (s *Server) loadInstallID() {
	raw, err := s.loadKVRetry(licenseInstallKV)
	if err != nil {
		slog.Error("读取部署指纹失败：本次启动不会改动已持久化的指纹（授权可能暂时判为未授权，恢复 PostgreSQL 后重启即可）", "err", err)
		return
	}
	adopt := func(b []byte) bool {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(b, &v) != nil || v.ID == "" {
			return false
		}
		licMu.Lock()
		licInstallID = v.ID
		licMu.Unlock()
		return true
	}
	if len(raw) > 0 && adopt(raw) {
		return
	}
	licMu.RLock()
	id := licInstallID
	licMu.RUnlock()
	blob, _ := json.Marshal(map[string]any{"id": id, "created": time.Now().Unix()})
	inserted, err := s.pg.saveKVIfAbsent(licenseInstallKV, blob)
	if err != nil {
		slog.Warn("部署指纹落库失败", "err", err)
		return
	}
	if inserted {
		return
	}
	// 没写进去 = 库里已经有一条（并发启动，或上面那条 JSON 解析不出来）。以库里的为准。
	if cur, err := s.pg.loadKV(licenseInstallKV); err == nil && len(cur) > 0 {
		adopt(cur)
	}
}

// applyLicense 验签后写入内存状态；fromFile=true 时顺带落库，
// 这样挂载的授权文件被移除后重启仍然有效。
func (s *Server) applyLicense(raw string, persist bool) error {
	p, err := licenseParse(raw)
	licMu.RLock()
	current := licPayload
	install := licInstallID
	licMu.RUnlock()

	// 校验失败时**绝不动**已经生效的授权。否则管理员粘错一段文本，
	// 就能把一套正常运行的平台推进 invalid → 只读降级——错误的输入不该有破坏力。
	if err == nil && p.InstallID != "" && p.InstallID != install {
		err = errors.New("license install mismatch")
		slog.Warn("授权文件与本部署不匹配", "license_install", p.InstallID, "this_install", install)
	}
	if err != nil {
		slog.Warn("授权文件无效", "err", err)
		if current == nil { // 本来就没有有效授权：把失败原因留给控制台显示
			licMu.Lock()
			licRaw, licPayload, licLoadErr = licenseCleanToken(raw), nil, err.Error()
			licMu.Unlock()
		}
		return err
	}
	licMu.Lock()
	licRaw, licPayload, licLoadErr = licenseCleanToken(raw), p, ""
	licMu.Unlock()
	if persist && s.pg != nil {
		blob, _ := json.Marshal(map[string]string{"license": licenseCleanToken(raw)})
		if err := s.pg.saveKV(licenseKVKey, blob); err != nil {
			slog.Warn("授权文件落库失败", "err", err)
		}
	}
	slog.Info("授权已加载", "customer", p.Customer, "max_hosts", p.MaxHosts, "expires", licenseDateStr(p.ExpiresAt))
	return nil
}

func licenseDateStr(ts int64) string {
	if ts <= 0 {
		return "永久"
	}
	return time.Unix(ts, 0).Format("2006-01-02")
}

// --- 状态计算 ---------------------------------------------------------------

// licenseUsedHosts 是计量口径：**已登记主机数**（含离线），与销售按主机数报价一致。
func (s *Server) licenseUsedHosts() int {
	if s.store == nil {
		return 0
	}
	// 用 hostCount() 而不是 len(ListHosts())：后者会整份复制主机结构（含最新采样切片），
	// 而这个函数在 /api/v1/summary 与 /metrics 的每次轮询里都会被调到。
	return s.store.hostCount()
}

// licenseObserve 记录峰值主机数。续费谈判要的是"这一年最多接过多少台"，
// 只看当下的数字会被客户在续费前一天下线一批机器抹平。
func (s *Server) licenseObserve(used int) {
	licMu.Lock()
	changed := false
	if used > licPeakHosts {
		licPeakHosts, licPeakTS = used, time.Now().Unix()
		changed = true
	}
	peak, ts := licPeakHosts, licPeakTS
	persist := licMeterLoaded
	licMu.Unlock()
	if changed && persist && s.pg != nil {
		blob, _ := json.Marshal(map[string]any{"peak_hosts": peak, "peak_ts": ts})
		if err := s.pg.saveKV(licenseMeterKV, blob); err != nil {
			slog.Warn("授权计量落库失败", "err", err)
		}
	}
}

// licenseStatus 计算当前授权状态。这是唯一的判定入口：注册准入、只读降级、
// 控制台横幅都读它，避免三处各写一套过期算法。
func (s *Server) licenseStatus() licenseStatusView {
	used := s.licenseUsedHosts()
	s.licenseObserve(used)

	licMu.RLock()
	p := licPayload
	loadErr := licLoadErr
	install := licInstallID
	peak := licPeakHosts
	licMu.RUnlock()

	v := licenseStatusView{
		State:     "unlicensed",
		Enforced:  licenseEnforced(),
		InstallID: install,
		UsedHosts: used,
		PeakHosts: peak,
	}
	if p == nil {
		if loadErr != "" {
			v.State, v.Error = "invalid", loadErr
		}
		v.ReadOnly = v.Enforced
		v.BlockNewHosts = v.Enforced
		return v
	}

	v.Licensed = true
	v.Customer, v.Edition, v.LicenseID = p.Customer, p.Edition, p.ID
	v.MaxHosts, v.IssuedAt, v.ExpiresAt, v.Notes = p.MaxHosts, p.IssuedAt, p.ExpiresAt, p.Notes
	grace := p.GraceDays
	if grace <= 0 {
		grace = licenseDefaultGrace
	}
	now := time.Now()
	if p.ExpiresAt > 0 {
		exp := time.Unix(p.ExpiresAt, 0)
		v.DaysLeft = int(exp.Sub(now).Hours() / 24)
		graceEnd := exp.AddDate(0, 0, grace)
		switch {
		case now.Before(exp):
			v.State = "active"
			v.GraceDaysLeft = grace
		case now.Before(graceEnd):
			v.State = "grace"
			v.GraceDaysLeft = int(graceEnd.Sub(now).Hours() / 24)
		default:
			v.State = "expired"
		}
	} else {
		v.State = "active"
	}
	// 判定用 >=：额度用满和超出，对使用者是同一件事——下一台机器装不上。
	// 只在 used > max 才报警，等于让"刚好用满"的客户在装第 N+1 台时才第一次听说这件事。
	if p.MaxHosts > 0 && used >= p.MaxHosts {
		v.OverQuota = true
		if v.State == "active" {
			v.State = "over_quota"
		}
	}
	if v.Enforced {
		v.ReadOnly = v.State == "expired"
		v.BlockNewHosts = v.ReadOnly || v.OverQuota ||
			(p.MaxHosts > 0 && used >= p.MaxHosts)
	}
	s.licenseRemind(v)
	return v
}

// licenseRemind 在到期前 30 天内每天往系统日志写一条，续费提醒不能只靠横幅——
// 值班的人未必每天打开控制台，但审计日志会被巡检看到。
func (s *Server) licenseRemind(v licenseStatusView) {
	if !v.Licensed || v.ExpiresAt <= 0 {
		return
	}
	if v.DaysLeft > 30 {
		return
	}
	day := time.Now().Format("2006-01-02")
	licMu.Lock()
	if licRemindDay == day {
		licMu.Unlock()
		return
	}
	licRemindDay = day
	licMu.Unlock()
	level := "warning"
	if v.State == "expired" {
		level = "critical"
	}
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: level,
		Message: Tz("log.license_expiring", v.Customer, licenseDateStr(v.ExpiresAt), v.DaysLeft)})
}

// --- 准入与降级 -------------------------------------------------------------

// licenseAllowNewHost 供 Agent 注册路径调用：只拦**新主机**，已登记的机器
// 永远放行（超限之后把在跑的机器踢下线，就是把客户的监控打瞎）。
func (s *Server) licenseAllowNewHost() (bool, licenseStatusView) {
	v := s.licenseStatus()
	if !v.Enforced {
		return true, v
	}
	return !v.BlockNewHosts, v
}

// licenseWriteAllowedPath 是只读降级下仍然放行的路径。分三类：
//
//  1. 账号自助（登录/登出/改密/账号初始化/MFA/SSO 回调）与**授权上传本身**——
//     客户拿到新授权总得有地方贴。
//  2. Agent 反向通道。
//  3. **所有数据摄入通道**：Prometheus remote_write、LLM 网关的内容审计上报、
//     Agent 日志。这些和 Agent 上报是同一件事——都是"数据往里进"。把它们按写操作
//     拦掉，等于授权一过期就开始**丢客户的数据**，而这些数据是补不回来的；
//     承诺的是"降级不停服"，丢数据比停服还糟。
//     MCP 同理放行：它是只读白名单工具，拦掉只是打断客户的外部 Agent，
//     对"促成续费"毫无帮助。
func licenseWriteAllowedPath(p string) bool {
	switch p {
	case "/api/v1/login", "/api/v1/login/sms-code", "/api/v1/logout", "/api/v1/password",
		"/api/v1/me", "/api/v1/account/init", "/api/v1/admin/license",
		"/api/v1/prom/write",                 // 外部 exporter/telegraf/OTel 推送
		"/api/v1/integrations/content-audit", // LLM 网关的结构化审计上报
		"/api/v1/mcp":                        // 对外 MCP（Bearer + 只读白名单）
		return true
	}
	if strings.HasPrefix(p, "/api/v1/agent/") ||
		strings.HasPrefix(p, "/api/v1/account/") ||
		strings.HasPrefix(p, "/api/v1/mfa/") ||
		strings.HasPrefix(p, "/api/v1/auth/") {
		return true
	}
	return false
}

// licenseGateMiddleware 把「过期未续费」降级成只读，而不是停服：GET 全放行，
// 非 GET 的 /api/v1 写操作返回 402 并带上明确说明。挂在 auth 之内、Routes 之外。
func (s *Server) licenseGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !licenseEnforced() || r.Method == http.MethodGet || r.Method == http.MethodOptions ||
			!strings.HasPrefix(r.URL.Path, "/api/v1/") || licenseWriteAllowedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if v := s.licenseStatus(); v.ReadOnly {
			// 未安装授权和"装过但过期了"是两回事，别用同一句话——
			// 客户照着"请续期"去找供应商，结果对方发现他压根没装过授权。
			key := "license.read_only"
			switch v.State {
			case "unlicensed":
				key = "license.not_installed"
			case "invalid":
				key = "license.read_only_invalid"
			}
			writeJSON(w, http.StatusPaymentRequired, map[string]any{
				"error":   Tr(r, key),
				"license": v,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- HTTP 接口 --------------------------------------------------------------

// handleLicenseStatus GET /api/v1/license —— 控制台横幅与授权页的数据源（viewer+）。
func (s *Server) handleLicenseStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.licenseStatus())
}

// handleLicenseInstall POST /api/v1/admin/license —— 上传授权文件（admin）。
func (s *Server) handleLicenseInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		License string `json:"license"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.License) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.applyLicense(req.License, true); err != nil {
		key := "license.invalid"
		switch err.Error() {
		case "license signature":
			key = "license.bad_signature"
		case "license install mismatch":
			key = "license.install_mismatch"
		case "license format":
			key = "license.bad_format"
		}
		s.addAuditLog(r, LogEntry{Kind: KindSystem, Level: "warning", Message: Tz("log.license_install_failed", err.Error())})
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, key)})
		return
	}
	v := s.licenseStatus()
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info",
		Message: Tz("log.license_installed", v.Customer, v.MaxHosts, licenseDateStr(v.ExpiresAt))})
	writeJSON(w, http.StatusOK, v)
}

// handleLicenseDelete DELETE /api/v1/admin/license —— 移除授权（admin）。
func (s *Server) handleLicenseDelete(w http.ResponseWriter, r *http.Request) {
	licMu.Lock()
	licRaw, licPayload, licLoadErr = "", nil, ""
	licMu.Unlock()
	if s.pg != nil {
		if err := s.pg.saveKV(licenseKVKey, []byte(`{"license":""}`)); err != nil {
			slog.Warn("清除授权落库失败", "err", err)
		}
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Message: Tz("log.license_removed")})
	writeJSON(w, http.StatusOK, s.licenseStatus())
}
