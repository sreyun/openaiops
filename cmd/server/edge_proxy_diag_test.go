package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bufferedAgent 模拟一台在**配错的反向代理**后面的 Agent：
// wait / alive 这类小请求照常到达服务端，而 tx 那条"边跑边写"的上行流被
// nginx 的 proxy_request_buffering 整包缓冲，直到命令结束才一次性转发上游。
type bufferedAgent struct {
	base, hostID, fp string
	txDelay          time.Duration // tx 被缓冲住多久（模拟 nginx 攒完整个请求体）
	output           string
	stop             chan struct{}
	txSent           chan struct{}
}

func (a *bufferedAgent) run(t *testing.T) {
	t.Helper()
	go func() {
		cl := &http.Client{Timeout: 40 * time.Second}
		for {
			select {
			case <-a.stop:
				return
			default:
			}
			req, _ := http.NewRequest(http.MethodGet,
				a.base+"/api/v1/agent/terminal/wait?host="+a.hostID, nil)
			req.Header.Set("X-Agent-Fingerprint", a.fp)
			resp, err := cl.Do(req)
			if err != nil {
				return
			}
			var out struct {
				Session string `json:"session"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if out.Session == "" {
				continue
			}
			// 测试只需要一个会话；接单后就别再挂长轮询，否则 httptest.Close
			// 要等满一整个 25s 的 wait 窗口。
			a.serveSession(cl, out.Session)
			return
		}
	}()
}

func (a *bufferedAgent) serveSession(cl *http.Client, sid string) {
	// 接单后 Agent 立刻开始 1.5s 一次的存活心跳（这里加密到 50ms 以便测试）。
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-a.stop:
				return
			case <-tick.C:
			}
			req, _ := http.NewRequest(http.MethodGet,
				a.base+"/api/v1/agent/terminal/alive?session="+sid, nil)
			req.Header.Set("X-Agent-Fingerprint", a.fp)
			if resp, err := cl.Do(req); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
	}()
	// 被缓冲的 tx：请求头也到不了服务端，直到 nginx 收全请求体才整包转发。
	time.Sleep(a.txDelay)
	close(done)
	req, _ := http.NewRequest(http.MethodPost,
		a.base+"/api/v1/agent/terminal/tx?session="+sid, strings.NewReader(a.output))
	req.Header.Set("X-Agent-Fingerprint", a.fp)
	req.Header.Set("Content-Type", "application/octet-stream")
	if resp, err := cl.Do(req); err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	select {
	case <-a.txSent:
	default:
		close(a.txSent)
	}
}

// 反向代理开着 nginx 默认的 proxy_request_buffering 时，Agent 的 tx 上行流要等命令
// 结束才到达服务端，接单信号必然迟到。这以前会被直接判成「Agent 未接单」——于是
// Agent 自动升级永远失败，而失败原因把矛头指向 Agent，运维照着查只会白查。
//
// 修好之后：服务端拿 alive 心跳当旁证，认出这是反代在缓冲，记一条带修复方法的告警，
// 并把等待延到命令自己的预算上限 —— 这一次执行照常拿到完整输出与退出码。
func TestExecSurvivesProxyBufferedUpstream(t *testing.T) {
	old := execPickupTimeout
	execPickupTimeout = 300 * time.Millisecond
	defer func() { execPickupTimeout = old }()

	srv, _ := newTestServer(t)
	const hostID, fp = "h-buffered", "fp-buffered"
	srv.store.RegisterHost(hostID, "web-01", fp)
	ts := httptest.NewServer(srv.httpHandler())
	defer ts.Close()

	agent := &bufferedAgent{
		base: ts.URL, hostID: hostID, fp: fp,
		txDelay: 1200 * time.Millisecond, // 远超 pickup 窗口
		output:  "agent_update: restart scheduled\n[AIOPS_EXIT]0\n",
		stop:    make(chan struct{}), txSent: make(chan struct{}),
	}
	agent.run(t)
	defer close(agent.stop)

	h, _ := srv.store.GetHost(hostID)
	out, kind, err := srv.execCommandOnHost(h, "echo hi", 30)
	if err != nil {
		t.Fatalf("反代缓冲上行流时执行不该失败：kind=%v err=%v out=%q", kind, err, out)
	}
	if kind != execOK {
		t.Fatalf("kind = %v, want execOK（输出与退出码都完整到达了）", kind)
	}
	if !strings.Contains(out, "restart scheduled") {
		t.Fatalf("输出没拿全：%q", out)
	}

	// 而且必须留下"是反代的问题"的判定，否则运维还是不知道该改哪里。
	verdicts := srv.edgeDiag.snapshot()
	if len(verdicts) != 1 || verdicts[0].Kind != "upstream_buffered" || verdicts[0].HostID != hostID {
		t.Fatalf("没有记下反代缓冲的判定：%+v", verdicts)
	}
	if !strings.Contains(verdicts[0].Detail, "proxy_request_buffering off") {
		t.Fatalf("判定里必须给出要加的那几行 nginx 配置：%q", verdicts[0].Detail)
	}

	// 还要走进平台自身故障的归口（self_fault.go）：聚合、活动记录，连续几次后升成事件。
	// 不带 hostID —— 故障在边缘那一跳，不该被拆成每台主机一条。
	faults := srv.faults.snapshot(10)
	if len(faults) != 1 || faults[0].Component != "edge_proxy" || faults[0].Kind != "upstream_buffered" {
		t.Fatalf("没有归口到平台自身故障：%+v", faults)
	}
	if faults[0].HostID != "" {
		t.Fatalf("边缘故障不该绑主机：%+v", faults[0])
	}
	if !strings.Contains(faults[0].Evidence, "deploy/nginx-aiops.conf") {
		t.Fatalf("证据里要给出完整示例配置的位置：%q", faults[0].Evidence)
	}

	// 故障本身不带主机，所以排障接口要能回答另一半问题：到底哪些机器被拖住了。
	rec := httptest.NewRecorder()
	srv.handlePlatformFaults(rec, httptest.NewRequest(http.MethodGet, "/api/v1/platform/faults", nil))
	var body struct {
		EdgeProxy []edgeProxyVerdict `json:"edge_proxy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("平台故障接口返回的不是 JSON：%v / %s", err, rec.Body.String())
	}
	if len(body.EdgeProxy) != 1 || body.EdgeProxy[0].HostID != hostID {
		t.Fatalf("平台故障接口没给出被拖住的主机：%s", rec.Body.String())
	}
}

// 反过来同样重要：真的没有 Agent 接单时，不能被上面的宽容路径拖成长时间挂起，
// 也不能误报成反代问题——那会把"主机离线/指纹不符"这类真故障指到 nginx 上。
func TestExecNoPickupStillFailsFast(t *testing.T) {
	oldPickup := execPickupTimeout
	execPickupTimeout = 200 * time.Millisecond
	defer func() { execPickupTimeout = oldPickup }()

	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h-dead", "dead-01", "fp-dead")
	h, _ := srv.store.GetHost("h-dead")

	start := time.Now()
	_, kind, err := srv.execCommandOnHost(h, "echo hi", 30)
	if err == nil || kind != execNoPickup {
		t.Fatalf("没有 Agent 接单时应判 no-pickup，实际 kind=%v err=%v", kind, err)
	}
	if !strings.Contains(err.Error(), Tz("playbook.no_pickup")) {
		t.Fatalf("错误信息应是「未接单」，不是反代那一条：%v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("等了 %v，没有及时收敛", el)
	}
	if v := srv.edgeDiag.snapshot(); len(v) != 0 {
		t.Fatalf("不该误判成反代问题：%+v", v)
	}
}

// 交互式终端救不回来（它本质上需要实时双向流），但至少要说对原因：
// rx 挂着、tx 不来 = 反代在缓冲，而不是「Agent 未接入」。
func TestTerminalTimeoutBlamesProxyWhenAgentAttached(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h-tty", "tty-01", "fp-tty")
	sess := srv.term.createFull("h-tty", "tty-01", "admin", "", "")
	if sess.agentAttached() {
		t.Fatal("还没挂 rx，不该判为已接单")
	}
	sess.markAgentRx(1)
	if !sess.agentAttached() {
		t.Fatal("rx 挂着就是已接单")
	}
	detail := srv.noteEdgeUpstreamBuffered("h-tty", "tty-01")
	if !strings.Contains(detail, "proxy_request_buffering off") {
		t.Fatalf("提示里要带上要加的配置：%q", detail)
	}
	sess.markAgentRx(-1)
	if sess.agentAttached() {
		t.Fatal("rx 断开且没有心跳，就不该再算已接单")
	}
	// alive 心跳同样算数，且会随时间过期。
	sess.markAgentAlive()
	if !sess.agentAttached() {
		t.Fatal("刚收到心跳应算已接单")
	}
	sess.attachMu.Lock()
	sess.lastAliveAt = time.Now().Add(-agentAttachStale - time.Second)
	sess.attachMu.Unlock()
	if sess.agentAttached() {
		t.Fatal("心跳过期后不该再算已接单")
	}
}
