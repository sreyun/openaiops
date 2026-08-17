package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// OpsActionPlan is the AI-proposed {summary, actions[]} contract.
type OpsActionPlan struct {
	Summary string      `json:"summary,omitempty"`
	Actions []OpsAction `json:"actions"`
}

// OpsAction is one mutating suggestion after server-side whitelist validation.
type OpsAction struct {
	Type   string         `json:"type"`
	Risk   string         `json:"risk,omitempty"`
	Target map[string]any `json:"target,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	Verify string         `json:"verify,omitempty"`
}

var (
	opsDangerCmdRe = regexp.MustCompile(`(?i)(\brm\s+-rf\b|\bmkfs\b|\bdd\s+if=|:\(\)\s*\{\s*:\|:\s*&\s*\}\s*;|\bshutdown\b|\breboot\b|\bformat\b|\bdel\s+/[fq]\b|\bRemove-Item\s+.+-Recurse\b)`)
	opsAllowedUI   = map[string]bool{
		"open_dashboard": true, "navigate_view": true, "export_report": true,
		"drill_down": true, "show_chart": true, "show_stat": true, "show_table": true, "show_logs": true,
		// 闭环动作（ai_followup.go）：把 AI 结论转成运维动作。放行的只是「按钮」，
		// 真正的写入仍由 /api/v1/ai/followup 用服务端原文执行，模型伪造 run_id 无效。
		aiActCreateTicket: true, aiActAddIncidentNote: true,
		aiActLinkChange: true, aiActProposeRemediation: true,
	}
)

func opsAllowedTypes() map[string]bool {
	return map[string]bool{
		"hyperv_power": true, "hyperv_config": true,
		"container_action": true, "container_exec": true,
		"k8s_scale": true, "k8s_restart": true, "k8s_undo": true, "k8s_delete_pod": true, "k8s_exec": true,
		"host_playbook": true, "sql_apply": true, "sql_ddl": true,
	}
}

func parseOpsActionPlanJSON(raw string) (*OpsActionPlan, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty plan")
	}
	if strings.HasPrefix(raw, "```") {
		if i := strings.Index(raw, "\n"); i >= 0 {
			raw = raw[i+1:]
		}
		if j := strings.LastIndex(raw, "```"); j >= 0 {
			raw = raw[:j]
		}
		raw = strings.TrimSpace(raw)
	}
	if !strings.HasPrefix(raw, "{") {
		i := strings.Index(raw, "{")
		j := strings.LastIndex(raw, "}")
		if i >= 0 && j > i {
			raw = raw[i : j+1]
		}
	}
	var plan OpsActionPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if plan.Actions == nil {
		return nil, fmt.Errorf("missing actions array")
	}
	return &plan, nil
}

// ValidateOpsActionPlan whitelist-validates and normalizes a plan.
// Unknown types / dangerous playbook commands / missing targets are rejected.
// Risk is recomputed server-side (model-claimed risk is ignored for authorization).
func ValidateOpsActionPlan(plan *OpsActionPlan) (*OpsActionPlan, string, error) {
	if plan == nil {
		return nil, "", fmt.Errorf("nil plan")
	}
	if len(plan.Actions) == 0 {
		return &OpsActionPlan{Summary: plan.Summary, Actions: []OpsAction{}}, "low", nil
	}
	if len(plan.Actions) > 32 {
		return nil, "", fmt.Errorf("too many actions (max 32)")
	}
	allowed := opsAllowedTypes()
	out := &OpsActionPlan{Summary: strings.TrimSpace(plan.Summary), Actions: make([]OpsAction, 0, len(plan.Actions))}
	maxRisk := "low"
	for i, a := range plan.Actions {
		typ := strings.ToLower(strings.TrimSpace(a.Type))
		if !allowed[typ] {
			return nil, "", fmt.Errorf("action[%d]: unsupported type %q", i, a.Type)
		}
		na, risk, err := validateOneOpsAction(typ, a)
		if err != nil {
			return nil, "", fmt.Errorf("action[%d] (%s): %w", i, typ, err)
		}
		na.Type = typ
		na.Risk = risk
		out.Actions = append(out.Actions, na)
		maxRisk = opsMaxRisk(maxRisk, risk)
	}
	return out, maxRisk, nil
}

