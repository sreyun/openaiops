package main

import (
	"strings"
	"testing"
)

// 网关中继的安装命令必须带 token。
//
// 网关不是"只起个反代"：cmd/agent/main.go 的 relay 分支起了中继之后照常采集上报，
// 它本身就是一台被纳管的主机。命令里少了 token，服务端开着安装 Token 校验时它注册
// 被 403 拒掉，现场症状是「内网机器全都在，唯独这台网关在主机列表里找不到」——而
// 中继转发一切正常，没人会往注册失败上想。/install-relay.sh 早就支持 ?token=
// （handleRelayInstallScript），漏的一直是控制台生成的那条命令。
func TestClassicRelayGatewayCmdCarriesToken(t *testing.T) {
	b, err := webFS.ReadFile("web/js/settings.js")
	if err != nil {
		t.Fatalf("读取经典版 settings.js 失败: %v", err)
	}
	js := string(b)
	for _, script := range []string{"install-relay.sh", "install-relay.ps1"} {
		if !strings.Contains(js, script+"?${gwQ}") {
			t.Errorf("经典版网关命令没有带查询参数：%s 缺少 ?${gwQ}", script)
		}
	}
	if !strings.Contains(js, `const gwQ = "token=" + encodeURIComponent(token)`) {
		t.Error("经典版网关命令的查询参数里必须有 token，否则网关注册不上（403）")
	}
	if !strings.Contains(js, `maskInstallCmd(gatewayCmd)`) {
		t.Error("网关命令现在带 token，展示时必须打码（复制走 dataset.rawCmd）")
	}
}
