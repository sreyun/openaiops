package main

import (
	"fmt"
	"regexp"
	"strings"
)

var reAssistCodeFence = regexp.MustCompile("(?is)```\\s*(promql|logql|sql|pgsql|postgresql|mysql)?\\s*\\n([\\s\\S]*?)```")

func extractAssistCode(answer string) (lang, code string) {
	m := reAssistCodeFence.FindStringSubmatch(answer)
	if len(m) >= 3 {
		return strings.ToLower(strings.TrimSpace(m[1])), strings.TrimSpace(m[2])
	}
	return "", ""
}

func parseDatasourceHint(contextText, explicit string) string {
	if id := strings.TrimSpace(explicit); id != "" {
		return id
	}
	ctx := contextText
	// Common UI context lines: "数据源：name（类型 postgres，地址 …）" or "datasource=xxx"
	for _, line := range strings.Split(ctx, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "datasource=") || strings.HasPrefix(low, "datasource:") {
			return strings.TrimSpace(line[len("datasource")+1:])
		}
		if strings.Contains(line, "数据源 id=") || strings.Contains(low, "datasource id=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

type assistVerifyResult struct {
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Task    string `json:"task,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Query   string `json:"query,omitempty"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) verifyAssistQuery(task, answer, contextText, datasourceID string) assistVerifyResult {
	task = strings.ToLower(strings.TrimSpace(task))
	switch task {
	case "promql", "logql", "pgsql", "sqlql":
	default:
		return assistVerifyResult{Skipped: true, Task: task, Summary: "该任务不支持自动验证"}
	}
	lang, code := extractAssistCode(answer)
	if code == "" {
		return assistVerifyResult{OK: false, Task: task, Error: "未找到可验证的代码块"}
	}
	dsID := parseDatasourceHint(contextText, datasourceID)
	if dsID == "" {
		// Fall back to first enabled matching datasource.
		for _, d := range s.cfg.ListDataSources() {
			if !d.Enabled {
				continue
			}
			if task == "promql" && (d.Type == "prometheus" || d.Type == "vm") {
				dsID = d.ID
				break
			}
			if task == "logql" && d.Type == "loki" {
				dsID = d.ID
				break
			}
			if (task == "pgsql" || task == "sqlql") && isSQLDataSourceType(d.Type) {
				dsID = d.ID
				break
			}
		}
	}
	if dsID == "" {
		return assistVerifyResult{Skipped: true, Task: task, Lang: lang, Query: truncateRun(code, 200), Summary: "未配置可用数据源，已跳过验证"}
	}
	ds, ok := s.cfg.GetDataSource(dsID)
	if !ok || !ds.Enabled {
		return assistVerifyResult{OK: false, Task: task, Lang: lang, Query: truncateRun(code, 200), Error: "数据源不存在或未启用"}
	}
	switch task {
	case "promql":
		if ds.Type != "prometheus" && ds.Type != "vm" {
			return assistVerifyResult{OK: false, Task: task, Error: "数据源类型不是 Prometheus/VM"}
		}
		out, err := queryPrometheus(ds, code)
		if err != nil {
			return assistVerifyResult{OK: false, Task: task, Lang: lang, Query: truncateRun(code, 200), Error: err.Error()}
		}
		return assistVerifyResult{OK: true, Task: task, Lang: firstNonEmptyOrDash(lang, "promql"), Query: truncateRun(code, 200), Summary: truncateRun(out, 240)}
	case "logql":
		if ds.Type != "loki" {
			return assistVerifyResult{OK: false, Task: task, Error: "数据源类型不是 Loki"}
		}
		out, err := queryLoki(ds, code, 5, 15)
		if err != nil {
			return assistVerifyResult{OK: false, Task: task, Lang: lang, Query: truncateRun(code, 200), Error: err.Error()}
		}
		return assistVerifyResult{OK: true, Task: task, Lang: firstNonEmptyOrDash(lang, "logql"), Query: truncateRun(code, 200), Summary: truncateRun(out, 240)}
	case "pgsql", "sqlql":
		if !isSQLDataSourceType(ds.Type) {
			return assistVerifyResult{OK: false, Task: task, Error: "数据源类型不是 SQL"}
		}
		c, err := s.resolveSQLConnFromDataSource(ds)
		if err != nil {
			return assistVerifyResult{OK: false, Task: task, Lang: lang, Query: truncateRun(code, 200), Error: err.Error()}
		}
		var cols []string
		var rows []map[string]any
		if driverOf(c) == "postgres" {
			cols, rows, err = pgQueryReadOnly(c, code, 5)
		} else {
			cols, rows, err = mysqlQueryReadOnly(c, code, 5)
		}
		if err != nil {
			return assistVerifyResult{OK: false, Task: task, Lang: firstNonEmptyOrDash(lang, "sql"), Query: truncateRun(code, 200), Error: err.Error()}
		}
		return assistVerifyResult{
			OK: true, Task: task, Lang: firstNonEmptyOrDash(lang, "sql"), Query: truncateRun(code, 200),
			Summary: fmt.Sprintf("验证通过：%d 列 · %d 行", len(cols), len(rows)),
		}
	default:
		return assistVerifyResult{Skipped: true, Task: task}
	}
}
