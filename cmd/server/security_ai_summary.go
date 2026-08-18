package main

// 扫描完成后自动生成的 AI 结论。
//
// 这两条路径与控制台上「AI 分析」按钮跑的是**同一个 assist 任务**
// （host_security_diagnosis / web_vuln_diagnosis），但此前各走各的：按钮走
// buildAssistPrompt 那条共享流水线，自动路径直接 buildAssistSystemPrompt + aiComplete。
// 同一个任务因此有两份提示词，而拿到不利那一份的偏偏是自动路径：
//
//   - **没有安全边界条款**。喂进去的全是扫描器抓回来的外部字符串——主机名、软件包名、
//     CVE 标题、Web 漏洞名与命中的 URL/响应片段——一台被拿下的主机或一个恶意站点
//     把指令写进这些字段，就是一次直达模型的提示注入，而这条路径连「以下为不可信材料」
//     都没说过。上下文也没走 sanitizeAssistContext，注入特征不过滤、长度不封顶。
//   - **吃不到热加载模板**。运维在 AI 设置里调的模板对自动结论毫无效果。
//   - **不进成本统计**。定时扫描每完成一次就是一次模型调用，recordAICallActor 全程
//     没被调用过，这部分开销在 AI 用量里根本不存在。
//   - **不走模型路由**。applyRoutedModel 的任务分级与成本护栏被绕开。
//
// 现在两条都改走 runAssistTaskSyncAs：扫描摘要作为**上下文**传入（由共享流水线套上
// 不可信围栏并过滤），actor 记为 aiActorAutoScan，好在统计里与人工调用分开。
//
// 非阻塞的语义不变：整个调用（含检索）都在 goroutine 里，失败只记日志，绝不改扫描状态。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// aiActorAutoScan 标记「扫描完成后自动跑的 AI 结论」，与人工点按钮的调用在成本统计里分开。
const aiActorAutoScan = "auto-scan"

// maybeHostSecurityAISummary optionally fills AISummary after a completed scan.
// Non-blocking: failures are logged and never change scan status.
func (s *Server) maybeHostSecurityAISummary(scan *HostScanResult) {
	if s == nil || scan == nil || scan.Status != "completed" {
		return
	}
	if !s.cfg.HostSecurity().AutoAISummary {
		return
	}
	ai := s.cfg.AIConfig()
	if !ai.Enabled || strings.TrimSpace(ai.Endpoint) == "" || strings.TrimSpace(ai.Model) == "" {
		return
	}
	scanID := scan.ID
	digest := buildHostScanAIContext(scan)
	go func() {
		text, err := s.runAssistTaskSyncAs(context.Background(), "host_security_diagnosis", aiActorAutoScan, "", digest)
		if err != nil {
			slog.Info("host security AI summary skipped", "scan_id", scanID, "err", err.Error())
			return
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if len(text) > 8000 {
			text = text[:8000]
		}
		now := time.Now().Unix()
		s.hostSec.mu.Lock()
		defer s.hostSec.mu.Unlock()
		for _, sc := range s.hostSec.scans {
			if sc != nil && sc.ID == scanID {
				sc.AISummary = text
				sc.AISummaryAt = now
				s.hostSec.rememberLastLocked(sc)
				s.hostSec.saveLocked()
				return
			}
		}
	}()
}

func (s *Server) maybeWebSecurityAISummary(scan *WebScanResult) {
	if s == nil || scan == nil || scan.Status != "completed" {
		return
	}
	if !s.cfg.WebSecurity().AutoAISummary {
		return
	}
	ai := s.cfg.AIConfig()
	if !ai.Enabled || strings.TrimSpace(ai.Endpoint) == "" || strings.TrimSpace(ai.Model) == "" {
		return
	}
	scanID := scan.ID
	digest := buildWebScanAIContext(scan)
	go func() {
		text, err := s.runAssistTaskSyncAs(context.Background(), "web_vuln_diagnosis", aiActorAutoScan, "", digest)
		if err != nil {
			slog.Info("web security AI summary skipped", "scan_id", scanID, "err", err.Error())
			return
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if len(text) > 8000 {
			text = text[:8000]
		}
		now := time.Now().Unix()
		s.webSec.mu.Lock()
		defer s.webSec.mu.Unlock()
		for _, sc := range s.webSec.scans {
			if sc != nil && sc.ID == scanID {
				sc.AISummary = text
				sc.AISummaryAt = now
				s.webSec.saveLocked()
				return
			}
		}
	}()
}

func buildHostScanAIContext(scan *HostScanResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "主机：%s (%s)\n风险：%s 评分：%d\n防火墙：%s CVE：%d 开放端口：%d\n",
		firstNonEmpty(scan.Hostname, scan.HostID), scan.HostID, scan.Risk, scan.Score,
		scan.Firewall, scan.CVECount, scan.PortCount)
	if hint := formatBaselineDiffHint(scan.BaselineDiff); hint != "" {
		fmt.Fprintf(&b, "基线对比：%s\n", hint)
	}
	fmt.Fprintf(&b, "摘要计数：%v\n", scan.Summary)
	n := 0
	for _, f := range scan.Findings {
		if f.Level != "critical" && f.Level != "high" && f.Level != "medium" {
			continue
		}
		fmt.Fprintf(&b, "- [%s/%s] %s %s\n", f.Level, f.Category, f.Title, firstNonEmpty(f.CVE, f.Package))
		n++
		if n >= 25 {
			break
		}
	}
	return b.String()
}

func buildWebScanAIContext(scan *WebScanResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "目标：%s %s\n摘要：%v\n", firstNonEmpty(scan.TargetName, scan.TargetID), scan.BaseURL, scan.Summary)
	if hint := formatBaselineDiffHint(scan.BaselineDiff); hint != "" {
		fmt.Fprintf(&b, "基线对比：%s\n", hint)
	}
	n := 0
	for _, f := range scan.Findings {
		sev := strings.ToLower(f.Severity)
		if sev != "critical" && sev != "high" && sev != "medium" {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s (%s) %s\n", f.Severity, firstNonEmpty(f.Name, f.TemplateID), f.TemplateID, firstNonEmpty(f.URL, f.MatchedAt))
		n++
		if n >= 25 {
			break
		}
	}
	return b.String()
}
