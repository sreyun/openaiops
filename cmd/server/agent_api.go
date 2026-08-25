package main

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// decompressBody transparently handles gzip Content-Encoding on request bodies.
// Go's http.Server does NOT auto-decompress request bodies (unlike responses).
// Since agent v5.1.0, the report payload may be gzip-compressed to save bandwidth.
// Returns the original r.Body when no compression is used (backward-compatible).
//
// **返回的 reader 永远是有上限的**，这一条是安全边界不是优化：
// 全局 bodyLimit 中间件管的是**压缩后**的字节数，而同构的指标 JSON 用 gzip 能压到
// 1/50 以上——几 MB 的请求体足以解出几百 MB，而这些 Agent 端点全在 isPublicPath 里，
// 指纹校验发生在**解码之后**。也就是说未鉴权的一方就能让服务端分配几百 MB 内存。
// 补传端点当初单独补过这个限制，注册与上报两条却漏了；把上限收进这里，
// 以后新增的 Agent 端点不会再有机会忘记。
func decompressBody(r *http.Request) (io.ReadCloser, error) {
	if r.Header.Get("Content-Encoding") != "gzip" {
		return limitedReadCloser{r: io.LimitReader(r.Body, agentIngestMaxBodyBytes), c: r.Body}, nil
	}
	gr, err := gzip.NewReader(r.Body)
	if err != nil {
		return nil, err
	}
	return limitedReadCloser{r: io.LimitReader(gr, agentIngestMaxBodyBytes), c: gr}, nil
}

// limitedReadCloser 把 io.LimitReader 与原始 Closer 绑在一起：调用方的
// `defer body.Close()` 必须仍然关到真正的 gzip reader 上。
type limitedReadCloser struct {
	r io.Reader
	c io.Closer
}

