package main

import (
	"os"
	"strings"
	"testing"
)

func TestDevDockerfileIncludesWin2012AgentArtifact(t *testing.T) {
	raw, err := os.ReadFile("../../docker/Dockerfile.dev")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(raw)
	for _, required := range []string{
		"AS win2012-builder",
		"-o /out/aiops-agent-windows-amd64-win2012.exe ./cmd/agent",
		"COPY --from=win2012-builder /out/aiops-agent-windows-amd64-win2012.exe /app/dist/aiops-agent-windows-amd64-win2012.exe",
		"aiops-agent-windows-amd64-win2012.exe.sha256",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile.dev missing %q", required)
		}
	}
}
