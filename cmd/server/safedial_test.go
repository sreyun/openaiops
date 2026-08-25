package main

import (
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSSRFBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		strict  bool
		blocked bool
		desc    string
	}{
		{"169.254.169.254", false, true, "AWS/GCP/Azure 元数据"},
		{"169.254.170.2", false, true, "AWS ECS 元数据"},
		{"100.100.100.200", false, true, "阿里云元数据"},
		{"fd00:ec2::254", false, true, "AWS IPv6 元数据"},
		{"169.254.1.23", false, true, "链路本地"},
		{"8.8.8.8", false, false, "公网默认放行"},
		{"1.1.1.1", true, false, "公网严格模式也放行"},
		{"10.0.0.5", false, false, "内网默认放行（保留内网监控/自建 LLM）"},
		{"192.168.1.10", false, false, "内网默认放行"},
		{"127.0.0.1", false, false, "环回默认放行"},
		{"10.0.0.5", true, true, "内网严格模式拒绝"},
		{"192.168.1.10", true, true, "内网严格模式拒绝"},
		{"127.0.0.1", true, true, "环回严格模式拒绝"},
		{"172.16.5.5", true, true, "RFC1918 严格模式拒绝"},
	}
	for _, c := range cases {
		got, why := ssrfBlockedIP(net.ParseIP(c.ip), c.strict)
		if got != c.blocked {
			t.Errorf("%s: ssrfBlockedIP(%s, strict=%v)=%v want %v (why=%q)", c.desc, c.ip, c.strict, got, c.blocked, why)
		}
	}
	if b, _ := ssrfBlockedIP(nil, false); !b {
		t.Error("无法解析的 IP(nil) 应被拒绝")
	}
}

// TestGuardedClientBlocksMetadata 验证带防护的 client 在 connect 前就拒绝元数据地址（快速失败，不挂起）。
func TestGuardedClientBlocksMetadata(t *testing.T) {
	c := newGuardedHTTPClient(2 * time.Second)
	_, err := c.Get("http://169.254.169.254/latest/meta-data/iam/")
	if err == nil {
		t.Fatal("应拒绝连接云元数据地址")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("错误信息应含 SSRF 提示，实际：%v", err)
	}
}

// 配了 HTTP_PROXY 的部署里，Dialer 上的 Control 钩子看到的是**代理**的 IP —— 一个
// 完全合法的地址。目标地址是写在请求行 / CONNECT 里交给代理去连的，整套 IP 校验
// 因此被绕过：代理会替攻击者把 169.254.169.254 取回来，服务端拿到的是实例 IAM 凭据。
//
// 这条路只在环境里真有代理变量时才走到，所以本机（有 HTTP_PROXY）会红、CI（没有）
// 会绿——正是最容易被漏掉的一类。直接测判定函数，不依赖环境。
func TestGuardedProxyBlocksMetadataTarget(t *testing.T) {
	proxy, _ := url.Parse("http://127.0.0.1:7897")
	cases := []struct {
		target  string
		strict  bool
		blocked bool
		desc    string
	}{
		{"http://169.254.169.254/latest/meta-data/iam/", false, true, "AWS 元数据 IP"},
		{"http://100.100.100.200/latest/meta-data/", false, true, "阿里云元数据 IP"},
		{"http://metadata.google.internal/computeMetadata/v1/", false, true, "GCP 元数据域名"},
		{"http://METADATA.GOOGLE.INTERNAL/x", false, true, "域名大小写不敏感"},
		{"https://api.openai.com/v1/chat/completions", false, false, "正常出站要放行"},
		{"http://10.0.0.5/hook", false, false, "内网默认放行（自建 LLM / 内网 Webhook）"},
		{"http://10.0.0.5/hook", true, true, "内网严格模式拒绝"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.target)
		if err != nil {
			t.Fatalf("%s: 用例 URL 本身解析失败：%v", c.desc, err)
		}
		got, gotErr := guardedProxyDecision(proxy, nil, u, c.strict)
		if c.blocked {
			if gotErr == nil {
				t.Errorf("%s: 应拒绝经代理连接 %s，实际放行到 %v", c.desc, c.target, got)
				continue
			}
			if !strings.Contains(gotErr.Error(), "SSRF") {
				t.Errorf("%s: 错误信息应含 SSRF 提示，实际：%v", c.desc, gotErr)
			}
			continue
		}
		if gotErr != nil {
			t.Errorf("%s: 不该拦 %s，实际：%v", c.desc, c.target, gotErr)
		} else if got == nil {
			t.Errorf("%s: %s 应当照常走代理，实际返回直连", c.desc, c.target)
		}
	}
}

// 没配代理时这层不介入：目标 IP 的校验交给更严的 ssrfDialControl（它看的是
// DNS 解析后的实际 IP，顺带覆盖 30x 重定向与 DNS rebinding）。
func TestGuardedProxyPassesThroughWithoutProxy(t *testing.T) {
	u, _ := url.Parse("http://169.254.169.254/latest/meta-data/")
	got, err := guardedProxyDecision(nil, nil, u, false)
	if err != nil || got != nil {
		t.Errorf("没有代理时应原样返回 (nil, nil)，实际：(%v, %v)", got, err)
	}
}
