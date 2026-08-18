package main

import (
	"crypto/subtle"
	"encoding/json"
	"strings"
)

// MCPScopedToken is a Wave-2 scoped bearer for MCP (subset of readonly tools).
type MCPScopedToken struct {
	Name   string   `json:"name"`
	Token  string   `json:"token"`
	Scopes []string `json:"scopes"`
}

var mcpScopeTools = map[string][]string{
	"metrics":   {"query_metrics", "list_alerts", "list_recent_changes", "check_host_health", "list_hosts", "render_chart", "query_metric_range", "query_promql_range", "show_instant_stat", "analyze_metric_trend", "forecast_metric", "list_dashboards", "get_dashboard", "list_dashboard_panels", "query_dashboard_panel", "analyze_dashboard"},
	"logs":      {"search_logs"},
	"sql":       {"list_datasources", "query_datasource"},
	"hardware":  {"query_hardware", "query_hardware_events", "query_hardware_history", "query_hardware_changes", "query_snmp", "query_interface_traffic", "query_traps"},
	"infra":     {"list_hosts", "query_containers", "query_k8s", "locate_resource", "query_hyperv", "query_netflow", "query_netflow_flows"},
	"knowledge": {"search_similar_cases", "search_knowledge", "list_ui_views", "navigate_ui", "query_security_posture"},
	"alerts":    {"list_alerts"},
	"sre":       {"get_duty_context", "diagnose_incident", "run_diagnostic", "check_host_health", "list_hosts", "list_alerts", "list_recent_changes", "query_platform_faults"},
	"ai":        {"run_assist_task", "analyze_dashboard", "diagnose_incident", "get_duty_context", "list_hosts"},
}

func parseMCPScopedTokens(raw string) []MCPScopedToken {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var list []MCPScopedToken
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil
	}
	out := list[:0]
	for _, t := range list {
		if strings.TrimSpace(t.Token) == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func mcpScopesAllowAll(scopes []string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, s := range scopes {
		if strings.EqualFold(strings.TrimSpace(s), "all") {
			return true
		}
	}
	return false
}

func mcpToolAllowedByScopes(tool string, scopes []string) bool {
	if !mcpReadonlyTools[tool] {
		return false
	}
	if mcpScopesAllowAll(scopes) {
		return true
	}
	allowed := map[string]bool{}
	for _, sc := range scopes {
		sc = strings.ToLower(strings.TrimSpace(sc))
		for _, name := range mcpScopeTools[sc] {
			allowed[name] = true
		}
	}
	return allowed[tool]
}

// resolveMCPAuth returns (ok, scopes, tokenName). Primary MCPToken has full scopes.
func resolveMCPAuth(cfg AIConfig, bearer string) (bool, []string, string) {
	bearer = strings.TrimSpace(bearer)
	if bearer == "" {
		return false, nil, ""
	}
	if tok := strings.TrimSpace(cfg.MCPToken); tok != "" {
		if subtle.ConstantTimeCompare([]byte(bearer), []byte(tok)) == 1 {
			return true, []string{"all"}, "primary"
		}
	}
	for _, st := range parseMCPScopedTokens(cfg.MCPScopedTokensJSON) {
		if subtle.ConstantTimeCompare([]byte(bearer), []byte(st.Token)) == 1 {
			scopes := st.Scopes
			if len(scopes) == 0 {
				scopes = []string{"all"}
			}
			name := st.Name
			if name == "" {
				name = "scoped"
			}
			return true, scopes, name
		}
	}
	return false, nil, ""
}
