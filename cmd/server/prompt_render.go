package main

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed prompts/*.md
var embeddedPromptsFS embed.FS

// promptVars 是模板渲染时的占位符值。
type promptVars map[string]string

// promptStore 提供系统提示词的模板化加载 + 部署级覆盖。默认从 go:embed 读内置模板；
// 若 AIConfig.PromptOverridesDir 指向的目录存在同名 .md，则优先读部署目录（私有化客户
// 可改提示词而无需重编译）。渲染结果带 prompt_version（模板内容哈希），供成本账本溯源。
type promptStore struct {
	mu      sync.RWMutex
	dir     string
}

var defaultPromptStore = &promptStore{}

// SetOverrideDir 设置部署级覆盖目录（空=仅用内嵌模板）。会立即重算版本指纹。
func (ps *promptStore) SetOverrideDir(dir string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.dir = dir
}

// loadTemplate 返回 (模板内容, 版本指纹, error)。优先部署目录，其次内嵌。
func (ps *promptStore) loadTemplate(name string) (string, string, error) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.dir != "" {
		p := filepath.Join(ps.dir, name+".md")
		if b, err := os.ReadFile(p); err == nil {
			return string(b), hashVersion(string(b)), nil
		}
	}
	b, err := embeddedPromptsFS.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", "", err
	}
	return string(b), hashVersion(string(b)), nil
}

// render 渲染一个命名模板：替换 {{key}} 占位符，返回渲染文本与版本指纹。
// 模板不存在时返回 err，调用方决定回退到内联字符串。
func render(name string, vars promptVars) (string, string, error) {
	tpl, ver, err := defaultPromptStore.loadTemplate(name)
	if err != nil {
		return "", "", err
	}
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, "{{"+k+"}}", v)
	}
	return tpl, ver, nil
}

// hashVersion 返回模板内容的确定性短哈希（供 prompt_version 字段）。
func hashVersion(s string) string {
	const f = "0123456789abcdef"
	// FNV-1a 64，简单且无外部依赖（prompt 版本无需密码学强度）。
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	out := make([]byte, 12)
	for i := 0; i < 6; i++ {
		out[i*2] = f[h&0xF]
		h >>= 4
		out[i*2+1] = f[h&0xF]
		h >>= 4
	}
	return string(out)
}

// promptVersionFor 返回某命名模板的当前版本指纹（供 ai_call_events.prompt_version）。
// 模板缺失时返回 ""。
func promptVersionFor(name string) string {
	_, ver, err := defaultPromptStore.loadTemplate(name)
	if err != nil {
		return ""
	}
	return ver
}
