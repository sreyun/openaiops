// Command licensetool 是**签发方**用的离线授权签发工具，不随产品交付给客户。
//
// 它与服务端共享一个约定：授权文件是 `AIOPS-LIC1.<base64url(payload)>.<base64url(sig)>`，
// 载荷是一段 JSON，签名是对载荷原始字节的 Ed25519 签名。服务端只内置公钥，
// 因此签发过程完全离线——不需要许可服务器，客户内网也验得了。
//
//	go build ./cmd/licensetool
//
//	# 一次性：生成签发密钥对（私钥务必保存在仓库之外）
//	licensetool -gen-key
//
//	# 签发：把公钥内置进服务端（-X main.licenseVendorPubKey=<pub>）后即可验签
//	AIOPS_LICENSE_PRIVKEY=<privb64> licensetool -issue \
//	    -customer "某某集团" -max-hosts 200 -expires 2027-08-31 \
//	    -install-id AIO-XXXX-XXXX-XXXX-XXXX -edition enterprise -out license.txt
//
//	# 核验：确认发出去的文件能被内置公钥验过
//	AIOPS_LICENSE_PUBKEY=<pubb64> licensetool -verify license.txt
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const tokenPrefix = "AIOPS-LIC1"

type licensePayload struct {
	ID        string `json:"id"`
	Customer  string `json:"customer"`
	Edition   string `json:"edition,omitempty"`
	MaxHosts  int    `json:"max_hosts"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	GraceDays int    `json:"grace_days,omitempty"`
	InstallID string `json:"install_id,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

func main() {
	genKey := flag.Bool("gen-key", false, "生成一对签发密钥（Ed25519），私钥请保存在仓库之外")
	issue := flag.Bool("issue", false, "签发授权文件")
	verify := flag.String("verify", "", "校验指定授权文件（公钥取 AIOPS_LICENSE_PUBKEY）")
	customer := flag.String("customer", "", "客户名称")
	edition := flag.String("edition", "standard", "版本标识（仅展示用）")
	maxHosts := flag.Int("max-hosts", 0, "授权主机数上限，0 = 不限")
	expires := flag.String("expires", "", "到期日 YYYY-MM-DD，留空 = 永久")
	grace := flag.Int("grace", 30, "过期后的宽限天数（宽限期内仍全功能）")
	installID := flag.String("install-id", "", "绑定的部署指纹（控制台「授权」页可见），留空 = 不绑定")
	notes := flag.String("notes", "", "备注（合同号等，会显示在控制台）")
	licID := flag.String("id", "", "授权编号，留空自动生成")
	keyFile := flag.String("key-file", "", "私钥文件（base64 单行）；亦可用 AIOPS_LICENSE_PRIVKEY")
	out := flag.String("out", "", "输出文件，留空打印到标准输出")
	flag.Parse()

	switch {
	case *genKey:
		runGenKey()
	case *verify != "":
		runVerify(*verify)
	case *issue:
		runIssue(*keyFile, *out, licensePayload{
			ID:        *licID,
			Customer:  *customer,
			Edition:   *edition,
			MaxHosts:  *maxHosts,
			GraceDays: *grace,
			InstallID: strings.TrimSpace(*installID),
			Notes:     *notes,
		}, *expires)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func runGenKey() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal(err)
	}
	fmt.Println("公钥（内置进服务端：-ldflags \"-X main.licenseVendorPubKey=...\"）：")
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	fmt.Println()
	fmt.Println("私钥（只留在签发方，切勿入库）：")
	fmt.Println(base64.StdEncoding.EncodeToString(priv))
}

func runIssue(keyFile, out string, p licensePayload, expires string) {
	if p.Customer == "" {
		fatal(fmt.Errorf("-customer 必填"))
	}
	priv, err := loadPriv(keyFile)
	if err != nil {
		fatal(err)
	}
	if p.ID == "" {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			fatal(err)
		}
		p.ID = "LIC-" + strings.ToUpper(base64.RawURLEncoding.EncodeToString(b[:]))
	}
	p.IssuedAt = time.Now().Unix()
	if strings.TrimSpace(expires) != "" {
		t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(expires), time.Local)
		if err != nil {
			fatal(fmt.Errorf("-expires 需要 YYYY-MM-DD：%w", err))
		}
		// 到期日按当天 23:59:59 计，避免"写了 8-31 结果 8-31 当天就过期"。
		p.ExpiresAt = t.Add(24*time.Hour - time.Second).Unix()
	}
	token, err := buildToken(priv, p)
	if err != nil {
		fatal(err)
	}
	text := renderLicenseFile(token)
	if out == "" {
		fmt.Print(text)
	} else if err := os.WriteFile(out, []byte(text), 0o600); err != nil {
		fatal(err)
	} else {
		fmt.Printf("已签发：%s\n客户=%s 主机上限=%d 到期=%s 部署=%s\n",
			out, p.Customer, p.MaxHosts, dateStr(p.ExpiresAt), orDash(p.InstallID))
	}
}