func opsMaxRisk(a, b string) string {
	rank := map[string]int{"low": 1, "medium": 2, "high": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func strMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%.0f", t))
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func validateOneOpsAction(typ string, a OpsAction) (OpsAction, string, error) {
	tgt := a.Target
	if tgt == nil {
		tgt = map[string]any{}
	}
	params := a.Params
	if params == nil {
		params = map[string]any{}
	}
	out := OpsAction{Target: map[string]any{}, Params: map[string]any{}, Verify: strings.TrimSpace(a.Verify)}
	copyStr := func(dst map[string]any, src map[string]any, keys ...string) {
		for _, k := range keys {
			if v := strMap(src, k); v != "" {
				dst[k] = v
			}
		}
	}

	switch typ {
	case "hyperv_power":
		copyStr(out.Target, tgt, "host_id", "vm_id", "id", "name")
		if strMap(out.Target, "host_id") == "" || (strMap(out.Target, "vm_id") == "" && strMap(out.Target, "id") == "" && strMap(out.Target, "name") == "") {
			return out, "", fmt.Errorf("missing host_id/vm_id")
		}
		act := strings.ToLower(strMap(params, "action"))
		if act == "" {
			act = "restart"
		}
		switch act {
		case "start", "stop", "restart", "force_stop":
		default:
			return out, "", fmt.Errorf("invalid power action")
		}
		out.Params["action"] = act
		return out, "high", nil

	case "hyperv_config":
		copyStr(out.Target, tgt, "host_id", "vm_id", "id", "name")
		if strMap(out.Target, "host_id") == "" || (strMap(out.Target, "vm_id") == "" && strMap(out.Target, "id") == "" && strMap(out.Target, "name") == "") {
			return out, "", fmt.Errorf("missing host_id/vm_id")
		}
		for _, k := range []string{"processor_count", "memory_mb", "memory_min_mb", "memory_max_mb"} {
			if v, ok := params[k]; ok && v != nil {
				out.Params[k] = v
			}
		}
		if v, ok := params["dynamic_memory"].(bool); ok {
			out.Params["dynamic_memory"] = v
		}
		return out, "high", nil

	case "container_action":
		copyStr(out.Target, tgt, "host_id", "id", "container_id", "name")
		if strMap(out.Target, "host_id") == "" || (strMap(out.Target, "id") == "" && strMap(out.Target, "container_id") == "") {
			return out, "", fmt.Errorf("missing host_id/id")
		}
		act := strings.ToLower(strMap(params, "action"))
		if act == "" {
			act = "restart"
		}
		switch act {
		case "start", "stop", "restart":
		default:
			return out, "", fmt.Errorf("invalid container action")
		}
		out.Params["action"] = act
		return out, "high", nil

	case "container_exec":
		copyStr(out.Target, tgt, "host_id", "id", "container_id", "name")
		cmd := strMap(params, "command")
		if strMap(out.Target, "host_id") == "" || (strMap(out.Target, "id") == "" && strMap(out.Target, "container_id") == "") || cmd == "" {
			return out, "", fmt.Errorf("missing host_id/id/command")
		}
		if err := opsValidateCommand(cmd); err != nil {
			return out, "", err
		}
		out.Params["command"] = cmd
		if t := strMap(params, "timeout_sec"); t != "" {
			out.Params["timeout_sec"] = params["timeout_sec"]
		}
		return out, "high", nil

	case "k8s_scale":
		copyStr(out.Target, tgt, "cluster_id", "namespace", "name")
		if strMap(out.Target, "cluster_id") == "" || strMap(out.Target, "namespace") == "" || strMap(out.Target, "name") == "" {
			return out, "", fmt.Errorf("missing cluster_id/namespace/name")
		}
		if params["replicas"] == nil {
			return out, "", fmt.Errorf("missing replicas")
		}
		out.Params["replicas"] = params["replicas"]
		return out, "high", nil

	case "k8s_restart", "k8s_undo", "k8s_delete_pod":
		copyStr(out.Target, tgt, "cluster_id", "namespace", "name")
		if strMap(out.Target, "cluster_id") == "" || strMap(out.Target, "namespace") == "" || strMap(out.Target, "name") == "" {
			return out, "", fmt.Errorf("missing cluster_id/namespace/name")
		}
		risk := "high"
		if typ == "k8s_restart" {
			risk = "medium"
		}
		return out, risk, nil

	case "k8s_exec":
		copyStr(out.Target, tgt, "cluster_id", "namespace", "name")
		cmd := strMap(params, "command")
		if strMap(out.Target, "cluster_id") == "" || strMap(out.Target, "namespace") == "" || strMap(out.Target, "name") == "" || cmd == "" {
			return out, "", fmt.Errorf("missing cluster_id/namespace/name/command")
		}
		if err := opsValidateCommand(cmd); err != nil {
			return out, "", err
		}
		out.Params["command"] = cmd
		if params["timeout_sec"] != nil {
			out.Params["timeout_sec"] = params["timeout_sec"]
		}
		return out, "high", nil

	case "host_playbook":
		copyStr(out.Target, tgt, "host_id", "name")
		if strMap(out.Target, "host_id") == "" {
			return out, "", fmt.Errorf("missing host_id")
		}
		stepsRaw, ok := params["steps"].([]any)
		if !ok || len(stepsRaw) == 0 {
			return out, "", fmt.Errorf("missing steps")
		}
		if len(stepsRaw) > 20 {
			return out, "", fmt.Errorf("too many steps (max 20)")
		}
		steps := make([]map[string]any, 0, len(stepsRaw))
		for si, raw := range stepsRaw {
			sm, ok := raw.(map[string]any)
			if !ok {
				return out, "", fmt.Errorf("step[%d] invalid", si)
			}
			cmd := strMap(sm, "command")
			cmdWin := strMap(sm, "command_win")
			if cmd == "" && cmdWin == "" && strMap(sm, "module") == "" {
				return out, "", fmt.Errorf("step[%d] empty", si)
			}
			if err := opsValidateCommand(cmd); err != nil {
				return out, "", fmt.Errorf("step[%d]: %w", si, err)
			}
			if err := opsValidateCommand(cmdWin); err != nil {
				return out, "", fmt.Errorf("step[%d]: %w", si, err)
			}
			step := map[string]any{}
			copyStr(step, sm, "name", "module", "command", "command_win", "target")
			if sm["args"] != nil {
				step["args"] = sm["args"]
			}
			if sm["timeout_sec"] != nil {
				step["timeout_sec"] = sm["timeout_sec"]
			}
			if v, ok := sm["continue_on_error"].(bool); ok {
				step["continue_on_error"] = v
			}
			if v, ok := sm["ignore_exit"].(bool); ok {
				step["ignore_exit"] = v
			}
			steps = append(steps, step)
		}
		out.Params["steps"] = steps
		copyStr(out.Params, params, "name", "description")
		return out, "high", nil

	case "sql_apply":
		sql := strMap(params, "sql")
		if sql == "" {
			sql = strMap(params, "rewritten")
		}
		if sql == "" {
			return out, "", fmt.Errorf("missing sql")
		}
		if utf8.RuneCountInString(sql) > 200000 {
			return out, "", fmt.Errorf("sql too large")
		}
		out.Params["sql"] = sql
		return out, "low", nil

	case "sql_ddl":
		copyStr(out.Target, tgt, "connection_id")
		sql := strMap(params, "sql")
		if strMap(out.Target, "connection_id") == "" || sql == "" {
			return out, "", fmt.Errorf("missing connection_id/sql")
		}
		if utf8.RuneCountInString(sql) > 200000 {
			return out, "", fmt.Errorf("sql too large")
		}
		low := strings.ToLower(sql)
		if strings.Contains(low, "drop table") || strings.Contains(low, "drop database") || strings.Contains(low, "truncate") {
			return out, "", fmt.Errorf("destructive DDL not allowed via AI plan")
		}
		out.Params["sql"] = sql
		copyStr(out.Params, params, "reason", "verify_sql")
		if params["timeout_sec"] != nil {
			out.Params["timeout_sec"] = params["timeout_sec"]
		}
		return out, "high", nil
	}
	return out, "", fmt.Errorf("unsupported type")
}

func opsValidateCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	if utf8.RuneCountInString(cmd) > 4000 {
		return fmt.Errorf("command too long")
	}
	if opsDangerCmdRe.MatchString(cmd) {
		return fmt.Errorf("command matches blocked dangerous pattern")
	}
	return nil
}

// sanitizeAssistActionReply strips invalid action JSON from high-risk assist replies.
func sanitizeAssistActionReply(task, reply string) (string, bool) {
	task = strings.ToLower(task)
	if !strings.Contains(task, "remediation") && !strings.Contains(task, "ops_plan") {
		return reply, false
	}
	plan, err := parseOpsActionPlanJSON(reply)
	if err != nil || plan == nil || len(plan.Actions) == 0 {
		return reply, false
	}
	norm, _, err := ValidateOpsActionPlan(plan)
	if err == nil {
		b, _ := json.MarshalIndent(norm, "", "  ")
		// Prefer keeping prose before JSON if present.
		if i := strings.Index(reply, "{"); i > 0 {
			return strings.TrimSpace(reply[:i]) + "\n\n```json\n" + string(b) + "\n```", false
		}
		return "```json\n" + string(b) + "\n```", false
	}
	// Strip executable JSON on validation failure.
	if i := strings.Index(reply, "{"); i >= 0 {
		prose := strings.TrimSpace(reply[:i])
		warn := "\n\n> ⚠️ 服务端已拦截不可信/非法动作计划：" + err.Error()
		if prose == "" {
			return strings.TrimSpace(warn), true
		}
		return prose + warn, true
	}
	return reply + "\n\n> ⚠️ 服务端已拦截非法动作计划：" + err.Error(), true
}

// filterUIActions keeps only allowlisted UI action types; navigate_view must resolve.
func filterUIActions(actions []map[string]any) []map[string]any {
	if len(actions) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(actions))
	for _, a := range actions {
		if a == nil {
			continue
		}
		typ, _ := a["type"].(string)
		typ = strings.ToLower(strings.TrimSpace(typ))
		if !opsAllowedUI[typ] {
			continue
		}
		if typ == "navigate_view" {
			view, _ := a["view"].(string)
			if def, ok := resolveUIView(view); ok {
				a = cloneMap(a)
				a["view"] = def.View
				if strings.TrimSpace(fmt.Sprint(a["label"])) == "" {
					a["label"] = def.Title
				}
				if strings.TrimSpace(fmt.Sprint(a["title"])) == "" {
					a["title"] = def.Title
				}
			} else {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func isHighRiskAssistTask(task string) bool {
	t := strings.ToLower(strings.TrimSpace(task))
	return strings.Contains(t, "remediation") || strings.Contains(t, "ops_plan") ||
		t == "host_remediation" || t == "k8s_remediation" || t == "sql_ops_plan"
}
