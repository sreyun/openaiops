package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// handleSreyunSuggestions 返回「快捷问题 / 推荐 Prompt」池，供 AI 对话空态展示、用户一键提问。
// 结合当前实际状态（活跃告警 / 纳管主机 / 近期错误日志）生成动态建议 + 覆盖 Sreyun 各项
// 能力（主机 / 指标 / 日志 / 告警 / 诊断 / 巡检）的精选示例。前端随机取若干条展示并可「换一批」。
func (s *Server) handleSreyunSuggestions(w http.ResponseWriter, r *http.Request) {
	var dyn []string // 依据当前状态的动态建议（前端优先展示）
	now := time.Now().Unix()

	// 纳管主机：总数 / 在线数 / 取一个样例主机名（按调用方授权裁剪）
	hosts := s.filterHostsForUser(r, s.store.ListHosts())
	online := 0
	var sampleHost string
	for _, h := range hosts {
		if now-h.LastSeen <= 60 { // 60s 内有上报视为在线
			online++
		}
		if sampleHost == "" && h.Hostname != "" {
			sampleHost = h.Hostname
		}
	}

	// 活跃告警（阈值告警 + 失败拨测）
	alertCount := 0
	if s.notifier != nil {
		alertCount += len(s.filterAlertsForUser(r, s.notifier.ActiveAlerts()))
	}
	if s.checks != nil {
		alertCount += len(s.filterAlertsForUser(r, s.checks.DownAlerts()))
	}
	if alertCount > 0 {
		dyn = append(dyn, fmt.Sprintf("分析当前 %d 条告警的根因，并给出处置建议", alertCount))
	}

	// 近 30 分钟错误日志
	if s.logs != nil {
		if errN := s.logs.errorCount(now - 1800); errN > 0 {
			dyn = append(dyn, fmt.Sprintf("诊断最近 30 分钟的 %d 条错误日志，定位可能的根因", errN))
		}
	}

	if sampleHost != "" {
		dyn = append(dyn, fmt.Sprintf("查询主机 %s 的 CPU / 内存 / 磁盘使用率", sampleHost))
	}
	if len(hosts) > 0 {
		dyn = append(dyn, fmt.Sprintf("当前共纳管 %d 台主机（在线 %d），帮我总结整体运行状况", len(hosts), online))
	}

	// 精选能力示例（覆盖主机排障 + 看板/安全/导出等统一入口能力）
	curated := []string{
		"当前有哪些主机在线？各自负载如何？",
		"最近 1 小时有哪些告警？按严重程度排序并解读",
		"查询 CPU / 内存 / 磁盘使用率最高的 3 台主机",
		"帮我做一张主机 CPU / 内存 / 磁盘总览看板",
		"列出当前所有看板，并挑一张做优化建议",
		"生成一份当前基础设施的健康巡检报告，并支持导出",
		"对最近打开的事件做 AI 根因诊断",
		"用安全诊断任务检查主机加固建议",
		"最近的错误日志里有没有值得关注的异常模式？",
		"帮我梳理当前值得优先处理的运维风险 Top 5",
		"对比各主机的负载，找出资源分配不均衡的问题",
		"如果要给这套系统做容量规划，应重点关注哪些指标？",
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"dynamic": dyn,
		"curated": curated,
	})
}

// handleSreyunStatus reports SRE/AI readiness for operators and health probes.
// GET /api/v1/hermes/status
func (s *Server) handleSreyunStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.AIConfig()
	aiReady := cfg.Enabled && strings.TrimSpace(cfg.Endpoint) != "" && strings.TrimSpace(cfg.Model) != ""
	toolCount := 0
	if s.sreyun != nil {
		toolCount = s.sreyun.toolCount()
	}
	hosts := s.filterHostsForUser(r, s.store.ListHosts())
	alertN := 0
	if s.notifier != nil {
		alertN = len(s.filterAlertsForUser(r, s.notifier.ActiveAlerts()))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            s.sreyun != nil && aiReady,
		"engine":        s.sreyun != nil,
		"ai_enabled":    cfg.Enabled,
		"ai_ready":      aiReady,
		"model":         cfg.Model,
		"tools":         toolCount,
		"hosts_visible": len(hosts),
		"active_alerts": alertN,
		"request_id":    requestIDFrom(r),
		"pg":            s.pg != nil,
		"strict_security": strings.EqualFold(os.Getenv("AIOPS_STRICT_SECURITY"), "1") ||
			strings.EqualFold(os.Getenv("AIOPS_STRICT_SECURITY"), "true"),
		"secret_key": secretEncryptionEnabled(),
	})
}
