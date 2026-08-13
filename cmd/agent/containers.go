package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// containersAvailable is true when docker or podman is on PATH.
func containersAvailable() bool {
	return containerCLI() != ""
}

func collectContainers() ([]shared.ContainerInfo, string, string) {
	cli := containerCLI()
	if cli == "" {
		return nil, "", "docker/podman not found"
	}
	// Label placeholders: Docker Compose + common Podman compose project label.
	format := "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.State}}|{{.Ports}}|{{.CreatedAt}}|{{.Label \"com.docker.compose.project\"}}|{{.Label \"com.docker.compose.service\"}}|{{.Label \"io.podman.compose.project\"}}"
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "ps", "-a", "--format", format)
	out, err := cmd.Output()
	if err != nil {
		return nil, cli, fmt.Sprintf("%s ps failed: %v", cli, err)
	}
	var list []shared.ContainerInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 10)
		if len(parts) < 5 {
			continue
		}
		c := shared.ContainerInfo{
			ID:      strings.TrimSpace(parts[0]),
			Name:    strings.TrimPrefix(strings.TrimSpace(parts[1]), "/"),
			Image:   strings.TrimSpace(parts[2]),
			Status:  strings.TrimSpace(parts[3]),
			State:   strings.TrimSpace(parts[4]),
			Runtime: cli,
		}
		if len(parts) > 5 {
			c.Ports = strings.TrimSpace(parts[5])
		}
		if len(parts) > 6 {
			c.Created = strings.TrimSpace(parts[6])
		}
		project := ""
		service := ""
		if len(parts) > 7 {
			project = strings.TrimSpace(parts[7])
		}
		if len(parts) > 8 {
			service = strings.TrimSpace(parts[8])
		}
		if project == "" && len(parts) > 9 {
			project = strings.TrimSpace(parts[9])
		}
		c.ComposeProject = project
		c.ComposeService = service
		if len(c.ID) > 12 {
			c.ID = c.ID[:12]
		}
		list = append(list, c)
	}
	return list, cli, ""
}

func (a *Agent) runContainerCollector(ctx context.Context) {
	interval := a.containerInterval
	if interval < 30*time.Second {
		interval = 60 * time.Second
	}
	slog.Info("容器采集器启动", "interval", interval)
	collectAndPost := func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("容器采集 panic 已恢复", "panic", r)
			}
		}()
		list, runtime, errMsg := collectContainers()
		rep := shared.ContainerReport{
			HostID:      a.identity.HostID,
			Fingerprint: a.identity.Fingerprint,
			Timestamp:   time.Now().Unix(),
			HostName:    a.identity.Hostname,
			Runtime:     runtime,
			Containers:  list,
			Error:       errMsg,
		}
		if errMsg != "" {
			slog.Warn("容器采集失败", "err", errMsg)
		} else {
			slog.Info("容器采集完成", "n", len(list), "runtime", runtime)
		}
		a.postContainerReport(rep)
	}
	collectAndPost()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collectAndPost()
		}
	}
}

func (a *Agent) postContainerReport(rep shared.ContainerReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			// Marshal per target: each panel may know this machine by a different
			// host_id (see serverTarget.hostIDOr). A shared local-id body is 403'd
			// forever by forwardFingerprintOKByHost on the rebound panel.
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				return
			}
			req, err := http.NewRequest(http.MethodPost, tgt.server+"/api/v1/agent/containers", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Debug("容器上报失败", "server", tgt.server, "err", err)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
		}(t)
	}
}
