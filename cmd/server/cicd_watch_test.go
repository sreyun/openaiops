package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newCICDWatchTestServer 起一个假 GitLab：流水线列表由 runs 指针实时决定，
// 便于在两次巡检之间「让一条新流水线变红」。
func newCICDWatchTestServer(t *testing.T, runs *[]map[string]any) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		uri := r.RequestURI
		switch {
		case strings.Contains(uri, "/trace"):
			_, _ = w.Write([]byte("npm ERR! build failed\n"))
		case strings.Contains(uri, "/jobs"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 77, "name": "build", "stage": "build", "status": "failed", "failure_reason": "script_failure"},
			})
		case strings.Contains(uri, "/pipelines"):
			_ = json.NewEncoder(w).Encode(*runs)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newCICDWatchTestSrv(t *testing.T, conn CICDConnection) *Server {
	t.Helper()
	cs := &ConfigStore{cfg: ServerConfig{CICDConnections: []CICDConnection{conn}}}
	cs.path = filepath.Join(t.TempDir(), "config.json")
	return &Server{
		store:       NewStore(),
		cfg:         cs,
		incidents:   newIncidentManager(),
		cicdWatcher: newCICDFailureWatcher(),
	}
}

func cicdWatchConn(id, baseURL string) CICDConnection {
	return CICDConnection{
		ID: id, Name: "corp-gitlab", Provider: CICDProviderGitLab,
		BaseURL: baseURL, Project: "a/b", Enabled: true,
		WatchFailures: true, AutoIncident: true,
	}
}

func cicdWatchAlerts(s *Server) []AlertRecord {
	var out []AlertRecord
	for _, a := range s.store.AlertHistory(100, false) {
		if a.Type == "cicd_failed" {
			out = append(out, a)
		}
	}
	return out
}

// 首轮巡检必须静默吸收既有的红流水线：否则每次服务端重启都会把最近历史里的
// 全部失败重新告警一遍。之后新出现的失败才告警。
func TestCICDFailureWatcherSeedsBacklogThenAlertsNewFailure(t *testing.T) {
	runs := []map[string]any{
		{"id": 100, "iid": 1, "status": "failed", "ref": "main", "web_url": "https://g/1"},
	}
	srv := newCICDWatchTestServer(t, &runs)
	s := newCICDWatchTestSrv(t, cicdWatchConn("c1", srv.URL))

	InvalidateCICDCache("c1")
	s.runCICDFailureScan()
	if got := cicdWatchAlerts(s); len(got) != 0 {
		t.Fatalf("首轮应静默吸收既有失败，却告警了 %d 条：%+v", len(got), got)
	}
	if len(s.incidents.List()) != 0 {
		t.Fatalf("首轮不得登记事件，得 %d 个", len(s.incidents.List()))
	}

	// 新的一条红流水线出现。
	runs = append([]map[string]any{
		{"id": 101, "iid": 2, "status": "failed", "ref": "release", "web_url": "https://g/2"},
	}, runs...)
	InvalidateCICDCache("c1")
	s.runCICDFailureScan()

	alerts := cicdWatchAlerts(s)
	if len(alerts) != 1 {
		t.Fatalf("新失败应告警 1 条，得 %d 条：%+v", len(alerts), alerts)
	}
	if alerts[0].Key != "cicd/c1/101" {
		t.Errorf("告警 key = %q，应为 cicd/c1/101", alerts[0].Key)
	}
	if !strings.Contains(alerts[0].Message, "release") {
		t.Errorf("告警文案应含分支名，得 %q", alerts[0].Message)
	}

	incs := s.incidents.List()
	if len(incs) != 1 {
		t.Fatalf("AutoIncident 打开时应登记 1 个事件，得 %d 个", len(incs))
	}
	// 与手工「失败转事件」共用同一个 key，否则操作员点诊断会开出孪生事件。
	if incs[0].Key != "cicd/c1/101" {
		t.Errorf("事件 key = %q，应与手工路径一致（cicd/c1/101）", incs[0].Key)
	}
	// 证据取自失败任务日志，是 AI 诊断链路的输入。
	var evidence string
	for _, e := range incs[0].Timeline {
		if strings.Contains(e.Text, "npm ERR!") {
			evidence = e.Text
		}
	}
	if evidence == "" {
		t.Errorf("事件应附带失败任务日志证据，timeline=%+v", incs[0].Timeline)
	}
}

