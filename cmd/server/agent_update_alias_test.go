package main

import (
	"regexp"
	"strings"
	"testing"
)

// 现网 2026-08-17 19:44，server11：
//
//	failed | script | 命令退出码 1: ps error: Set-ItemProperty : Cannot evaluate parameter 'Name'
//	because its argument is specified as a script block and there is no input.
//	At line:19 char:10 + Sp 'wmi' {if(([wmiclass]'Win32_Process')…
//
// 引导脚本里定义了 `function Sp`，可 **PowerShell 的命令解析顺序是：别名 → 函数 → cmdlet
// → 可执行文件**，别名排在函数前面。`sp` 是 Set-ItemProperty 的内置别名，于是
// `Sp 'wmi' {…}` 被解析成 `Set-ItemProperty -Path 'wmi' -Name {脚本块}`，整条引导在拉起
// 助手之前就终止了——一次升级都没发生。
//
// 这条规则对本仓库特别危险：这两段脚本要塞进 cmd.exe 的 8191 字符命令行，写法被迫极限
// 压缩，短名字满地都是，而内置别名恰恰全是两三个字母：sp/sc/gc/gi/si/gm/gp/ls/cp/mv/rm/
// ni/rd/ri/rp/rv/sl/sv/ft/fl/fw/gu/oh/ii/iex/irm/iwr/start/set/write/where/select/sort…
//
// 本机没有 PowerShell 可用来做真解析，所以这条规则只能靠静态检查守住。
var powerShellBuiltinAliases = map[string]bool{
	"ac": true, "asnp": true, "cat": true, "cd": true, "chdir": true, "clc": true,
	"clear": true, "clhy": true, "cli": true, "clp": true, "cls": true, "clv": true,
	"cnsn": true, "compare": true, "copy": true, "cp": true, "cpi": true, "cpp": true,
	"curl": true, "cvpa": true, "dbp": true, "del": true, "diff": true, "dir": true,
	"dnsn": true, "ebp": true, "echo": true, "epal": true, "epcsv": true, "epsn": true,
	"erase": true, "etsn": true, "exsn": true, "fc": true, "fhx": true, "fl": true,
	"ft": true, "fw": true, "gal": true, "gbp": true, "gc": true, "gci": true,
	"gcm": true, "gcs": true, "gdr": true, "ghy": true, "gi": true, "gjb": true,
	"gl": true, "gm": true, "gmo": true, "gp": true, "gps": true, "gpv": true,
	"group": true, "gsn": true, "gsnp": true, "gsv": true, "gu": true, "gv": true,
	"gwmi": true, "h": true, "history": true, "icm": true, "iex": true, "ihy": true,
	"ii": true, "ipal": true, "ipcsv": true, "ipmo": true, "ipsn": true, "irm": true,
	"ise": true, "iwmi": true, "iwr": true, "kill": true, "lp": true, "ls": true,
	"man": true, "md": true, "measure": true, "mi": true, "mount": true, "move": true,
	"mp": true, "mv": true, "nal": true, "ndr": true, "ni": true, "nmo": true,
	"npssc": true, "nsn": true, "nv": true, "ogv": true, "oh": true, "popd": true,
	"ps": true, "pushd": true, "pwd": true, "r": true, "rbp": true, "rcjb": true,
	"rcsn": true, "rd": true, "rdr": true, "ren": true, "ri": true, "rjb": true,
	"rm": true, "rmdir": true, "rmo": true, "rni": true, "rnp": true, "rp": true,
	"rsn": true, "rsnp": true, "rujb": true, "rv": true, "rvpa": true, "rwmi": true,
	"sajb": true, "sal": true, "saps": true, "sasv": true, "sbp": true, "sc": true,
	"select": true, "set": true, "shcm": true, "si": true, "sl": true, "sleep": true,
	"sls": true, "sort": true, "sp": true, "spjb": true, "spps": true, "spsv": true,
	"start": true, "stz": true, "sujb": true, "sv": true, "swmi": true, "tee": true,
	"trcm": true, "type": true, "wget": true, "where": true, "wjb": true, "write": true,
}

// 有意使用的别名：它们在任何 Windows PowerShell 上语义都固定，且比全名短得多，
// 而命令行预算是这两段脚本的硬约束。白名单以外的别名一律视为事故。
var intentionalPSAliases = map[string]bool{"md": true, "rm": true, "sleep": true}

