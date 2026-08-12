package main

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"aiops-monitor/shared"
)

// decompressBody transparently handles gzip Content-Encoding on request bodies.
// Go's http.Server does NOT auto-decompress request bodies (unlike responses).
// Since agent v5.1.0, the report payload may be gzip-compressed to save bandwidth.
// Returns the original r.Body when no compression is used (backward-compatible).
func decompressBody(r *http.Request) (io.ReadCloser, error) {
	if r.Header.Get("Content-Encoding") != "gzip" {
		return r.Body, nil
	}
	gr, err := gzip.NewReader(r.Body)
	if err != nil {
		return nil, err
	}
	return gr, nil
}

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