// 同一条失败在后续巡检里反复出现（它会在列表里待很久），只能告警一次。
func TestCICDFailureWatcherDedupsRepeatedFailure(t *testing.T) {
	runs := []map[string]any{{"id": 200, "iid": 1, "status": "success", "ref": "main"}}
	srv := newCICDWatchTestServer(t, &runs)
	s := newCICDWatchTestSrv(t, cicdWatchConn("c2", srv.URL))

	InvalidateCICDCache("c2")
	s.runCICDFailureScan() // 播种

	runs = []map[string]any{{"id": 201, "iid": 2, "status": "failed", "ref": "main"}}
	for i := 0; i < 3; i++ {
		InvalidateCICDCache("c2")
		s.runCICDFailureScan()
	}
	if got := cicdWatchAlerts(s); len(got) != 1 {
		t.Fatalf("重复巡检同一条失败应只告警 1 次，得 %d 次", len(got))
	}
	if got := len(s.incidents.List()); got != 1 {
		t.Fatalf("重复巡检应只登记 1 个事件，得 %d 个", got)
	}
}

// 连接被停用/取消勾选后必须丢弃基线：重新启用时按新基线播种，
// 而不是把停用期间攒下的红流水线一次性重放成告警风暴。
func TestCICDFailureWatcherReseedsAfterDisable(t *testing.T) {
	runs := []map[string]any{{"id": 300, "iid": 1, "status": "success", "ref": "main"}}
	srv := newCICDWatchTestServer(t, &runs)
	s := newCICDWatchTestSrv(t, cicdWatchConn("c3", srv.URL))

	InvalidateCICDCache("c3")
	s.runCICDFailureScan() // 播种

	// 停用连接。
	s.cfg.mu.Lock()
	s.cfg.cfg.CICDConnections[0].Enabled = false
	s.cfg.mu.Unlock()
	runs = []map[string]any{
		{"id": 301, "iid": 2, "status": "failed", "ref": "main"},
		{"id": 302, "iid": 3, "status": "failed", "ref": "main"},
	}
	InvalidateCICDCache("c3")
	s.runCICDFailureScan()
	if got := cicdWatchAlerts(s); len(got) != 0 {
		t.Fatalf("停用期间不得告警，得 %d 条", len(got))
	}

	// 重新启用：停用期间积压的失败属于旧账，应被重新播种吸收。
	s.cfg.mu.Lock()
	s.cfg.cfg.CICDConnections[0].Enabled = true
	s.cfg.mu.Unlock()
	InvalidateCICDCache("c3")
	s.runCICDFailureScan()
	if got := cicdWatchAlerts(s); len(got) != 0 {
		t.Fatalf("重新启用应重新播种而非重放积压，得 %d 条告警：%+v", len(got), got)
	}
}

// 只勾了「失败告警」没勾「自动事件」时，告警照发，但不得开事件。
func TestCICDFailureWatcherSkipsIncidentWhenNotOptedIn(t *testing.T) {
	runs := []map[string]any{{"id": 400, "iid": 1, "status": "success", "ref": "main"}}
	srv := newCICDWatchTestServer(t, &runs)
	c := cicdWatchConn("c4", srv.URL)
	c.AutoIncident = false
	s := newCICDWatchTestSrv(t, c)

	InvalidateCICDCache("c4")
	s.runCICDFailureScan()
	runs = []map[string]any{{"id": 401, "iid": 2, "status": "failed", "ref": "main"}}
	InvalidateCICDCache("c4")
	s.runCICDFailureScan()

	if got := cicdWatchAlerts(s); len(got) != 1 {
		t.Fatalf("应告警 1 条，得 %d 条", len(got))
	}
	if got := len(s.incidents.List()); got != 0 {
		t.Fatalf("未勾选自动事件时不得登记事件，得 %d 个", got)
	}
}

