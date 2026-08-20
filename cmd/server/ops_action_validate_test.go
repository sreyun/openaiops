package main

import "testing"

func TestValidateOpsActionPlanRejectsUnknownType(t *testing.T) {
	plan := &OpsActionPlan{Actions: []OpsAction{{
		Type: "shell_exec", Params: map[string]any{"command": "id"},
	}}}
	if _, _, err := ValidateOpsActionPlan(plan); err == nil {
		t.Fatal("expected reject for unknown type")
	}
}

func TestValidateOpsActionPlanRejectsDangerousPlaybook(t *testing.T) {
	plan := &OpsActionPlan{Actions: []OpsAction{{
		Type:   "host_playbook",
		Target: map[string]any{"host_id": "h1"},
		Params: map[string]any{"steps": []any{
			map[string]any{"name": "wipe", "command": "rm -rf /"},
		}},
	}}}
	if _, _, err := ValidateOpsActionPlan(plan); err == nil {
		t.Fatal("expected reject for dangerous command")
	}
}

func TestValidateOpsActionPlanOKHyperV(t *testing.T) {
	plan := &OpsActionPlan{Actions: []OpsAction{{
		Type:   "hyperv_power",
		Target: map[string]any{"host_id": "h1", "vm_id": "vm1"},
		Params: map[string]any{"action": "restart"},
		Risk:   "low", // model claim ignored
	}}}
	norm, risk, err := ValidateOpsActionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if risk != "high" {
		t.Fatalf("risk=%s want high", risk)
	}
	if norm.Actions[0].Risk != "high" {
		t.Fatalf("action risk not recomputed")
	}
}

func TestSanitizeAssistActionReplyStripsBadJSON(t *testing.T) {
	reply := "分析如下\n{\"summary\":\"x\",\"actions\":[{\"type\":\"evil\",\"params\":{}}]}"
	out, stripped := sanitizeAssistActionReply("host_remediation", reply)
	if !stripped {
		t.Fatalf("expected strip, got %q", out)
	}
	if stringsContains(out, `"type":"evil"`) {
		t.Fatalf("evil action leaked: %s", out)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
