package main

import (
	"os/exec"
	"strings"
	"testing"
)

// 安装脚本是被 `curl -fsSL … | sh` 直接以 root 执行的生成文本，在它跑起来之前没有任何
// 环节会解析它。一个不闭合的 if/case、一处漏掉的引号，代价是所有新装机器同时失败。
// 升级脚本已有同类门禁（TestLegacyUnixAgentUpdateScriptParses），安装脚本此前没有。
func TestInstallShellTemplatesParse(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	for name, tpl := range map[string]string{
		"install.sh":       installShTemplate,
		"uninstall.sh":     uninstallShTemplate,
		"relay-install.sh": relayInstallShTemplate,
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(sh, "-n")
			cmd.Stdin = strings.NewReader(tpl)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s 不是合法 POSIX sh: %v\n%s", name, err, out)
			}
		})
	}
}

// 下载失败时必须报**真实**的 HTTP 状态码。
//
// 这条来自现网：中继回源失败返回 502，而脚本里写死了一句「HTTP 404 usually means
// 服务端镜像缺二进制」，于是装机的人照着 404 的方向查，第一步就走错了。错误信息里
// 断言一个并没有发生的状态码，比不给信息更糟。
func TestInstallScriptReportsRealHTTPStatus(t *testing.T) {
	if strings.Contains(installShTemplate, "HTTP 404 usually means") {
		t.Fatal("install.sh 仍在把任何下载失败都当成 404 解释")
	}
	for _, must := range []string{"aiops_http_status", "aiops_explain_http"} {
		if !strings.Contains(installShTemplate, must) {
			t.Fatalf("install.sh 缺少 %s：无法报出真实状态码", must)
		}
	}
	// 502 是中继/反代回源失败的形态，必须给出指向中继而不是指向服务端镜像的排查方向。
	if !strings.Contains(installShTemplate, "502") {
		t.Fatal("install.sh 未对 502 给出专门解释（中继回源失败是最常见的非 404 失败）")
	}
	if !strings.Contains(installShTemplate, "HTTP_PROXY") {
		t.Fatal("install.sh 的 502 解释里应提示中继机的出网代理设置")
	}
}

// 中继机是"唯一能出网"的那台，而企业里"能出网"往往等于"经由 HTTP 代理出网"。
// systemd 服务不继承登录 shell 的环境变量：装机时 curl 通、中继起来后回源却直连，
// 结果是内网每台机器注册/上报全 502，网关机上手工 curl 却一切正常。
func TestRelayInstallPassesProxyEnvIntoUnit(t *testing.T) {
	for _, must := range []string{"RELAY_ENV", "HTTPS_PROXY", "NO_PROXY", "${RELAY_ENV}ExecStart="} {
		if !strings.Contains(relayInstallShTemplate, must) {
			t.Fatalf("中继安装脚本没有把出网代理写进 systemd 单元，缺少 %q", must)
		}
	}
}