// 没勾「失败告警」的连接根本不该被巡检（省掉上游调用）。
func TestCICDFailureWatcherIgnoresUnwatchedConnection(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	c := cicdWatchConn("c5", srv.URL)
	c.WatchFailures = false
	s := newCICDWatchTestSrv(t, c)
	InvalidateCICDCache("c5")
	s.runCICDFailureScan()
	if hits != 0 {
		t.Fatalf("未勾选失败告警的连接不应产生上游调用，得 %d 次", hits)
	}
}

// SetCICDSyncResult 每次轮询都会被调用，而 save() 会把整份服务端配置
// （用户、剧本、全部连接）加密后整体写回 PG/磁盘。结果没变化时必须省掉这次写。
func TestSetCICDSyncResultSkipsRedundantPersist(t *testing.T) {
	dir := t.TempDir()
	cs := &ConfigStore{cfg: ServerConfig{
		CICDConnections: []CICDConnection{{ID: "p1", Name: "n", Enabled: true}},
	}}
	cs.path = filepath.Join(dir, "config.json")

	saved := func() bool {
		_, err := os.Stat(cs.path)
		return err == nil
	}
	reset := func() {
		_ = os.Remove(cs.path)
	}

	// 首轮：LastSyncAt 为 0 → 心跳过期，必须落盘。
	cs.SetCICDSyncResult("p1", "")
	if !saved() {
		t.Fatal("首次同步结果必须落盘")
	}

	// 结果未变且心跳新鲜 → 不落盘。
	reset()
	cs.SetCICDSyncResult("p1", "")
	if saved() {
		t.Fatal("结果未变化时不应重写整份配置")
	}
	// 内存态仍要刷新，否则前端看到的最后同步时间会停滞。
	if got, _ := cs.GetCICDConnection("p1"); got.LastSyncAt == 0 {
		t.Fatal("内存中的 LastSyncAt 必须每次刷新")
	}

	// 错误信息变化（操作员真正会看到的那部分）→ 必须落盘。
	reset()
	cs.SetCICDSyncResult("p1", "令牌无效")
	if !saved() {
		t.Fatal("错误信息变化必须落盘")
	}
	if got, _ := cs.GetCICDConnection("p1"); got.LastError != "令牌无效" {
		t.Fatalf("LastError = %q", got.LastError)
	}

	// 错误恢复（有 → 无）同样是变化。
	reset()
	cs.SetCICDSyncResult("p1", "")
	if !saved() {
		t.Fatal("错误恢复必须落盘")
	}

	// 心跳过期（超过 cicdSyncPersistEvery）→ 即便结果没变也要落盘一次。
	reset()
	cs.mu.Lock()
	cs.cfg.CICDConnections[0].LastSyncAt = time.Now().Add(-2 * cicdSyncPersistEvery).Unix()
	cs.mu.Unlock()
	cs.SetCICDSyncResult("p1", "")
	if !saved() {
		t.Fatal("心跳过期时必须落盘一次")
	}
}

// Gitee v5 用 ?access_token= 查询参数鉴权，传输层错误会带出整条 URL——
// 令牌会随之写进 CICDConnection.LastError（落库）并渲染给所有只读用户。
func TestCICDSanitizeErrScrubsToken(t *testing.T) {
	token := "gitee-secret-token"
	err := fmt.Errorf(`Get "https://gitee.com/api/v5/repos/a/b?access_token=%s": dial tcp: timeout`, token)
	got := cicdSanitizeErr(err, token)
	if strings.Contains(got.Error(), token) {
		t.Fatalf("令牌泄漏：%q", got.Error())
	}
	if !strings.Contains(got.Error(), "***") {
		t.Fatalf("应以 *** 占位，得 %q", got.Error())
	}
	// URL 里是转义后的拼写，也要一并清掉。
	escToken := "tok en+/=" // QueryEscape 后与原文不同
	err2 := fmt.Errorf(`Get "https://gitee.com/x?access_token=tok+en%%2B%%2F%%3D": timeout`)
	if out := cicdSanitizeErr(err2, escToken); strings.Contains(out.Error(), "tok+en%2B%2F%3D") {
		t.Fatalf("转义形式的令牌未被清理：%q", out.Error())
	}
	// 无令牌 / 无匹配时原样返回，不要凭空包一层丢掉错误类型。
	if out := cicdSanitizeErr(err, ""); out != err {
		t.Fatal("空令牌时应原样返回")
	}
	plain := fmt.Errorf("connection refused")
	if out := cicdSanitizeErr(plain, token); out != plain {
		t.Fatal("未命中令牌时应原样返回原错误")
	}
	if cicdSanitizeErr(nil, token) != nil {
		t.Fatal("nil 错误应返回 nil")
	}
}

