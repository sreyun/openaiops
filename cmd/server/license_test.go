package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 授权层是要收钱的东西：判定错一次，要么把客户的产线打成只读，要么把该收的钱漏掉。
// 这一组测试把"状态机 + 准入 + 降级"三件事都钉死。

// licenseTestKey 生成一对临时签发密钥，并把公钥装进环境（进程内全局状态在
// t.Cleanup 里复原，避免污染同包内其它测试）。
func licenseTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIOPS_LICENSE_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	licenseResetForTest(t)
	return priv
}

// licenseResetForTest 隔离包级授权状态。
func licenseResetForTest(t *testing.T) {
	t.Helper()
	licMu.Lock()
	oldRaw, oldPayload, oldErr, oldInstall, oldPeak, oldDay := licRaw, licPayload, licLoadErr, licInstallID, licPeakHosts, licRemindDay
	licRaw, licPayload, licLoadErr, licPeakHosts, licRemindDay = "", nil, "", 0, ""
	if licInstallID == "" {
		licInstallID = "AIO-TEST-TEST-TEST-TEST"
	}
	licMu.Unlock()
	t.Cleanup(func() {
		licMu.Lock()
		licRaw, licPayload, licLoadErr, licInstallID, licPeakHosts, licRemindDay = oldRaw, oldPayload, oldErr, oldInstall, oldPeak, oldDay
		licMu.Unlock()
	})
}

func licenseSign(t *testing.T, priv ed25519.PrivateKey, p licensePayload) string {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, payload)
	return licenseTokenPrefix + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
}

