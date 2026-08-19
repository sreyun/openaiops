package main

import (
	"unicode"
	"unicode/utf8"
)

// containsSubstrFold：日志正文的大小写不敏感子串匹配（与 alertgov.go 里按列表比对的
// containsFold 是两回事，故不同名）。substrLower 必须是调用方已经 strings.ToLower 过的关键字。
//
// 原来的写法是 `strings.Contains(strings.ToLower(l.Message), kw)`：**每条日志都会分配
// 一份小写副本**。日志环上限 5 万条、单条正文最长 4000 字节，于是一次关键字检索最坏
// 会产生近 200 MB 的临时垃圾——而这一切还发生在持锁期间，摄入被一并挡住。
// 500 个日志采集器持续写入时，这是"搜一次日志、整个平台顿一下"的直接来源。
//
// 这里改成逐字节扫描、零分配，并保持与「先 ToLower 再 Contains」等价：
//
//   - 快路径按 ASCII 折叠逐字节比。它只可能漏、不可能错报——正文里 ≥0x80 的字节永远
//     等不上已小写的 ASCII 关键字字节；而按原字节比中的非 ASCII 关键字，命中即真命中
//     （UTF-8 自同步，合法关键字只会落在 rune 边界上）。
//   - 快路径没命中、且正文含非 ASCII 时，再按 rune 走一遍 unicode.ToLower 折叠，接住
//     ÄÖÜ↔äöü、İ→i、K(U+212A)→k 这类跨字节宽度的折叠。纯 ASCII 正文（绝大多数日志）
//     永远走不到这条路径。
//
// 唯一与 ToLower 的刻意分歧：正文里的非法 UTF-8 字节按原字节比较，而不是先替换成
// U+FFFD 再比。只有关键字本身含 U+FFFD 时才看得出差别，不值得为此付出代价。
func containsSubstrFold(s, substrLower string) bool {
	if substrLower == "" {
		return true
	}
	if len(substrLower) > len(s) {
		return false
	}
	// 首字符先做一次廉价筛选，避免对每个位置都进入完整比较。
	first := substrLower[0]
	last := len(s) - len(substrLower)
	nonASCII := false
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= utf8.RuneSelf {
			nonASCII = true
		}
		if i <= last && lowerASCII(b) == first && equalFoldPrefix(s[i:], substrLower) {
			return true
		}
	}
	if nonASCII {
		return containsFoldUnicode(s, substrLower)
	}
	return false
}

// equalFoldPrefix 判断 s 是否以 prefix（已小写）开头，忽略 ASCII 大小写。
func equalFoldPrefix(s, prefixLower string) bool {
	if len(prefixLower) > len(s) {
		return false
	}
	for i := 0; i < len(prefixLower); i++ {
		if lowerASCII(s[i]) != prefixLower[i] {
			return false
		}
	}
	return true
}

// containsFoldUnicode 是含非 ASCII 正文的慢路径：按 rune 逐个 unicode.ToLower 折叠后比较。
// 仍然零分配——只在字符串上滑窗，不构造小写副本。
func containsFoldUnicode(s, substrLower string) bool {
	for i := 0; i < len(s); {
		if hasPrefixFoldUnicode(s[i:], substrLower) {
			return true
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		if sz <= 0 {
			sz = 1
		}
		i += sz
	}
	return false
}

// hasPrefixFoldUnicode 判断 s 折叠后是否以 prefixLower 开头。prefixLower 已由
// strings.ToLower 逐 rune 折叠过，所以这里对 s 做同样的逐 rune 折叠即可对齐。
func hasPrefixFoldUnicode(s, prefixLower string) bool {
	for len(prefixLower) > 0 {
		if len(s) == 0 {
			return false
		}
		pr, psz := utf8.DecodeRuneInString(prefixLower)
		sr, ssz := utf8.DecodeRuneInString(s)
		if pr == utf8.RuneError && psz == 1 || sr == utf8.RuneError && ssz == 1 {
			// 非法字节：按原字节比，避免在这里做 U+FFFD 替换的语义。
			if s[0] != prefixLower[0] {
				return false
			}
		} else if unicode.ToLower(sr) != pr {
			return false
		}
		s = s[ssz:]
		prefixLower = prefixLower[psz:]
	}
	return true
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
