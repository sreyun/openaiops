package main

import "testing"

// 只读诊断通道读到 /etc/shadow 或 Agent 自己的 config.yaml，都是越权。
// 等价路径写法必须先规范化再判定，否则一个多余的斜杠就绕过去了。
func TestDiagCommandBlocksSensitivePathVariants(t *testing.T) {
	for _, cmd := range []string{
		"cat /etc/shadow",
		"cat /etc//shadow",
		"cat /etc/../etc/shadow",
		"tail -n 50 /opt/aiops-agent/config.yaml",
		"cat /proc/self/environ",
		"head /home/u/.ssh/id_rsa",
		"cat /srv/certs/server.key",
	} {
		if ok, _ := diagCommandAllowed(cmd); ok {
			t.Errorf("敏感路径命令未被拦截: %s", cmd)
		}
	}
}

// 正常只读诊断不能被误伤。
func TestDiagCommandStillAllowsOrdinaryReads(t *testing.T) {
	for _, cmd := range []string{
		"df -hT",
		"cat /proc/meminfo",
		"tail -n 200 /var/log/messages",
		"journalctl -n 100 --no-pager",
		"ps aux | head -20",
		"cat /opt/app/application.yaml",
	} {
		if ok, reason := diagCommandAllowed(cmd); !ok {
			t.Errorf("普通只读命令被误伤: %s (%s)", cmd, reason)
		}
	}
}

// 服务端侧的敏感路径判定同样要认等价写法与 Agent 自身凭据。
func TestDeniedSensitivePathNormalizes(t *testing.T) {
	for _, p := range []string{
		"/etc/./shadow",
		`C:\Windows\..\Windows\System32\config\SAM`,
		"/opt/aiops-agent/config.yaml",
		"/proc/1/environ",
		"/data/backup/id_ed25519",
	} {
		if !deniedSensitivePath(p) {
			t.Errorf("敏感路径未拦截: %s", p)
		}
	}
	for _, p := range []string{"/var/log/app.log", "/etc/hosts", "/opt/app/config.yaml"} {
		if deniedSensitivePath(p) {
			t.Errorf("普通路径被误伤: %s", p)
		}
	}
}
