package main

import (
	"fmt"
	"regexp"
	"strings"
)

// diagArgsReadOnly rejects the write / destructive OPTIONS of commands that sit on
// the read-only diagnostic allowlist (evaluateDiagCommand).
//
// The allowlist only checks the command NAME, but several of those "read-only"
// tools have flags that write files, change system state or even execute other
// programs — and run_diagnostic executes on the managed host (usually as root),
// reachable by any MCP token with the "sre" scope and by prompt-injected AI:
//
//	cat /dev/null | sort -o /etc/passwd     sort -o overwrites any file;
//	                                        --compress-program EXECUTES a program
//	uniq /etc/hosts /etc/passwd             2nd operand is an OUTPUT file
//	journalctl --vacuum-time=1s / --rotate  deletes the system journal
//	dmesg -C / dmesg -n 1                   clears the ring buffer / console level
//	date -s ... / date 010100002020         sets the system clock
//	hostname evil                           renames the host
//	ss -K dst 10.0.0.5                      force-kills live connections
//	sar -o /etc/x 1 1                       writes a data file anywhere
//	kubectl get secrets -o yaml             dumps cluster secrets
//
// Rules are conservative: an ambiguous form (option clusters, abbreviated GNU
// long options) is rejected rather than parsed optimistically.
func diagArgsReadOnly(command string) (bool, string) {
	for _, seg := range strings.Split(command, "|") {
		toks := strings.Fields(strings.TrimSpace(seg))
		if len(toks) == 0 {
			continue
		}
		args := make([]string, 0, len(toks)-1)
		for _, t := range toks[1:] {
			args = append(args, strings.Trim(t, `"'`))
		}
		name := strings.ToLower(strings.Trim(toks[0], `"'`))
		if reason := diagArgViolation(name, args); reason != "" {
			return false, fmt.Sprintf("诊断命令 %q 使用了写入/破坏性参数（%s），只读诊断通道已拦截", strings.TrimSpace(seg), reason)
		}
	}
	return true, ""
}

// dateSetOperand matches the positional form that SETS the clock:
// [MMDDhhmm[[CC]YY][.ss]] (GNU and BSD date).
var dateSetOperand = regexp.MustCompile(`^[0-9]{8}([0-9]{2}){0,2}(\.[0-9]{2})?$`)

func diagArgViolation(name string, args []string) string {
	switch name {
	case "sort":
		if shortOptHas(args, "oT", "kSt") || longOptHas(args, "output", "compress-program", "temporary-directory") {
			return "sort -o/--output/--compress-program/-T"
		}
	case "uniq":
		if uniqHasOutputOperand(args) {
			return "uniq 的第二个操作数是输出文件"
		}
	case "sar":
		if shortOptHas(args, "o", "fiseIPmn") {
			return "sar -o"
		}
	case "journalctl":
		if longOptHas(args, "vacuum-size", "vacuum-time", "vacuum-files", "rotate", "flush", "sync",
			"relinquish-var", "smart-relinquish-var", "setup-keys", "update-catalog") {
			return "journalctl --vacuum-*/--rotate/--flush 等"
		}
	case "dmesg":
		if shortOptHas(args, "cCnDE", "lfsF") || longOptHas(args, "clear", "read-clear", "console-level", "console-off", "console-on") {
			return "dmesg -c/-C/-n/-D/-E"
		}
	case "date":
		if shortOptHas(args, "s", "dfIr") || longOptHas(args, "set") {
			return "date -s/--set"
		}
		for _, a := range args {
			if dateSetOperand.MatchString(a) {
				return "date MMDDhhmm 设置时间"
			}
		}
	case "hostname":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				return "hostname <新主机名>"
			}
		}
		if shortOptHas(args, "Fb", "") || longOptHas(args, "file", "boot") {
			return "hostname -F/-b"
		}
	case "ss":
		if shortOptHas(args, "KD", "AfFN") || longOptHas(args, "kill", "diag") {
			return "ss -K/--kill/-D"
		}
	case "kubectl":
		if len(args) > 0 && strings.EqualFold(args[0], "get") {
			for _, a := range args[1:] {
				if strings.Contains(strings.ToLower(a), "secret") {
					return "kubectl get secrets"
				}
			}
		}
	}
	return ""
}

// shortOptHas reports whether any single-dash option cluster contains one of the
// letters in bad. Walking stops at the first letter in argTaking, because the rest
// of the cluster is that option's argument (e.g. date -Iseconds, dmesg -lcrit).
func shortOptHas(args []string, bad, argTaking string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue
		}
		for _, c := range a[1:] {
			if strings.ContainsRune(bad, c) {
				return true
			}
			if strings.ContainsRune(argTaking, c) {
				break
			}
		}
	}
	return false
}

// longOptHas reports whether any --option names one of the given long options,
// including the unambiguous abbreviations getopt_long accepts (--out → --output).
func longOptHas(args []string, names ...string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if !strings.HasPrefix(a, "--") {
			continue
		}
		n := a[2:]
		if i := strings.IndexByte(n, '='); i >= 0 {
			n = n[:i]
		}
		if n == "" {
			continue
		}
		for _, name := range names {
			if strings.HasPrefix(name, n) {
				return true
			}
		}
	}
	return false
}

// uniqHasOutputOperand reports whether uniq got a second operand ([INPUT [OUTPUT]]).
func uniqHasOutputOperand(args []string) bool {
	positional := 0
	endOfOpts := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endOfOpts {
			switch {
			case a == "--":
				endOfOpts = true
				continue
			case a == "-f" || a == "-s" || a == "-w" ||
				a == "--skip-fields" || a == "--skip-chars" || a == "--check-chars":
				i++ // option value
				continue
			case len(a) > 1 && a[0] == '-':
				continue
			}
		}
		positional++
	}
	return positional >= 2
}
