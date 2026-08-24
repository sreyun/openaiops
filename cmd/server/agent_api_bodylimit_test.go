package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Agent 摄入端点全在 isPublicPath 里，指纹校验发生在解码之后：未鉴权的一方
// 送一个几 MB 的 gzip 包，解出来就是几百 MB 的内存分配。上限必须落在
// decompressBody 里，而不是靠每个 handler 自觉。
func TestDecompressBodyCapsDecompressedSize(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	// 40 MiB 的高度可压缩内容 —— 压缩后只有几十 KB，轻松穿过全局 bodyLimit。
	chunk := strings.Repeat("A", 1<<20)
	for i := 0; i < 40; i++ {
		if _, err := io.WriteString(zw, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 1<<20 {
		t.Fatalf("压缩包本身就该很小，实际 %d 字节", buf.Len())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/report", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	body, err := decompressBody(req)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	n, err := io.Copy(io.Discard, body)
	if err != nil {
		t.Fatalf("读取不该报错（截断即可）：%v", err)
	}
	if n > agentIngestMaxBodyBytes {
		t.Fatalf("解压后读出了 %d 字节，超过上限 %d", n, agentIngestMaxBodyBytes)
	}
}

// 未压缩的请求体同样要受限，否则换个不带 Content-Encoding 的请求就绕过去了。
func TestDecompressBodyCapsPlainBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/report",
		strings.NewReader(strings.Repeat("B", agentIngestMaxBodyBytes+4096)))
	body, err := decompressBody(req)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	n, _ := io.Copy(io.Discard, body)
	if n != agentIngestMaxBodyBytes {
		t.Fatalf("未压缩体应被截到 %d 字节，实际 %d", agentIngestMaxBodyBytes, n)
	}
}

// 正常大小的上报必须原样通过——上限不能把真实业务挡掉。
func TestDecompressBodyPassesNormalReport(t *testing.T) {
	payload := `{"host_id":"h1","hostname":"web-01","metrics":{}}`
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = io.WriteString(zw, payload)
	_ = zw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/report", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	body, err := decompressBody(req)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("正常上报被改动了：%q", string(got))
	}
}
