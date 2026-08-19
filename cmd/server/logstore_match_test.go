package main

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// containsSubstrFold 必须与「先 ToLower 再 Contains」完全等价——它是替换那条路径的，
// 行为一旦有偏差，用户会发现"搜得到的日志忽然搜不到了"，而且很难联想到是匹配函数换了。
func TestContainsSubstrFoldMatchesToLowerContains(t *testing.T) {
	corpus := []string{
		"", "a", "ERROR", "error", "Connection Pool EXHAUSTED",
		"2026-08-18T00:00:01 app[7] Connection pool exhausted while serving id=abc",
		"中文日志：连接池已耗尽 CONNECTION pool",
		"MiXeD CaSe ÄÖÜ payload", "tab\tseparated ERROR value",
		// 折叠会改变字节宽度的两个经典字符：İ(U+0130,2B)→i(1B)、K(U+212A,3B)→k(1B)。
		// 纯按字节比的实现会在这里漏掉，而它们真的会出现在土耳其语/开尔文单位的日志里。
		"İSTANBUL node down", "temp 300K threshold",
		strings.Repeat("x", 300) + "NeedLe" + strings.Repeat("y", 300),
	}
	needles := []string{
		"", "error", "exhausted", "connection pool", "needle", "连接池", "äöü", "zzz", "a",
		"istanbul", "300k", "i̇", "ö",
	}
	for _, s := range corpus {
		for _, n := range needles {
			kw := strings.ToLower(n)
			want := strings.Contains(strings.ToLower(s), kw)
			got := containsSubstrFold(s, kw)
			if got != want {
				t.Fatalf("containsSubstrFold(%q, %q)=%v want %v", s, kw, got, want)
			}
		}
	}
}

// 关键字比正文长、空正文等边界不能 panic。
func TestContainsSubstrFoldEdges(t *testing.T) {
	if !containsSubstrFold("anything", "") {
		t.Fatal("空关键字应视为匹配（与 kw==\"\" 时不过滤的语义一致）")
	}
	if containsSubstrFold("ab", "abc") {
		t.Fatal("关键字比正文长时不应匹配")
	}
	if containsSubstrFold("", "a") {
		t.Fatal("空正文不应匹配非空关键字")
	}
}

// 手挑语料只能覆盖想得到的情况，而这个函数的整个价值就在于"与 ToLower+Contains 完全等价"。
// 用固定种子在混合字母表上随机撞，撞出来的反例可复现。
func TestContainsSubstrFoldDifferentialRandom(t *testing.T) {
	// 混合 ASCII 大小写、拉丁重音、CJK，以及折叠后字节宽度会变的 İ / K(开尔文)。
	alphabet := []rune("abAB01 _中文ÄäÖöİK\u212a")
	rng := rand.New(rand.NewSource(20260818))
	pick := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		return sb.String()
	}
	for i := 0; i < 20000; i++ {
		s := pick(1 + rng.Intn(24))
		n := pick(1 + rng.Intn(5))
		// 一半的样本改用正文的真实子串，否则随机串几乎撞不出"命中"这一侧的分支。
		if rng.Intn(2) == 0 {
			r := []rune(s)
			a := rng.Intn(len(r))
			b := a + 1 + rng.Intn(len(r)-a)
			n = string(r[a:b])
		}
		kw := strings.ToLower(n)
		if !utf8.ValidString(s) || !utf8.ValidString(kw) {
			continue // 非法 UTF-8 是刻意的分歧（见 containsSubstrFold 注释），不参与差分
		}
		if got, want := containsSubstrFold(s, kw), strings.Contains(strings.ToLower(s), kw); got != want {
			t.Fatalf("差分不一致 #%d: containsSubstrFold(%q, %q)=%v want %v", i, s, kw, got, want)
		}
	}
}

// 基准语料必须带大写。strings.ToLower 对「全小写 ASCII」有一条原样返回的快路径，
// 拿全小写的行去比等于比了个寂寞——而真实日志行里级别、主机名、ISO 时间戳的 T/Z
// 几乎必然带大写，走的正是会分配的那条路。
const benchLogLine = "2026-08-18T00:00:01Z WARN app[7] node=PROD-DB-01 Connection Pool EXHAUSTED while serving request id=AbC123 "

// 含中文的正文走的是 unicode 慢路径，它同样必须零分配——否则等于把优化又还回去了。
func BenchmarkContainsSubstrFoldUnicode(b *testing.B) {
	line := strings.Repeat("连接池已耗尽 "+benchLogLine, 6)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = containsSubstrFold(line, "exhausted")
	}
}

func BenchmarkContainsSubstrFold(b *testing.B) {
	line := strings.Repeat(benchLogLine, 6)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = containsSubstrFold(line, "exhausted")
	}
}

func BenchmarkToLowerContains(b *testing.B) {
	line := strings.Repeat(benchLogLine, 6)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = strings.Contains(strings.ToLower(line), "exhausted")
	}
}
