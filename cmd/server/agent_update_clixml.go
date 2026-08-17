package main

import (
	"regexp"
	"strconv"
	"strings"
)

// PowerShell 在输出被重定向时，把非 stdout 的每一条记录（错误、警告、详细、**进度**）
// 用 CLIXML 序列化后写进 stderr。Agent 的 exec 通道把 stdout 与 stderr 合流，于是升级
// 结果里会混进这样一坨：
//
//	#< CLIXML
//	legacy agent update ok helper=... log=C:\ProgramData\...
//	<Objs Version="1.1.0.1" xmlns="..."><Obj S="progress" ...><AV>Preparing modules for first use.</AV>…
//
// 现网原样撞上过：那条 XML 几千字节，而 host result 的 message 只留 500 字，于是唯一
// 有用的那行「到底哪条路把助手拉起来了、日志在哪」被挤出可视范围，操作台上只剩一坨
// 谁也不会读的 XML。
//
// 但直接把整坨丢掉也不对：**真正的错误信息就藏在同一坨里**（`<S S="Error">`），那往往
// 是唯一说清失败原因的东西。所以这里做的是「解码而非丢弃」——把 Error/Warning 记录还原
// 成可读的行，把进度/详细这类噪声删掉。
var (
	clixmlHeaderRe = regexp.MustCompile(`(?m)^\s*#<\s*CLIXML\s*$\r?\n?`)
	clixmlObjsRe   = regexp.MustCompile(`(?s)<Objs\b[^>]*>.*?</Objs>`)
	clixmlTailRe   = regexp.MustCompile(`(?s)<Objs\b[^>]*>.*$`)
	clixmlStringRe = regexp.MustCompile(`(?s)<S(?:\s+S="([^"]*)")?[^>]*>(.*?)</S>`)
	clixmlEscapeRe = regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)
)

// sanitizePowerShellOutput turns a raw exec result into something a human can
// read: CLIXML noise out, the diagnostics that were buried in it back in as
// plain lines. Input without CLIXML is returned untouched.
func sanitizePowerShellOutput(out string) string {
	if !strings.Contains(out, "CLIXML") && !strings.Contains(out, "<Objs") {
		return out
	}
	cleaned := clixmlHeaderRe.ReplaceAllString(out, "")
	cleaned = strings.ReplaceAll(cleaned, "#< CLIXML", "")
	cleaned = clixmlObjsRe.ReplaceAllStringFunc(cleaned, decodeCLIXMLDiagnostics)
	// A blob truncated mid-way (the message cap cut it, or the process died while
	// writing) never matches the paired form above. Nothing readable can follow an
	// unterminated <Objs>, so decode what is there and drop the rest.
	cleaned = clixmlTailRe.ReplaceAllStringFunc(cleaned, decodeCLIXMLDiagnostics)
	lines := strings.Split(cleaned, "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.TrimSpace(strings.TrimRight(ln, "\r")) == "" {
			continue
		}
		kept = append(kept, strings.TrimRight(ln, "\r"))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// decodeCLIXMLDiagnostics keeps only the record kinds that carry a message a
// human would want (Error / Warning), and only the first few of them: a failing
// loop can emit hundreds of identical records, and this text shares a 500-byte
// budget with the actual command output.
func decodeCLIXMLDiagnostics(blob string) string {
	var out []string
	seen := map[string]bool{}
	for _, m := range clixmlStringRe.FindAllStringSubmatch(blob, -1) {
		kind := m[1]
		if kind != "Error" && kind != "Warning" {
			continue
		}
		text := strings.TrimSpace(decodeCLIXMLText(m[2]))
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, "ps "+strings.ToLower(kind)+": "+text)
		if len(out) >= 5 {
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "\n" + strings.Join(out, "\n")
}

// decodeCLIXMLText undoes the two layers PowerShell applies to a string record:
// XML entities, and its own _xNNNN_ escape for characters XML cannot carry
// literally (newlines arrive as _x000D__x000A_).
func decodeCLIXMLText(s string) string {
	s = clixmlEscapeRe.ReplaceAllStringFunc(s, func(esc string) string {
		n, err := strconv.ParseUint(esc[2:6], 16, 32)
		if err != nil {
			return esc
		}
		switch r := rune(n); r {
		case '\r':
			return ""
		case '\n', '\t':
			return " "
		default:
			if r < 0x20 {
				return " "
			}
			return string(r)
		}
	})
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&#39;", "'", "&amp;", "&")
	s = r.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