// buildToken 把载荷签成 `AIOPS-LIC1.<b64url(payload)>.<b64url(sig)>`。
// 单独抽出来是为了能在测试里跑一遍「签发 → 服务端同款解析」的往返——
// 这是唯一一处「发出去的文件客户到底能不能装上”的机器校验，
// 而这类错误一旦发生，是在客户现场发现的。
func buildToken(priv ed25519.PrivateKey, p licensePayload) (string, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	return tokenPrefix + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// renderLicenseFile 包上 BEGIN/END 并按 72 列折行：授权文件常被邮件/微信转发，
// 不折行的一长串在很多客户端里会被截断。
func renderLicenseFile(token string) string {
	return "-----BEGIN AIOPS LICENSE-----\n" + wrap(token, 72) + "\n-----END AIOPS LICENSE-----\n"
}

func runVerify(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	pubB64 := strings.TrimSpace(os.Getenv("AIOPS_LICENSE_PUBKEY"))
	if pubB64 == "" {
		fatal(fmt.Errorf("请用 AIOPS_LICENSE_PUBKEY 指定公钥"))
	}
	pubRaw, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		fatal(fmt.Errorf("公钥无效"))
	}
	parts := strings.Split(clean(string(raw)), ".")
	if len(parts) != 3 || parts[0] != tokenPrefix {
		fatal(fmt.Errorf("授权文件格式不正确"))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		fatal(err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pubRaw), payload, sig) {
		fatal(fmt.Errorf("签名校验失败"))
	}
	var p licensePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		fatal(err)
	}
	fmt.Printf("签名有效\n编号=%s 客户=%s 版本=%s 主机上限=%d 到期=%s 宽限=%d天 部署=%s\n",
		p.ID, p.Customer, p.Edition, p.MaxHosts, dateStr(p.ExpiresAt), p.GraceDays, orDash(p.InstallID))
}

func loadPriv(keyFile string) (ed25519.PrivateKey, error) {
	b64 := strings.TrimSpace(os.Getenv("AIOPS_LICENSE_PRIVKEY"))
	if keyFile != "" {
		raw, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, err
		}
		b64 = strings.TrimSpace(string(raw))
	}
	if b64 == "" {
		return nil, fmt.Errorf("缺少私钥：用 -key-file 或 AIOPS_LICENSE_PRIVKEY 提供")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("私钥不是合法 base64：%w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("私钥长度不对（%d）", len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}

func clean(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-----") {
			continue
		}
		b.WriteString(strings.Join(strings.Fields(line), ""))
	}
	return b.String()
}

func wrap(s string, n int) string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return strings.Join(append(out, s), "\n")
}

func dateStr(ts int64) string {
	if ts <= 0 {
		return "永久"
	}
	return time.Unix(ts, 0).Format("2006-01-02")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误：", err)
	os.Exit(1)
}
