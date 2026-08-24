package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// 签发工具此前**一条测试都没有**。它是唯一决定「客户拿到的授权文件能不能装上」的
// 环节，而这类错误只会在客户现场暴露：文件已经发出去了，人已经在等着开工。
// 这里把往返链路（签发 → 折行/包装 → 还原 → 验签 → 解载荷）整条钉住。

func TestBuildTokenRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want := licensePayload{
		ID: "LIC-TEST", Customer: "某某集团", Edition: "enterprise",
		MaxHosts: 200, IssuedAt: 1756000000, ExpiresAt: 1790000000,
		GraceDays: 30, InstallID: "AIO-AAAA-BBBB-CCCC-DDDD", Notes: "合同 HT-2026-0821",
	}
	token, err := buildToken(priv, want)
	if err != nil {
		t.Fatal(err)
	}

	// 服务端 licenseParse 的等价流程：拆三段 → 校前缀 → 验签 → 解 JSON。
	parts := strings.Split(clean(renderLicenseFile(token)), ".")
	if len(parts) != 3 {
		t.Fatalf("令牌应为三段，实际 %d 段", len(parts))
	}
	if parts[0] != tokenPrefix {
		t.Fatalf("前缀应为 %s，实际 %s", tokenPrefix, parts[0])
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("载荷不是 base64url：%v", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("签名不是 base64url：%v", err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("用配对公钥验签失败——发出去的文件客户装不上")
	}
	var got licensePayload
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("载荷不是合法 JSON：%v", err)
	}
	if got != want {
		t.Fatalf("往返后载荷变了：\n want=%+v\n  got=%+v", want, got)
	}
}

// 改一个字节就必须验不过——不然「防篡改」只是句话。
func TestBuildTokenRejectsTamper(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	token, err := buildToken(priv, licensePayload{ID: "LIC-1", Customer: "c", MaxHosts: 10})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	var tampered licensePayload
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(raw, &tampered)
	tampered.MaxHosts = 100000 // 把主机上限改大
	forged, _ := json.Marshal(tampered)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if ed25519.Verify(pub, forged, sig) {
		t.Fatal("改过主机上限的载荷竟然验签通过")
	}
}

// 另一把私钥签出来的文件必须被拒：这是「授权只能由签发方发」的全部依据。
func TestForeignKeyDoesNotVerify(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	token, err := buildToken(otherPriv, licensePayload{ID: "LIC-2", Customer: "c", MaxHosts: 1})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if ed25519.Verify(pub, payload, sig) {
		t.Fatal("别人的私钥签出来的授权竟然验过了")
	}
}

// 授权文件常被邮件/微信转发一圈，回来时可能多了换行、空格或 BEGIN/END 包装。
// clean() 必须把这些都吃掉，否则客户粘贴后看到的是「格式不正确」。
func TestCleanToleratesMangledPaste(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	token, err := buildToken(priv, licensePayload{ID: "LIC-3", Customer: "c", MaxHosts: 5})
	if err != nil {
		t.Fatal(err)
	}
	mangled := "  \n" + renderLicenseFile(token) + "\n \n"
	mangled = strings.ReplaceAll(mangled, "\n", " \n ") // 每行前后再塞点空格
	if got := clean(mangled); got != token {
		t.Fatalf("被转发弄脏的文件没能还原：\n want=%s\n  got=%s", token, got)
	}
}

func TestWrapKeepsContent(t *testing.T) {
	s := strings.Repeat("x", 200)
	w := wrap(s, 72)
	if strings.ReplaceAll(w, "\n", "") != s {
		t.Fatal("折行改动了内容")
	}
	for _, line := range strings.Split(w, "\n") {
		if len(line) > 72 {
			t.Fatalf("折行后仍有 %d 列的长行", len(line))
		}
	}
}
