package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ============================================================================
// OpenAPI / Swagger / Knife4j 文档 URL 自动拉取
//
// 用户常粘贴 doc.html#/home（Knife4j UI），而不是 /v3/api-docs JSON。
// 本模块从文档页 URL 推断 origin，探测常见规范端点与 swagger-resources 分组，
// 返回可导入的 JSON 与建议基址 / 系统名。
// ============================================================================

const openAPIFetchMaxBytes = 8 << 20 // 8 MiB

type openAPIGroup struct {
	Name        string `json:"name"`
	URL         string `json:"url"` // absolute URL to the group spec
	ContextPath string `json:"context_path,omitempty"`
}

type openAPIFetchResult struct {
	Spec          string         `json:"spec"`
	Groups        []openAPIGroup `json:"groups,omitempty"`
	SelectedGroup string         `json:"selected_group,omitempty"`
	SuggestedName string         `json:"suggested_name,omitempty"`
	SuggestedBase string         `json:"suggested_base,omitempty"`
	SourceURL     string         `json:"source_url"`
	Notes         []string       `json:"notes,omitempty"`
}

type swaggerResource struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Location       string `json:"location"`
	SwaggerVersion string `json:"swaggerVersion"`
}

type springdocConfig struct {
	URL  string `json:"url"`
	URLs []struct {
		URL         string `json:"url"`
		Name        string `json:"name"`
		ContextPath string `json:"contextPath"`
	} `json:"urls"`
}

// normalizeOpenAPIDocURL strips UI fragments (#/home) and returns:
//   - root: scheme://host[:port][/context]  (context = path before doc.html / index.html)
//   - groupHint: from hash like #/SwaggerModels/activiti → "activiti"
func normalizeOpenAPIDocURL(raw string) (root string, groupHint string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("文档地址为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("文档地址无效")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("仅支持 http/https")
	}
	frag := strings.Trim(u.Fragment, "/")
	if frag != "" {
		parts := strings.Split(frag, "/")
		last := parts[len(parts)-1]
		low := strings.ToLower(last)
		if last != "" && low != "home" && low != "default" && low != "swagger-ui" && !strings.EqualFold(last, "SwaggerModels") {
			groupHint = last
		}
		// #/SwaggerModels/xxx
		for i, p := range parts {
			if strings.EqualFold(p, "SwaggerModels") && i+1 < len(parts) && parts[i+1] != "" {
				groupHint = parts[i+1]
			}
		}
	}
	path := u.Path
	lowPath := strings.ToLower(path)
	for _, marker := range []string{"/doc.html", "/swagger-ui.html", "/swagger-ui/", "/swagger-ui/index.html", "/index.html"} {
		if idx := strings.Index(lowPath, marker); idx >= 0 {
			path = path[:idx]
			break
		}
	}
	path = strings.TrimRight(path, "/")
	root = u.Scheme + "://" + u.Host + path
	return root, groupHint, nil
}

func absURL(root, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	base, err := url.Parse(root + "/")
	if err != nil {
		return root + ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return root + "/" + strings.TrimLeft(ref, "/")
	}
	return base.ResolveReference(u).String()
}