func TestLicenseParseRoundTripAndTamper(t *testing.T) {
	priv := licenseTestKey(t)
	tok := licenseSign(t, priv, licensePayload{ID: "LIC-1", Customer: "某某集团", MaxHosts: 50,
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(72 * time.Hour).Unix()})

	// 邮件转发一圈之后常见的形态：BEGIN/END 包装 + 折行 + 前后空白，都必须能吃下。
	wrapped := "-----BEGIN AIOPS LICENSE-----\n" + tok[:40] + "\n  " + tok[40:] + "\n-----END AIOPS LICENSE-----\n"
	p, err := licenseParse(wrapped)
	if err != nil {
		t.Fatalf("合法授权应解析成功: %v", err)
	}
	if p.Customer != "某某集团" || p.MaxHosts != 50 {
		t.Fatalf("载荷读错了: %+v", p)
	}

	// 改一个字节 → 验签必须失败（否则客户改个 max_hosts 就白嫖）。
	parts := strings.Split(tok, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var tampered licensePayload
	if err := json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.MaxHosts = 100000
	bad, _ := json.Marshal(tampered)
	forged := licenseTokenPrefix + "." + base64.RawURLEncoding.EncodeToString(bad) + "." + parts[2]
	if _, err := licenseParse(forged); err == nil {
		t.Fatal("篡改后的授权居然验过了")
	}

	// 换一把签发密钥签的授权也必须被拒（防止第三方自签）。
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := licenseParse(licenseSign(t, other, licensePayload{ID: "x", Customer: "y", MaxHosts: 1})); err == nil {
		t.Fatal("他方签发的授权居然验过了")
	}
}

func TestLicenseStatusLifecycle(t *testing.T) {
	priv := licenseTestKey(t)
	t.Setenv("AIOPS_LICENSE_ENFORCE", "1")
	srv, _ := newTestServer(t)

	install := func(p licensePayload) licenseStatusView {
		t.Helper()
		if err := srv.applyLicense(licenseSign(t, priv, p), false); err != nil {
			t.Fatalf("applyLicense: %v", err)
		}
		return srv.licenseStatus()
	}

	// 有效期内 → active，且不降级。
	v := install(licensePayload{ID: "a", Customer: "c", MaxHosts: 10, ExpiresAt: time.Now().Add(240 * time.Hour).Unix()})
	if v.State != "active" || v.ReadOnly || v.BlockNewHosts {
		t.Fatalf("有效期内不应降级: %+v", v)
	}

	// 刚过期、仍在宽限期 → grace：横幅要提醒，但功能一律不动。
	v = install(licensePayload{ID: "b", Customer: "c", MaxHosts: 10, GraceDays: 30, ExpiresAt: time.Now().Add(-48 * time.Hour).Unix()})
	if v.State != "grace" || v.ReadOnly {
		t.Fatalf("宽限期内不应只读: %+v", v)
	}
	if v.GraceDaysLeft <= 0 || v.GraceDaysLeft > 30 {
		t.Fatalf("宽限剩余天数不合理: %+v", v)
	}
	if v.DaysLeft >= 0 {
		t.Fatalf("已过期应给负天数: %+v", v)
	}

	// 宽限期也过了 → expired + 只读降级。
	v = install(licensePayload{ID: "c", Customer: "c", MaxHosts: 10, GraceDays: 1, ExpiresAt: time.Now().Add(-72 * time.Hour).Unix()})
	if v.State != "expired" || !v.ReadOnly || !v.BlockNewHosts {
		t.Fatalf("过宽限期应只读: %+v", v)
	}

	// 永久授权：没有到期日就不该有任何到期判定。
	v = install(licensePayload{ID: "d", Customer: "c", MaxHosts: 10})
	if v.State != "active" || v.ReadOnly {
		t.Fatalf("永久授权判定错误: %+v", v)
	}

	// 额度**刚好用满**（used == max）就该报出来：对使用者而言，"用满"和"超出"
	// 是同一件事——下一台机器装不上。等到 used > max 才提示，客户第一次听说这件事
	// 会是在装第 N+1 台失败的时候。
	srv.store.RegisterHost("h1", "n1", "fp1")
	v = install(licensePayload{ID: "full", Customer: "c", MaxHosts: 1})
	if v.State != "over_quota" || !v.OverQuota || !v.BlockNewHosts {
		t.Fatalf("额度用满应判为 over_quota: %+v", v)
	}
	if v.ReadOnly {
		t.Fatalf("额度用满不该把平台打成只读: %+v", v)
	}

	// 主机超限 → over_quota：拦新增，但**不**转只读（在跑的监控不能停）。
	srv.store.RegisterHost("h2", "n2", "fp2")
	v = install(licensePayload{ID: "e", Customer: "c", MaxHosts: 1})
	if v.State != "over_quota" || !v.OverQuota || !v.BlockNewHosts {
		t.Fatalf("超限应拦新增: %+v", v)
	}
	if v.ReadOnly {
		t.Fatalf("超限不该把平台打成只读: %+v", v)
	}
	if v.UsedHosts != 2 || v.PeakHosts < 2 {
		t.Fatalf("计量口径不对: %+v", v)
	}
}

func TestLicenseUnlicensedOnlyBlocksWhenEnforced(t *testing.T) {
	licenseResetForTest(t)
	srv, _ := newTestServer(t)

	// 社区/自建（默认不强制）：没有授权也不能拦任何东西。
	t.Setenv("AIOPS_LICENSE_ENFORCE", "0")
	v := srv.licenseStatus()
	if v.State != "unlicensed" || v.ReadOnly || v.BlockNewHosts {
		t.Fatalf("未开启强制时不应有任何拦截: %+v", v)
	}

	// 商业交付镜像：未装授权 = 只读，但上传授权的入口必须留着。
	t.Setenv("AIOPS_LICENSE_ENFORCE", "1")
	v = srv.licenseStatus()
	if !v.ReadOnly || !v.BlockNewHosts {
		t.Fatalf("强制模式下未授权应降级: %+v", v)
	}
	if !licenseWriteAllowedPath("/api/v1/admin/license") || !licenseWriteAllowedPath("/api/v1/login") {
		t.Fatal("只读降级把上传授权/登录也堵死了，客户拿到新授权将无处可贴")
	}
}

func TestLicenseInstallIDBinding(t *testing.T) {
	priv := licenseTestKey(t)
	srv, _ := newTestServer(t)
	licMu.RLock()
	install := licInstallID
	licMu.RUnlock()

	if err := srv.applyLicense(licenseSign(t, priv, licensePayload{ID: "x", Customer: "c", MaxHosts: 5, InstallID: "AIO-OTHER-DEPLOY"}), false); err == nil {
		t.Fatal("绑定到别的部署的授权应被拒")
	}
	if v := srv.licenseStatus(); v.Licensed {
		t.Fatalf("被拒的授权不应生效: %+v", v)
	}
	if err := srv.applyLicense(licenseSign(t, priv, licensePayload{ID: "y", Customer: "c", MaxHosts: 5, InstallID: install}), false); err != nil {
		t.Fatalf("绑定本部署的授权应通过: %v", err)
	}
}

// TestLicenseBadUploadKeepsWorkingLicense 钉住一条容易写错的边界：管理员粘错文本
// 不能把一套正在生效的授权推进 invalid —— 那等于让一次误操作把平台打成只读。
func TestLicenseBadUploadKeepsWorkingLicense(t *testing.T) {
	priv := licenseTestKey(t)
	t.Setenv("AIOPS_LICENSE_ENFORCE", "1")
	srv, _ := newTestServer(t)
	good := licenseSign(t, priv, licensePayload{ID: "ok", Customer: "c", MaxHosts: 10,
		ExpiresAt: time.Now().Add(240 * time.Hour).Unix()})
	if err := srv.applyLicense(good, false); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"随手粘的一段废话", "AIOPS-LIC1.aaa.bbb", ""} {
		if err := srv.applyLicense(bad, false); err == nil {
			t.Fatalf("非法授权居然被接受: %q", bad)
		}
		v := srv.licenseStatus()
		if v.State != "active" || v.ReadOnly || v.Customer != "c" {
			t.Fatalf("误操作把生效中的授权弄坏了（输入 %q）: %+v", bad, v)
		}
	}

	// 绑到别的部署的授权同样不能顶掉当前这份。
	other := licenseSign(t, priv, licensePayload{ID: "other", Customer: "someone-else", MaxHosts: 1, InstallID: "AIO-NOT-THIS-ONE"})
	if err := srv.applyLicense(other, false); err == nil {
		t.Fatal("跨部署授权应被拒")
	}
	if v := srv.licenseStatus(); v.Customer != "c" || v.MaxHosts != 10 {
		t.Fatalf("跨部署授权顶掉了当前授权: %+v", v)
	}
}

