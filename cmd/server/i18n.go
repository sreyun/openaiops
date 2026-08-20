package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

//go:embed i18n/*.json
var i18nFS embed.FS

var (
	i18nMu     sync.RWMutex
	i18nStores = map[string]map[string]string{}
)

// Supported languages in priority order.
var supportedLangs = []string{"zh-CN", "zh-TW", "en"}

// Default language when detection fails.
const defaultLang = "zh-CN"

func init() {
	for _, lang := range supportedLangs {
		data, err := i18nFS.ReadFile("i18n/" + lang + ".json")
		if err != nil {
			continue
		}
		var m map[string]string
		if json.Unmarshal(data, &m) == nil {
			i18nStores[lang] = m
		}
	}
}

// normalizeLang converts a language tag to one of our supported languages.
// Returns "" if the input doesn't match any supported language.
func normalizeLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch {
	case strings.HasPrefix(lang, "zh-cn") || lang == "zh" || lang == "zh-hans" || strings.HasPrefix(lang, "zh-sg"):
		return "zh-CN"
	case strings.HasPrefix(lang, "zh-tw") || strings.HasPrefix(lang, "zh-hk") || strings.HasPrefix(lang, "zh-mo") || lang == "zh-hant":
		return "zh-TW"
	case strings.HasPrefix(lang, "en"):
		return "en"
	}
	return ""
}

// parseAcceptLanguage parses the Accept-Language header and returns the
// first supported language, or "" if none match.
func parseAcceptLanguage(header string) string {
	parts := strings.Split(header, ",")
	for _, part := range parts {
		// Strip quality value (e.g. "en-US;q=0.9")
		part = strings.TrimSpace(strings.Split(part, ";")[0])
		if lang := normalizeLang(part); lang != "" {
			return lang
		}
	}
	return ""
}

// langFromRequest detects the preferred language from the HTTP request.
// Priority: ?lang= query param > Accept-Language header > default zh-CN.
func langFromRequest(r *http.Request) string {
	// 1. URL query param
	if lang := r.URL.Query().Get("lang"); lang != "" {
		if normalized := normalizeLang(lang); normalized != "" {
			return normalized
		}
	}
	// 2. Cookie (persisted user preference from the dashboard)
	if c, err := r.Cookie("aiops_lang"); err == nil && c.Value != "" {
		if normalized := normalizeLang(c.Value); normalized != "" {
			return normalized
		}
	}
	// 3. Accept-Language header
	if al := r.Header.Get("Accept-Language"); al != "" {
		if lang := parseAcceptLanguage(al); lang != "" {
			return lang
		}
	}
	// 4. Default
	return defaultLang
}

// T translates a key to the specified language, with optional fmt.Sprintf args.
// Falls back to zh-CN, then to the key itself if not found.
func T(lang, key string, args ...any) string {
	i18nMu.RLock()
	defer i18nMu.RUnlock()
	// Try exact language match
	if store, ok := i18nStores[lang]; ok {
		if msg, ok := store[key]; ok {
			if len(args) > 0 {
				return fmt.Sprintf(msg, args...)
			}
			return msg
		}
	}
	// Fallback to zh-CN
	if store, ok := i18nStores[defaultLang]; ok {
		if msg, ok := store[key]; ok {
			if len(args) > 0 {
				return fmt.Sprintf(msg, args...)
			}
			return msg
		}
	}
	// Last resort: return the key itself
	return key
}

// Tr translates using the language detected from the HTTP request.
func Tr(r *http.Request, key string, args ...any) string {
	return T(langFromRequest(r), key, args...)
}

// Tz translates with the default language (zh-CN). Used for internal log
// messages and notifications that don't have a request context.
func Tz(key string, args ...any) string {
	return T(defaultLang, key, args...)
}

// LogKind constants — stored as English enum values, translated at display time.
const (
	KindOperation = "operation"
	KindSystem    = "system"
	KindPlugin    = "plugin"
	KindTerminal  = "terminal" // 终端会话中用户输入的每条命令（终端审计日志）
)

// TranslateLogKind converts an internal LogEntry.Kind to a display string.
func TranslateLogKind(kind, lang string) string {
	switch kind {
	case KindOperation, "操作":
		return T(lang, "log.kind.operation")
	case KindSystem, "系统":
		return T(lang, "log.kind.system")
	case KindPlugin, "插件":
		return T(lang, "log.kind.plugin")
	default:
		return kind
	}
}
