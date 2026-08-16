package main

import (
	"os"
	"testing"
)

func TestDumpHelper2(t *testing.T) {
	s := buildWindowsUpdateHelperScript(
		`C:\Program Files\AIOps Agent\aiops-agent.exe`,
		`C:\Program Files\AIOps Agent\.aiops-agent.new.1234`,
		`C:\Program Files\AIOps Agent\config.yaml`,
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.log`,
		`C:\Program Files\AIOps Agent\aiops-agent-update.result`,
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.result`)
	os.WriteFile("/tmp/claude-0/-opt-aiops/2f025a4b-239f-4fcd-b9f5-d27d7f4e9abc/scratchpad/helper_new.ps1", []byte(s), 0o644)
}