func looksLikeOpenAPIJSON(b []byte) bool {
	s := strings.TrimSpace(string(b))
	if s == "" || s[0] != '{' {
		return false
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(b, &probe) != nil {
		return false
	}
	if _, ok := probe["openapi"]; ok {
		return true
	}
	if _, ok := probe["swagger"]; ok {
		return true
	}
	if _, ok := probe["paths"]; ok {
		return true
	}
	return false
}

func httpGetBytes(client *http.Client, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json, application/vnd.oai.openapi+json, */*")
	req.Header.Set("User-Agent", "aiops-monitor-openapi-fetch/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, openAPIFetchMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > openAPIFetchMaxBytes {
		return nil, "", fmt.Errorf("响应过大（>%d bytes）", openAPIFetchMaxBytes)
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, ct, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, ct, nil
}

func parseSwaggerResources(root string, body []byte) []openAPIGroup {
	var items []swaggerResource
	if json.Unmarshal(body, &items) != nil {
		return nil
	}
	var out []openAPIGroup
	for _, it := range items {
		loc := strings.TrimSpace(it.Location)
		if loc == "" {
			loc = strings.TrimSpace(it.URL)
		}
		if loc == "" {
			continue
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			name = loc
		}
		out = append(out, openAPIGroup{Name: name, URL: absURL(root, loc)})
	}
	return out
}

func parseSpringdocConfig(root string, body []byte) []openAPIGroup {
	var cfg springdocConfig
	if json.Unmarshal(body, &cfg) != nil {
		return nil
	}
	var out []openAPIGroup
	if len(cfg.URLs) > 0 {
		for _, u := range cfg.URLs {
			if strings.TrimSpace(u.URL) == "" {
				continue
			}
			name := u.Name
			if name == "" {
				name = u.URL
			}
			cp := strings.TrimSpace(u.ContextPath)
			if cp != "" && !strings.HasPrefix(cp, "/") {
				cp = "/" + cp
			}
			out = append(out, openAPIGroup{Name: name, URL: absURL(root, u.URL), ContextPath: cp})
		}
		return out
	}
	if strings.TrimSpace(cfg.URL) != "" {
		out = append(out, openAPIGroup{Name: "default", URL: absURL(root, cfg.URL)})
	}
	return out
}

func suggestNameFromSpec(spec []byte) string {
	var info struct {
		Info struct {
			Title string `json:"title"`
		} `json:"info"`
	}
	_ = json.Unmarshal(spec, &info)
	return strings.TrimSpace(info.Info.Title)
}

func suggestBaseFromRootAndSpec(root string, spec []byte, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimRight(strings.TrimSpace(override), "/")
	}
	// Prefer user document origin as API base (Knife4j apps usually share host).
	base := strings.TrimRight(root, "/")
	var doc openAPISpec
	if json.Unmarshal(spec, &doc) == nil {
		if len(doc.Servers) > 0 && strings.TrimSpace(doc.Servers[0].URL) != "" {
			su := strings.TrimSpace(doc.Servers[0].URL)
			if strings.HasPrefix(su, "http://") || strings.HasPrefix(su, "https://") {
				return strings.TrimRight(su, "/")
			}
			// relative server url → join with root
			return strings.TrimRight(absURL(root, su), "/")
		}
		if doc.Host != "" {
			scheme := "http"
			if len(doc.Schemes) > 0 {
				scheme = doc.Schemes[0]
			}
			return strings.TrimRight(scheme+"://"+doc.Host+doc.BasePath, "/")
		}
	}
	return base
}

func suggestBaseForGroup(root string, g openAPIGroup, spec []byte, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimRight(strings.TrimSpace(override), "/")
	}
	if cp := strings.TrimSpace(g.ContextPath); cp != "" {
		return strings.TrimRight(root+cp, "/")
	}
	// Infer from group URL: http://host/activiti/v3/api-docs → http://host/activiti
	// Do NOT match bare /v2/api-docs or /v3/api-docs (i==0); and never treat
	// "/v2/api-docs" as context "/v2" via a shorter "/api-docs" suffix.
	if u, err := url.Parse(g.URL); err == nil {
		p := u.Path
		low := strings.ToLower(p)
		for _, suf := range []string{"/v3/api-docs", "/v2/api-docs"} {
			if i := strings.Index(low, suf); i > 0 {
				return strings.TrimRight(u.Scheme+"://"+u.Host+p[:i], "/")
			}
		}
		if i := strings.Index(low, "/api-docs"); i > 0 {
			before := low[:i]
			if !strings.HasSuffix(before, "/v2") && !strings.HasSuffix(before, "/v3") {
				return strings.TrimRight(u.Scheme+"://"+u.Host+p[:i], "/")
			}
		}
	}
	return suggestBaseFromRootAndSpec(root, spec, override)
}

func pickGroup(groups []openAPIGroup, hint string) openAPIGroup {
	if len(groups) == 0 {
		return openAPIGroup{}
	}
	hint = strings.TrimSpace(hint)
	if hint != "" {
		for _, g := range groups {
			if strings.EqualFold(g.Name, hint) {
				return g
			}
		}
		for _, g := range groups {
			if strings.Contains(strings.ToLower(g.Name), strings.ToLower(hint)) ||
				strings.Contains(strings.ToLower(g.URL), strings.ToLower(hint)) {
				return g
			}
		}
	}
	return groups[0]
}

// fetchOpenAPIFromDocURL discovers and downloads OpenAPI/Swagger JSON from a
// doc.html / Knife4j / swagger-ui / direct api-docs URL.
func fetchOpenAPIFromDocURL(rawURL, groupHint, baseOverride string) (*openAPIFetchResult, error) {
	root, hashHint, err := normalizeOpenAPIDocURL(rawURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(groupHint) == "" {
		groupHint = hashHint
	}
	client := newGuardedHTTPClient(20 * time.Second)
	notes := []string{}

	// Direct URL already points at a JSON spec.
	if body, _, err := httpGetBytes(client, strings.TrimSpace(rawURL)); err == nil && looksLikeOpenAPIJSON(body) {
		res := &openAPIFetchResult{
			Spec:          string(body),
			SuggestedName: suggestNameFromSpec(body),
			SuggestedBase: suggestBaseFromRootAndSpec(root, body, baseOverride),
			SourceURL:     strings.TrimSpace(rawURL),
			Notes:         []string{"文档地址本身即为 OpenAPI/Swagger JSON"},
		}
		return res, nil
	}

	var groups []openAPIGroup
	// 1) Knife4j / Springfox resources
	if body, _, err := httpGetBytes(client, root+"/swagger-resources"); err == nil {
		if g := parseSwaggerResources(root, body); len(g) > 0 {
			groups = append(groups, g...)
			notes = append(notes, "已发现 swagger-resources 分组 "+fmt.Sprintf("%d", len(g))+" 个")
		}
	}
	// 2) springdoc swagger-config
	if body, _, err := httpGetBytes(client, root+"/v3/api-docs/swagger-config"); err == nil {
		if g := parseSpringdocConfig(root, body); len(g) > 0 {
			groups = append(groups, g...)
			notes = append(notes, "已发现 springdoc swagger-config")
		}
	}

	// Deduplicate groups by URL
	if len(groups) > 0 {
		seen := map[string]bool{}
		uniq := groups[:0]
		for _, g := range groups {
			if g.URL == "" || seen[g.URL] {
				continue
			}
			seen[g.URL] = true
			uniq = append(uniq, g)
		}
		groups = uniq
	}

	candidates := []string{
		root + "/v3/api-docs",
		root + "/v2/api-docs",
		root + "/api-docs",
		root + "/swagger.json",
		root + "/openapi.json",
		root + "/v3/api-docs/default",
	}
	if groupHint != "" {
		q := url.QueryEscape(groupHint)
		candidates = append([]string{
			root + "/v3/api-docs/" + groupHint,
			root + "/v3/api-docs?group=" + q,
			root + "/v2/api-docs?group=" + q,
		}, candidates...)
	}

	tryFetch := func(u string) ([]byte, bool) {
		body, _, err := httpGetBytes(client, u)
		if err != nil || !looksLikeOpenAPIJSON(body) {
			return nil, false
		}
		return body, true
	}

	if len(groups) > 0 {
		sel := pickGroup(groups, groupHint)
		if body, ok := tryFetch(sel.URL); ok {
			notes = append(notes, "已拉取分组「"+sel.Name+"」")
			return &openAPIFetchResult{
				Spec:          string(body),
				Groups:        groups,
				SelectedGroup: sel.Name,
				SuggestedName: openAPICoalesce(suggestNameFromSpec(body), sel.Name),
				SuggestedBase: suggestBaseForGroup(root, sel, body, baseOverride),
				SourceURL:     sel.URL,
				Notes:         notes,
			}, nil
		}
		// try all groups until one works
		for _, g := range groups {
			if body, ok := tryFetch(g.URL); ok {
				notes = append(notes, "已拉取分组「"+g.Name+"」")
				return &openAPIFetchResult{
					Spec:          string(body),
					Groups:        groups,
					SelectedGroup: g.Name,
					SuggestedName: openAPICoalesce(suggestNameFromSpec(body), g.Name),
					SuggestedBase: suggestBaseForGroup(root, g, body, baseOverride),
					SourceURL:     g.URL,
					Notes:         notes,
				}, nil
			}
		}
		return &openAPIFetchResult{Groups: groups, Notes: notes}, fmt.Errorf("已发现 %d 个分组，但均无法拉取到有效 OpenAPI JSON；请在分组中选择后重试", len(groups))
	}

	var lastErr error
	for _, u := range candidates {
		body, ok := tryFetch(u)
		if !ok {
			lastErr = fmt.Errorf("候选 %s 无效", u)
			continue
		}
		notes = append(notes, "已从 "+u+" 拉取规范")
		return &openAPIFetchResult{
			Spec:          string(body),
			SuggestedName: suggestNameFromSpec(body),
			SuggestedBase: suggestBaseFromRootAndSpec(root, body, baseOverride),
			SourceURL:     u,
			Notes:         notes,
		}, nil
	}

	msg := "无法从文档地址发现 OpenAPI/Swagger JSON。请确认可访问 /swagger-resources、/v3/api-docs 或 /v2/api-docs"
	if lastErr != nil {
		msg += "（" + lastErr.Error() + "）"
	}
	return nil, fmt.Errorf("%s；文档根：%s", msg, root)
}

func openAPICoalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}