// TestLicenseGateDegradesToReadOnly 验证降级的边界：读不受影响、Agent 反向通道
// 不受影响、写被拦，且拦的时候带着可执行的说明。
func TestLicenseGateDegradesToReadOnly(t *testing.T) {
	priv := licenseTestKey(t)
	t.Setenv("AIOPS_LICENSE_ENFORCE", "1")
	srv, _ := newTestServer(t)
	if err := srv.applyLicense(licenseSign(t, priv, licensePayload{ID: "z", Customer: "c", MaxHosts: 10,
		GraceDays: 1, ExpiresAt: time.Now().Add(-72 * time.Hour).Unix()}), false); err != nil {
		t.Fatal(err)
	}

	reached := false
	h := srv.licenseGateMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	call := func(method, path string) int {
		reached = false
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader("{}")))
		return rr.Code
	}

	if code := call(http.MethodGet, "/api/v1/hosts"); code != http.StatusOK || !reached {
		t.Fatalf("只读降级不该拦读接口: %d", code)
	}
	if code := call(http.MethodPost, "/api/v1/agent/report"); code != http.StatusOK || !reached {
		t.Fatalf("只读降级不该拦 Agent 上报: %d", code)
	}
	if code := call(http.MethodPost, "/api/v1/admin/license"); code != http.StatusOK || !reached {
		t.Fatalf("只读降级不该拦授权上传: %d", code)
	}
	if code := call(http.MethodPost, "/api/v1/playbooks"); code != http.StatusPaymentRequired || reached {
		t.Fatalf("过期后写操作应被拦: %d", code)
	}

	// 数据摄入通道必须一律放行：拦掉它们等于授权一过期就开始丢客户的数据，
	// 而这些数据补不回来——比停服还糟，也与"降级不停服"的承诺直接矛盾。
	for _, p := range []string{
		"/api/v1/prom/write",                 // 外部 exporter 推送
		"/api/v1/integrations/content-audit", // LLM 网关审计上报
		"/api/v1/agent/logs",                 // Agent 日志
		"/api/v1/agent/hardware",             // Agent 硬件快照
		"/api/v1/mcp",                        // 只读白名单 MCP
	} {
		if code := call(http.MethodPost, p); code != http.StatusOK || !reached {
			t.Fatalf("摄入/只读通道 %s 不该被授权层拦掉: %d", p, code)
		}
	}

	// 关掉强制（社区/自建）后，同样的过期授权不得拦任何东西。
	t.Setenv("AIOPS_LICENSE_ENFORCE", "0")
	if code := call(http.MethodPost, "/api/v1/playbooks"); code != http.StatusOK || !reached {
		t.Fatalf("未开启强制时不应拦写操作: %d", code)
	}
}

// TestLicenseQuotaBlocksNewHostOnly 是这一层最关键的商业承诺：超限只挡新机器，
// 已登记的机器照常注册/上报。
func TestLicenseQuotaBlocksNewHostOnly(t *testing.T) {
	priv := licenseTestKey(t)
	t.Setenv("AIOPS_LICENSE_ENFORCE", "1")
	srv, token := newTestServer(t)
	if err := srv.applyLicense(licenseSign(t, priv, licensePayload{ID: "q", Customer: "c", MaxHosts: 1,
		ExpiresAt: time.Now().Add(240 * time.Hour).Unix()}), false); err != nil {
		t.Fatal(err)
	}

	rr := postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": "h-1", "hostname": "n1", "token": token, "fingerprint": "fp-1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("额度内的第一台应注册成功: %d %s", rr.Code, rr.Body)
	}

	rr = postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": "h-2", "hostname": "n2", "token": token, "fingerprint": "fp-2"})
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("超额的新主机应被拒: %d %s", rr.Code, rr.Body)
	}
	if _, ok := srv.store.GetHost("h-2"); ok {
		t.Fatal("被拒的主机不该进库（否则下次就变成'已登记'绕过额度）")
	}

	// 已登记主机重新注册（重启/重装恢复）必须照常通过。
	rr = postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": "h-1", "hostname": "n1", "token": token, "fingerprint": "fp-1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("已登记主机重注册应放行: %d %s", rr.Code, rr.Body)
	}
}