func (l limitedReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l limitedReadCloser) Close() error               { return l.c.Close() }

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	body, err := decompressBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	defer body.Close()

	var req struct {
		HostID      string `json:"host_id"`
		Hostname    string `json:"hostname"`
		Token       string `json:"token"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	// Admission: a valid install token is required to register a NEW agent.
	// Once registered, the agent authenticates subsequent reports by fingerprint,
	// so rotating this token never disturbs already-installed agents.
	//
	// v5.2.6: Allow re-registration WITHOUT install token when the host is
	// already known (matching fingerprint in store). This is critical for
	// server restart recovery: if the DB was lost or the agent's config has
	// no token, the agent can still re-join by proving its machine fingerprint.
	// New agents (unknown host_id + unknown fingerprint) still require a token.
	if s.cfg.AgentTokenRequired() && !s.cfg.ValidInstallToken(req.Token) {
		// 认**指纹**而不是 host_id：重装后 host_id 是全新的随机值，按 id 查必然落空，
		// 于是一台早已登记在册的机器会被当成陌生 Agent 拒之门外——这与上面注释里
		// "凭机器指纹即可重新加入"的意图相悖。指纹本就是后续所有上报的认证凭据，
		// 用它准入不会放宽任何信任边界。
		known := false
		if req.Fingerprint != "" {
			if h, ok := s.store.GetHost(req.HostID); ok && h.Fingerprint == req.Fingerprint {
				known = true
			} else if _, ok := s.store.CanonicalHostID(req.HostID, req.Fingerprint); ok {
				known = true // 同一台机器的既有记录（重装换了 id）
			}
		}
		if !known {
			// Unknown host or fingerprint doesn't match → require install token
			writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "agent.invalid_token")})
			return
		}
		slog.Info("允许已知机器免Token重新注册（凭机器指纹）", "host_id", shortID(req.HostID))
	}
	if req.Fingerprint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "agent.fingerprint_required")})
		return
	}
	// 规范身份对齐：重装后 Agent 会带着**新的随机 host_id** 来注册。直接收下就多出
	// 一条记录，而平台里所有数据都按 host_id 存（VM 指标 host 标签、日志、告警、
	// 硬件快照/变更、Flow 明细…），这台机器的历史会被劈成两半。
	// 按机器指纹认回它原来的 id 下发给 Agent，历史即自然接续，也不再产生重复。
	hostID := req.HostID
	if canonical, ok := s.store.CanonicalHostID(req.HostID, req.Fingerprint); ok {
		slog.Info("按机器指纹认回既有身份（Agent 重装/换 ID）",
			"claimed", shortID(req.HostID), "canonical", shortID(canonical), "hostname", req.Hostname)
		hostID = canonical
	}
	// Count install-token uses only for truly new hosts (not fingerprint rejoin).
	isNew := false
	allowFPRebind := false
	if existing, existed := s.store.GetHost(hostID); !existed {
		isNew = true
	} else if existing.Fingerprint != "" && existing.Fingerprint != req.Fingerprint {
		// Fingerprint formula upgrades (e.g. drop flapping NIC MAC on Win11) keep
		// the same host_id but present a new fp. A valid install token authorizes
		// rebinding — without this, register returns 409 and every subsequent
		// report/terminal/desktop channel 403s ("remote maintenance dead").
		// Refuse silent rebind without a token (host_id takeover protection).
		if req.Token != "" && s.cfg.ValidInstallToken(req.Token) {
			allowFPRebind = true
			slog.Warn("安装令牌重新绑定主机指纹",
				"host_id", shortID(hostID), "hostname", req.Hostname)
		} else {
			writeJSON(w, http.StatusConflict, map[string]string{"error": Tr(r, "agent.fingerprint_conflict")})
			return
		}
	}
	if allowFPRebind {
		s.store.RegisterHostRebindFP(hostID, req.Hostname, req.Fingerprint)
	} else {
		s.store.RegisterHost(hostID, req.Hostname, req.Fingerprint)
	}
	if isNew && req.Token != "" {
		s.cfg.ConsumeInstallTokenUse(req.Token)
	}
	resp := map[string]any{
		"status": "ok",
		// Agent 会改用这个 id：与请求里的不同即表示"你其实是这台老主机"。
		"host_id":          hostID,
		"server_time_unix": time.Now().Unix(),
	}
	// 日志加密：把按「主密钥 + 指纹」派生的日志密钥一次性下发给 agent（未配置主密钥则不下发，日志走明文）
	if lk := deriveLogKey(req.Fingerprint); lk != nil {
		resp["log_key"] = base64.StdEncoding.EncodeToString(lk)
		resp["log_encrypt"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleReport ingests a metrics report (base + custom + events) from an agent.
// Authentication is by machine fingerprint (bound at registration), NOT by the
// install token — so rotating the token never breaks already-installed agents.
// Verification + upsert happen atomically inside Store.UpsertAuthenticated to
// avoid a TOCTOU window and double-lock overhead on the hot report path.
//
// Since v5.1.0, agents may gzip-compress the JSON body (Content-Encoding: gzip)
// to reduce bandwidth. This handler transparently decompresses when needed.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	body, err := decompressBody(r)
	if err != nil {
		// Gzip decompression failure — likely caused by proxy corruption
		// on external networks. Log the error so operators can diagnose.
		slog.Warn("Agent 上报 gzip 解压失败（可能外网代理损坏）",
			"remote", r.RemoteAddr, "content_encoding", r.Header.Get("Content-Encoding"),
			"content_length", r.ContentLength, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	defer body.Close()

	var rep shared.Report
	if err := json.NewDecoder(body).Decode(&rep); err != nil {
		slog.Warn("Agent 上报 JSON 解析失败",
			"remote", r.RemoteAddr, "content_encoding", r.Header.Get("Content-Encoding"),
			"err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.host_required")})
		return
	}
	h, ok := s.store.UpsertAuthenticated(rep, rep.Fingerprint)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "agent.fingerprint_failed")})
		return
	}
	// Install-time grouping: folder_id (any depth) preferred; else legacy L1 category.
	if err := s.cfg.applyAgentFolderHint(h.ID, rep.FolderID, rep.Category); err != nil {
		slog.Debug("agent folder hint skipped", "host", shortID(h.ID), "err", err)
	}
	// Optional fleet auto-update (default off). Runs async; never blocks report ACK.
	go s.maybeAutoUpdateHost(h.ID)
	// Mirror the sample to VictoriaMetrics when enabled (non-blocking, best-effort).
	s.vm.enqueue(rep.HostID, rep.Hostname, s.effectiveCategory(rep.HostID), time.Now().Unix(), rep.Metrics)
	// Slow degradation detection: check if resources are trending upward near thresholds.
	go s.checkSlowDegradation(rep.HostID)
	// 响应体额外下发「分布式探测任务」：agent 作为多地探针执行并回报（迭代 D，additive/向后兼容）
	writeJSON(w, http.StatusOK, shared.ReportResponse{Status: "ok", HostID: h.ID, ProbeTasks: s.distProbeTasks()})
}

// handleAgentBackfill 接收 Agent 在失联期间攒下的采样（POST /api/v1/agent/backfill）。
//
// 与实时上报分成两个端点是刻意的：补传包比实时包大一到两个数量级，混在一起会让链路差
// 的机器连实时数据都送不出去。这里也**只写时序库**，绝不碰 Store —— 详见
// ingestAgentBackfill 的说明。
func (s *Server) handleAgentBackfill(w http.ResponseWriter, r *http.Request) {
	// 补传包默认是 gzip 的（体积比实时包大一到两个数量级，压缩收益明显），
	// 而 Go 的 http.Server 不会自动解压请求体——必须显式走 decompressBody。
	body, err := decompressBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid gzip body"})
		return
	}
	defer body.Close()
	// 解压后再限一次长度。全局 bodyLimit（100 MiB）管的是**压缩后**的字节数，而 gzip
	// 对同构指标 JSON 能压到 1/50 以上——一个几 MB 的请求体足以解出几百 MB，而这一切
	// 发生在下面的指纹校验之前。Agent 侧一批最多 60 条（约 200 KB 未压缩），8 MiB 给了
	// 40 倍余量，仍能把放大攻击挡在内存分配之前。
	var rep shared.BackfillReport
	if err := json.NewDecoder(io.LimitReader(body, agentBackfillMaxBodyBytes)).Decode(&rep); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(rep.HostID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}
	fp := r.Header.Get("X-Agent-Fingerprint")
	if fp == "" {
		fp = r.URL.Query().Get("fp")
	}
	if !s.forwardFingerprintOKByHost(rep.HostID, fp) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "fingerprint mismatch"})
		return
	}
	accepted, dropped := s.ingestAgentBackfill(rep)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "accepted": accepted, "dropped": dropped})
}

const (
	// agentBackfillMaxPerReport 是单次补传允许接收的条数上限。Agent 侧本来就分批
	// （agentBackfillPerBatch=60），这里是对「异常/被篡改的 Agent 一次灌几万条」的防线。
	agentBackfillMaxPerReport = 240
	// agentBackfillMaxBodyBytes 是**解压后**允许读取的最大字节数，见 handleAgentBackfill。
	agentBackfillMaxBodyBytes = 8 << 20
	// agentIngestMaxBodyBytes 是 decompressBody 对**解压后**字节数的统一上限。
	// 单次实时上报实测 3~8 KB，补传一批（60 条）约 200 KB —— 8 MiB 留了 40 倍余量，
	// 同时把 gzip 放大挡在内存分配之前。
	agentIngestMaxBodyBytes = 8 << 20
	// agentBackfillMaxAgeSec 是补传样本的最大回溯窗口，与 Agent 侧的缓冲上限
	// （agentBackfillMaxAge = 7 天）对齐。两边必须一致：服务端收窄会让 Agent 辛苦攒下
	// 的老数据在入口被静默丢掉，放宽则等于允许一台时钟错乱的主机往任意历史位置写点。
	agentBackfillMaxAgeSec = 7 * 24 * 3600
	// agentBackfillMaxSkewSec 是能接受的时钟偏移。超出即视为时钟不可信，按零偏移处理
	// （宁可让这批点落在原始时间戳上，也不要按一个荒谬的偏移把它们搬到别处）。
	agentBackfillMaxSkewSec = 24 * 3600
)

// ingestAgentBackfill 把 Agent 在失联期间攒下的采样写入 VictoriaMetrics。
//
// 三条约束，缺一不可：
//
//  1. **只写时序库，不进内存环**。内存环（Store）表达的是"最近实时状态"，其
//     LastSeen、多级降采样、告警评估全部假设样本是按时间顺序刚刚到达的。把一批
//     五分钟前的历史点塞进去，会立刻污染 1m/5m 分层并可能触发过期数据的告警。
//     历史的唯一真源是 VM，补传也只补 VM。
//
//  2. **按时钟偏移换算时间戳**。实时样本是服务端收到时用自己的时钟打戳的，而补传
//     样本带的是 Agent 本地时钟。两者不校准的话，一台快 3 分钟的主机补上来的点会和
//     它自己的实时点错开 3 分钟——曲线上就是一段重叠又断裂的"数据错乱"。用
//     ReportedAt（Agent 构造本次上报时的本地时刻）与服务端 now 的差值做平移，正好
//     把补传点放回它在服务端时间轴上应有的位置。
//
//  3. **对时间戳做硬校验**。未来时间、超过一天的陈旧点、荒谬的偏移一律丢弃：VM 的
//     保留期是 100 年，一条写歪的点会永久留在那里，且没有任何界面能发现它。
func (s *Server) ingestAgentBackfill(rep shared.BackfillReport) (accepted, dropped int) {
	if s == nil || s.vm == nil || len(rep.Samples) == 0 || !s.vm.enabled() {
		return 0, 0
	}
	now := time.Now().Unix()
	skew := backfillClockSkew(rep.ReportedAt, now)
	cat := s.effectiveCategory(rep.HostID)
	hostname := rep.Hostname
	if h := s.hostByID(rep.HostID); h != nil && h.Hostname != "" {
		hostname = h.Hostname
	}
	for i, b := range rep.Samples {
		if accepted >= agentBackfillMaxPerReport {
			dropped += len(rep.Samples) - i
			break
		}
		ts, ok := backfillTimestamp(b.Ts, skew, now)
		if !ok {
			dropped++
			continue
		}
		s.vm.enqueue(rep.HostID, hostname, cat, ts, b.Metrics)
		accepted++
	}
	if accepted > 0 || dropped > 0 {
		slog.Info("已接收 Agent 补传采样", "host", shortID(rep.HostID),
			"accepted", accepted, "dropped", dropped, "skew_sec", skew)
	}
	return accepted, dropped
}

// backfillClockSkew 返回把 Agent 本地时间换算到服务端时间轴的偏移量。
// 偏移荒谬（超过一天）时返回 0：宁可让这批点落在它们自称的时间上，也不要按一个
// 明显错误的偏移把它们整体搬到别处——后者会在曲线上造出一段凭空出现的历史。
func backfillClockSkew(reportedAt, now int64) int64 {
	if reportedAt <= 0 {
		return 0
	}
	d := now - reportedAt
	if d <= -agentBackfillMaxSkewSec || d >= agentBackfillMaxSkewSec {
		return 0
	}
	return d
}

// backfillTimestamp 把一条补传样本的时间戳换算到服务端时间轴并做硬校验。
// VM 的保留期是 100 年，一条写歪的点会永久留在那里且没有任何界面能发现它，
// 所以未来时间、超过保留窗口的陈旧点一律拒绝。
func backfillTimestamp(ts, skew, now int64) (int64, bool) {
	if ts <= 0 {
		return 0, false
	}
	out := ts + skew
	// 允许 60s 的向前容差（采集与送达之间的正常延迟 + 取整），再多就是时钟有问题。
	if out > now+60 || now-out > agentBackfillMaxAgeSec {
		return 0, false
	}
	return out, true
}
