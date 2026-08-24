package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 服务端 i18n 的门禁。前端有 check:i18n，服务端此前一条也没有——于是三份语言包
// 各自漂了一段时间：en 少 5 个键、zh-TW 少 8 个，还有两个键（ai.config_saved /
// log.forward_agent_offline）**三份里都没有**，界面与日志里直接把键名原样印出来
// （"ai.config_saved"）。这三条断言把那三类问题各钉一条。

func loadLocale(t *testing.T, lang string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("i18n", lang+".json"))
	if err != nil {
		t.Fatalf("读取语言包 %s 失败: %v", lang, err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("语言包 %s 不是合法 JSON: %v", lang, err)
	}
	return m
}

// TestI18nLocalesHaveSameKeys 三份语言包的键集合必须完全一致。
// 缺键不会报错，只会静默回退到 zh-CN——英文客户看到一段简体中文，没人会当成 bug 报上来。
func TestI18nLocalesHaveSameKeys(t *testing.T) {
	base := loadLocale(t, defaultLang)
	for _, lang := range supportedLangs {
		if lang == defaultLang {
			continue
		}
		other := loadLocale(t, lang)
		var missing, extra []string
		for k := range base {
			if _, ok := other[k]; !ok {
				missing = append(missing, k)
			}
		}
		for k := range other {
			if _, ok := base[k]; !ok {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s 缺少 %d 个键: %v", lang, len(missing), missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s 多出 %d 个 %s 没有的键: %v", lang, len(extra), defaultLang, extra)
		}
	}
}

var i18nVerbRE = regexp.MustCompile(`%[-+# 0]*[0-9.*]*[a-zA-Z]`)

// TestI18nFormatVerbsMatch 同一个键在三种语言里的 fmt 占位符必须一一对应。
// 少一个 %s，用户看到的就是 "%!s(MISSING)"；多一个，多出来的参数被吞掉。
func TestI18nFormatVerbsMatch(t *testing.T) {
	base := loadLocale(t, defaultLang)
	verbs := func(s string) []string {
		out := []string{}
		for _, v := range i18nVerbRE.FindAllString(strings.ReplaceAll(s, "%%", ""), -1) {
			out = append(out, v[len(v)-1:])
		}
		return out
	}
	for _, lang := range supportedLangs {
		if lang == defaultLang {
			continue
		}
		for k, v := range loadLocale(t, lang) {
			want, got := verbs(base[k]), verbs(v)
			if strings.Join(want, "") != strings.Join(got, "") {
				t.Errorf("键 %s 的占位符不一致：%s=%v %s=%v", k, defaultLang, want, lang, got)
			}
		}
	}
}

// i18nCallRE 匹配 T/Tr/Tz 的**字面量**键。动态拼接（如 Tr(r, "terminal_auth."+code)）
// 靠"字面量后面紧跟 , 或 )"这一条排除掉——那种键没法静态校验。
var i18nCallRE = regexp.MustCompile(`\b(?:Tz|Tr|T)\(\s*(?:r|req|lang|[a-zA-Z_][\w.]*)?\s*,?\s*"([a-z][a-z0-9_.]*)"\s*[,)]`)

// TestI18nReferencedKeysExist 源码里写死的键必须在语言包里存在。
// 不存在时 T() 原样返回键名，于是活动日志里出现 "ai.config_saved" 这种东西。
func TestI18nReferencedKeysExist(t *testing.T) {
	base := loadLocale(t, defaultLang)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range i18nCallRE.FindAllStringSubmatch(string(raw), -1) {
			key := m[1]
			if !strings.Contains(key, ".") { // 单词参数（如 T(lang, "on")）不是键
				continue
			}
			if _, ok := base[key]; !ok {
				missing[key] = append(missing[key], f)
			}
		}
	}
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for k := range missing {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Errorf("语言包缺少被引用的键 %q（引用处：%v）", k, missing[k])
		}
	}
}
