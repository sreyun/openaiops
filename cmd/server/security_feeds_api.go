package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// FeedSourceView is one catalog row plus its live state, shaped for the UI so
// the frontend never has to join two lists by hand.
type FeedSourceView struct {
	FeedSource
	Enabled   bool   `json:"enabled"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	Ref       string `json:"state_ref,omitempty"`
	Files     int    `json:"files,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Error     string `json:"error,omitempty"`
	Installed bool   `json:"installed"`
}

type feedStatusResponse struct {
	Config  SecurityFeedConfig `json:"config"`
	Sources []FeedSourceView   `json:"sources"`
	Job     *FeedJob           `json:"job,omitempty"`
	Dir     string             `json:"dir"`
	// ProxyFromEnv reports whether the process itself has proxy env vars, so the
	// UI can explain why downloads work without an explicit proxy setting.
	ProxyFromEnv string `json:"proxy_from_env,omitempty"`
}

func (s *Server) feedStatus() feedStatusResponse {
	cfg := s.cfg.SecurityFeeds()
	out := feedStatusResponse{Config: cfg, ProxyFromEnv: envProxyHint()}
	if s.feeds == nil {
		return out
	}
	out.Dir = s.feeds.dir
	out.Job = s.feeds.currentJob()
	for _, src := range feedCatalog {
		st := s.feeds.stateOf(src.ID)
		v := FeedSourceView{
			FeedSource: src,
			Enabled:    cfg.sourceEnabled(src.ID),
			UpdatedAt:  st.UpdatedAt,
			Ref:        st.Ref,
			Files:      st.Files,
			Bytes:      st.Bytes,
			Error:      st.Error,
		}
		if fi, err := os.Stat(s.feeds.sourceDir(src.ID)); err == nil && fi.IsDir() {
			v.Installed = true
		}
		out.Sources = append(out.Sources, v)
	}
	return out
}

func envProxyHint() string {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return k + "=" + truncateRun(v, 80)
		}
	}
	return ""
}

func (s *Server) handleSecurityFeedStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.feedStatus())
}

func (s *Server) handleSetSecurityFeedConfig(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeSecErr(w, http.StatusBadRequest, "bad json")
		return
	}
	in := s.cfg.SecurityFeeds()
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &in); err != nil {
		writeSecErr(w, http.StatusBadRequest, "bad json")
		return
	}
	// An explicitly sent empty list means "disable everything"; an omitted field
	// must not be mistaken for that, or saving the proxy would kill all sources.
	if _, ok := raw["sources"]; ok {
		if in.Sources == nil {
			in.Sources = []string{}
		}
	}
	if err := s.cfg.SetSecurityFeeds(in); err != nil {
		writeSecErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "更新安全情报源配置",
	})
	writeJSON(w, http.StatusOK, s.feedStatus())
}

// handleSecurityFeedUpdate kicks off an async update and returns immediately.
// The previous synchronous refresh held the request open for the whole download
// and timed out at the proxy long before a large template tree finished.
func (s *Server) handleSecurityFeedUpdate(w http.ResponseWriter, r *http.Request) {
	if s.feeds == nil {
		writeSecErr(w, http.StatusServiceUnavailable, "情报源模块未就绪")
		return
	}
	var req struct {
		Sources []string `json:"sources,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	cfg := s.cfg.SecurityFeeds()
	sources := cfg.enabledSources()
	if len(req.Sources) > 0 {
		sources = nil
		for _, id := range req.Sources {
			if src, ok := feedSourceByID(id); ok {
				sources = append(sources, src)
			}
		}
	}
	job, err := s.feeds.runUpdate(cfg, sources, s.actorName(r))
	if err != nil {
		writeSecErr(w, http.StatusConflict, err.Error())
		return
	}
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "启动安全情报源更新（" + strconv.Itoa(len(sources)) + " 个源）",
	})
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleSecurityFeedCancel(w http.ResponseWriter, r *http.Request) {
	if s.feeds == nil || !s.feeds.cancelJob() {
		writeSecErr(w, http.StatusConflict, "没有正在运行的更新任务")
		return
	}
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "取消安全情报源更新",
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSecurityFeedTest probes reachability with a small request so an operator
// can validate proxy/mirror settings in seconds instead of starting a download
// and waiting for it to fail.
func (s *Server) handleSecurityFeedTest(w http.ResponseWriter, r *http.Request) {
	var in SecurityFeedConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		in = s.cfg.SecurityFeeds()
	}
	in = in.withDefaults()
	in.TimeoutSec = 25 // a connectivity probe must answer fast
	client, err := feedHTTPClient(in)
	if err != nil {
		writeSecErr(w, http.StatusBadRequest, err.Error())
		return
	}
	target := feedURL(in, "https://api.github.com/repos/projectdiscovery/nuclei-templates/releases/latest")
	started := time.Now()
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("User-Agent", "AIOps-Monitor-FeedUpdater/1.0")
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "target": target, "error": feedNetError(in, err),
			"elapsed_ms": time.Since(started).Milliseconds(),
		})
		return
	}
	defer resp.Body.Close()
	var body struct {
		TagName string `json:"tag_name"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         resp.StatusCode == http.StatusOK,
		"status":     resp.StatusCode,
		"target":     target,
		"latest_tag": body.TagName,
		"elapsed_ms": time.Since(started).Milliseconds(),
	})
}

// startSecurityFeedScheduler runs auto-updates on the configured cadence. It
// checks often but acts rarely, and never at boot: a restart loop must not turn
// into a download loop.
func (s *Server) startSecurityFeedScheduler() {
	// 情报源同步同理：一次坏数据不该让漏洞库从此不再更新。
	go superviseLoop("security-feed-scheduler", func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for range t.C {
			cfg := s.cfg.SecurityFeeds()
			if !cfg.AutoUpdate || s.feeds == nil || s.feeds.jobRunning() {
				continue
			}
			due := cfg.LastAutoRunSec + int64(cfg.IntervalHours)*3600
			if time.Now().Unix() < due {
				continue
			}
			s.cfg.markFeedAutoRun(time.Now().Unix())
			if _, err := s.feeds.runUpdate(cfg, cfg.enabledSources(), "scheduler"); err != nil {
				continue
			}
			s.store.AddLog(LogEntry{
				Kind: KindOperation, Level: "info", Actor: "system",
				Message: "按计划更新安全情报源",
			})
		}
	})
}

// feedSourcePath exposes an installed source directory to the consumers
// (built-in checks, knowledge lookups). Returns "" when the source is absent.
func (s *Server) feedSourcePath(id string) string {
	if s.feeds == nil {
		return ""
	}
	dir := s.feeds.sourceDir(id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// nucleiFeedTemplatesDir returns the template tree managed by the feed updater,
// or "" when it has not been populated yet.
func (s *Server) nucleiFeedTemplatesDir() string {
	dir := s.feedSourcePath("nuclei-templates")
	if dir == "" {
		return ""
	}
	if !nucleiTemplatesReady(dir) {
		return ""
	}
	return dir
}
