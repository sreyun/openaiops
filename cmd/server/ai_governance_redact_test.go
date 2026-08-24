package main

import (
	"strings"
	"testing"
)

// redactAIText 是写进交付说明的合规能力（「AI 设置 → 敏感字段脱敏」）。
// 这里钉住两件事：该挡的挡住了，不该动的没被改坏。
func TestRedactAITextMasksCredentials(t *testing.T) {
	cases := []struct {
		name, in string
		gone     []string // 脱敏后不允许再出现的子串
		keep     []string // 必须原样保留的子串（模型还要靠它定位问题）
	}{
		{
			name: "openai key",
			in:   "curl -H 'Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz012345' https://api",
			gone: []string{"sk-abcdefghijklmnopqrstuvwxyz012345"},
		},
		{
			name: "key value pair",
			in:   `db connect api_key=8f3kd91mZq host=10.0.0.7 port=5432`,
			gone: []string{"8f3kd91mZq"},
			keep: []string{"10.0.0.7", "port=5432", "api_key="},
		},
		{
			name: "password in command",
			in:   "mysql -uroot -h db1 password: 'Hunter2!secret'",
			gone: []string{"Hunter2!secret"},
			keep: []string{"db1"},
		},
		{
			name: "email and phone",
			in:   "联系人 ops@example.com 手机 13800138000 主机 web-01",
			gone: []string{"ops@example.com", "13800138000"},
			keep: []string{"web-01"},
		},
		{
			name: "github and aws keys",
			in:   "ghp_abcdefghijklmnopqrstuvwxyz0123 AKIAIOSFODNN7EXAMPLE",
			gone: []string{"ghp_abcdefghijklmnopqrstuvwxyz0123", "AKIAIOSFODNN7EXAMPLE"},
		},
		{
			name: "long hash",
			in:   "image sha 5d41402abc4b2a76b9719d911017c592aa",
			gone: []string{"5d41402abc4b2a76b9719d911017c592aa"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := redactAIText(c.in, true)
			for _, g := range c.gone {
				if strings.Contains(out, g) {
					t.Errorf("敏感内容未脱敏：%q 仍出现在 %q", g, out)
				}
			}
			for _, k := range c.keep {
				if !strings.Contains(out, k) {
					t.Errorf("不该脱敏的内容被改坏：%q 从 %q 里消失了", k, out)
				}
			}
		})
	}
}

// 旧实现把每一个 @ 都换成 [at]，于是镜像摘要、Java 注解、user@host 形式的命令全被改坏，
// 送到模型那边的上下文已经不是现场原文了。
func TestRedactAITextKeepsNonEmailAtSigns(t *testing.T) {
	in := "docker pull nginx@sha256:abc @Service 注解 ssh deploy@10.0.0.9"
	out := redactAIText(in, true)
	for _, keep := range []string{"nginx@sha256", "@Service", "deploy@10.0.0.9"} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q 被误脱敏：%q", keep, out)
		}
	}
}

func TestRedactAITextDisabledIsIdentity(t *testing.T) {
	in := "api_key=sk-abcdefghijklmnopqrstuvwxyz012345"
	if got := redactAIText(in, false); got != in {
		t.Fatalf("关闭时不应改动原文，得到 %q", got)
	}
}