// 端到端：Gitee 连接打到一个立刻断开的地址，cicdRequest 返回的错误里不能有令牌。
func TestCICDRequestErrorDoesNotLeakGiteeToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close() // 关掉 → 拨号必然失败，错误里带上完整 URL

	s := &Server{}
	c := CICDConnection{ID: "leak", Provider: CICDProviderGitee, BaseURL: addr,
		Project: "a/b", Token: "gitee-secret-token"}
	_, err := s.ListCICDRuns(context.Background(), c, 5)
	if err == nil {
		t.Fatal("拨号应失败")
	}
	if strings.Contains(err.Error(), "gitee-secret-token") {
		t.Fatalf("令牌泄漏到错误信息：%q", err.Error())
	}
}

// base_url 由操作员填写，属于「用户可影响 URL」的出站，必须过 SSRF 校验，
// 否则一条连接就能把云实例元数据（等价于 IAM 凭据）读出来。
func TestCICDHTTPClientBlocksCloudMetadata(t *testing.T) {
	client, err := cicdHTTPClient(CICDConnection{}, 5*time.Second)
	if err != nil {
		t.Fatalf("cicdHTTPClient: %v", err)
	}
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://100.100.100.200/latest/meta-data/",
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s 应被 SSRF 防护拒绝", target)
		}
		if !strings.Contains(err.Error(), "SSRF") {
			t.Fatalf("%s 的失败原因应是 SSRF 防护，得 %v", target, err)
		}
	}
}

// 巡检去重表必须有上界，否则长跑进程会一直攒 key。
func TestCICDWatcherSeenMapBounded(t *testing.T) {
	w := newCICDFailureWatcher()
	stale := time.Now().Add(-2 * cicdWatchSeenTTL).Unix()
	for i := 0; i < cicdWatchSeenMax+10; i++ {
		key := fmt.Sprintf("c/%d", i)
		w.markSeen(key)
		if i < cicdWatchSeenMax/2 { // 把前半段做旧，让清理有东西可删
			w.mu.Lock()
			w.seen[key] = stale
			w.mu.Unlock()
		}
	}
	w.mu.Lock()
	n := len(w.seen)
	w.mu.Unlock()
	if n > cicdWatchSeenMax {
		t.Fatalf("去重表未收敛：%d 条 > 上限 %d", n, cicdWatchSeenMax)
	}
}

// forget 只能清掉本连接的记录，不能误伤别的连接（前缀匹配易写错）。
func TestCICDWatcherForgetIsScopedToConnection(t *testing.T) {
	w := newCICDFailureWatcher()
	w.takeSeed("c1")
	w.takeSeed("c10")
	w.markSeen("c1/1")
	w.markSeen("c10/1")

	w.forget("c1")

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.seen["c1/1"]; ok {
		t.Error("c1 的记录应被清除")
	}
	if _, ok := w.seen["c10/1"]; !ok {
		t.Error("c10 的记录不得被误删")
	}
	if w.seeded["c10"] != true {
		t.Error("c10 的播种标记不得被误删")
	}
	if w.seeded["c1"] {
		t.Error("c1 的播种标记应被清除，以便重新播种")
	}
}