var psKeywords = map[string]bool{
	"if": true, "else": true, "elseif": true, "for": true, "foreach": true, "while": true,
	"do": true, "try": true, "catch": true, "finally": true, "function": true, "param": true,
	"return": true, "throw": true, "switch": true, "break": true, "continue": true,
	"exit": true, "in": true, "begin": true, "process": true, "end": true, "filter": true,
	"data": true, "trap": true, "class": true, "enum": true, "using": true, "hidden": true,
	"static": true, "default": true,
}

var (
	psStringLiteralRe = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	psCommentLineRe   = regexp.MustCompile(`(?m)^\s*#.*$`)
	psFunctionDefRe   = regexp.MustCompile(`(?mi)^\s*function\s+([A-Za-z_][A-Za-z0-9_\-]*)`)
	psWordRe          = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-]*`)
)

// psGeneratedScripts are every PowerShell body this server hands to a host.
func psGeneratedScripts(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"bootstrap": decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA)),
		"helper":    windowsUpdateHelperScript(),
		"evidence":  decodeLegacyWindowsPS(t, windowsUpdateEvidenceCommand()),
	}
}

// 定义一个与内置别名同名的函数，等于定义一个**永远不会被调用**的函数。
func TestGeneratedScriptsDefineNoAliasShadowedFunction(t *testing.T) {
	for name, body := range psGeneratedScripts(t) {
		for _, m := range psFunctionDefRe.FindAllStringSubmatch(body, -1) {
			if powerShellBuiltinAliases[strings.ToLower(m[1])] {
				t.Errorf("%s: function %q is shadowed by the built-in alias %q — "+
					"PowerShell resolves aliases BEFORE functions, so this function can never run. "+
					"Rename it, or store the body in a variable and call it with &$Var.",
					name, m[1], strings.ToLower(m[1]))
			}
		}
	}
}

// 反过来的一半：脚本里以裸名字调用的命令，凡是撞上内置别名的，要么是有意为之
// （白名单），要么就是下一次的事故。
func TestBootstrapCommandsDoNotCollideWithBuiltinAliases(t *testing.T) {
	for name, body := range psGeneratedScripts(t) {
		for _, word := range leadingCommandWords(body) {
			low := strings.ToLower(word)
			if !powerShellBuiltinAliases[low] || intentionalPSAliases[low] {
				continue
			}
			t.Errorf("%s: %q is invoked as a bare command but it is a built-in PowerShell alias "+
				"(%q). Either use the full cmdlet name, or add it to intentionalPSAliases with a reason.",
				name, word, low)
		}
	}
}

// 守卫本身也要被守：把事故当天的原文喂回去，两条检查都必须报警。没有这一条，上面两条
// 测试可能只是"永远绿"的装饰。
func TestAliasGuardsCatchTheRealIncident(t *testing.T) {
	incident := "$k=''\n" +
		"function Sp($tg,$bk){if($k){return};try{&$bk}catch{return}}\n" +
		"Sp 'wmi' {if(([wmiclass]'Win32_Process').Create($c).ReturnValue){throw 'x'}}\n"

	var shadowed bool
	for _, m := range psFunctionDefRe.FindAllStringSubmatch(incident, -1) {
		if powerShellBuiltinAliases[strings.ToLower(m[1])] {
			shadowed = true
		}
	}
	if !shadowed {
		t.Fatal("the function-shadowing guard would NOT have caught `function Sp`")
	}

	var called bool
	for _, w := range leadingCommandWords(incident) {
		if strings.EqualFold(w, "Sp") {
			called = true
		}
	}
	if !called {
		t.Fatal("the bare-command guard would NOT have caught the `Sp 'wmi' {…}` call")
	}
}

// leadingCommandWords returns the first bare word of every statement. String
// literals and comments are blanked first: prose inside a throw message is not
// code, and a stray "; see …" would otherwise read as a command called `see`.
func leadingCommandWords(body string) []string {
	body = psCommentLineRe.ReplaceAllString(body, "")
	body = psStringLiteralRe.ReplaceAllString(body, `''`)
	var out []string
	for _, frag := range strings.FieldsFunc(body, func(r rune) bool {
		return r == '\n' || r == ';' || r == '{' || r == '}' || r == '|'
	}) {
		frag = strings.TrimSpace(frag)
		if frag == "" {
			continue
		}
		w := psWordRe.FindString(frag)
		if w == "" || psKeywords[strings.ToLower(w)] {
			continue
		}
		// A word directly followed by '=' or ':' is an assignment/label, not a call.
		if rest := frag[len(w):]; strings.HasPrefix(strings.TrimSpace(rest), "=") {
			continue
		}
		out = append(out, w)
	}
	return out
}
