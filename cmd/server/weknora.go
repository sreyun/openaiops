package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// WeKnora 外部文档 RAG
//
// 复杂运维文档（手册/Wiki/PDF）由 WeKnora 独立维护；本平台仅通过 API URL + API Key
// 调用 knowledge-search / hybrid-search，把命中片段作为 Sreyun 工具 search_knowledge 注入诊断/对话。
//
// 重要：官方 knowledge-search 在未传 knowledge_base_id(s) 时往往无法覆盖全部可见库。
// 因此当配置留空时，先 GET /knowledge-bases 枚举可见库，再以 knowledge_base_ids 检索；
// 多库批量失败或结果偏少时，再按库并行 fan-out（knowledge-search → hybrid-search）。
// 认证头：X-API-Key。
// ============================================================================

const (
	weknoraSearchPath   = "/knowledge-search"
	weknoraListKBPath   = "/knowledge-bases"
	weknoraKBCacheTTL   = 5 * time.Minute
	weknoraFanOutMax    = 8 // 并行检索的最大并发
	weknoraFanOutThresh = 2 // 批量结果少于此数时尝试 fan-out 补全
)

// weknoraConfigured 判断是否已填写可用的 WeKnora 对接参数（开关 + URL + Key）。
func weknoraConfigured(cfg AIConfig) bool {
	return cfg.WeKnoraEnabled &&
		strings.TrimSpace(cfg.WeKnoraURL) != "" &&
		strings.TrimSpace(cfg.WeKnoraAPIKey) != ""
}

// normalizeWeKnoraBaseURL 归一化用户填写的 API 根地址为 …/api/v1。
// 接受 http://host:8080 或 http://host:8080/api/v1（尾斜杠可有可无）。
func normalizeWeKnoraBaseURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if u == "" {
		return ""
	}
	if strings.HasSuffix(u, "/api/v1") {
		return u
	}
	return u + "/api/v1"
}

// parseWeKnoraKBIDs 解析逗号/空白分隔的知识库 ID 列表。
func parseWeKnoraKBIDs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// weknoraKBInfo 可见知识库摘要。
type weknoraKBInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	Temporary  bool   `json:"is_temporary,omitempty"`
	KnowledgeN int    `json:"knowledge_count,omitempty"`
	ChunkCount int    `json:"chunk_count,omitempty"`
}

type weknoraKBCacheEntry struct {
	at  time.Time
	kbs []weknoraKBInfo
}

var (
	weknoraKBCacheMu sync.Mutex
	weknoraKBCache   = map[string]weknoraKBCacheEntry{}
)

func weknoraCacheKey(base, apiKey string) string {
	// 不落明文 Key，仅用长度+前后缀做隔离（同 URL 不同租户 Key）
	k := strings.TrimSpace(apiKey)
	prefix, suffix := k, ""
	if len(k) > 8 {
		prefix = k[:4]
		suffix = k[len(k)-4:]
	}
	return base + "|" + fmt.Sprintf("%d:%s…%s", len(k), prefix, suffix)
}

// weknoraChunk 是 knowledge-search 返回的一条命中（兼容 data[] / chunks[] 字段名差异）。
type weknoraChunk struct {
	ID                string  `json:"id"`
	Content           string  `json:"content"`
	KnowledgeID       string  `json:"knowledge_id"`
	KnowledgeTitle    string  `json:"knowledge_title"`
	KnowledgeFilename string  `json:"knowledge_filename"`
	Score             float64 `json:"score"`
	ChunkIndex        int     `json:"chunk_index"`
	SourceDoc         string  `json:"source_doc"` // 部分实现别名
	KnowledgeBaseID   string  `json:"knowledge_base_id,omitempty"`
}

// weknoraSearchReq 对应 POST /knowledge-search 请求体。
type weknoraSearchReq struct {
	Query            string   `json:"query"`
	KnowledgeBaseID  string   `json:"knowledge_base_id,omitempty"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids,omitempty"`
	KnowledgeIDs     []string `json:"knowledge_ids,omitempty"`
	TopK             int      `json:"top_k,omitempty"`
	MatchCount       int      `json:"match_count,omitempty"` // 部分版本用 match_count
}

// weknoraSearchMeta 描述一次检索实际覆盖的范围（供测试/调试）。
type weknoraSearchMeta struct {
	KBCount    int      `json:"kb_count"`
	KBIDs      []string `json:"kb_ids,omitempty"`
	AutoListed bool     `json:"auto_listed"`
	Strategy   string   `json:"strategy,omitempty"` // batch | fanout | hybrid | legacy
	HitCount   int      `json:"hit_count"`
}

// weknoraSearch 调用 WeKnora 知识搜索，返回格式化文本（供工具/测试使用）。
func weknoraSearch(cfg AIConfig, query string, topK int, overrideKBIDs []string) (string, error) {
	chunks, _, err := weknoraSearchChunksMeta(cfg, query, topK, overrideKBIDs)
	if err != nil {
		markWeKnoraFail(err)
		return "", err
	}
	markWeKnoraOK()
	return formatWeKnoraChunks(chunks), nil
}

func weknoraSearchChunks(cfg AIConfig, query string, topK int, overrideKBIDs []string) ([]weknoraChunk, error) {
	chunks, _, err := weknoraSearchChunksMeta(cfg, query, topK, overrideKBIDs)
	return chunks, err
}

func weknoraSearchChunksMeta(cfg AIConfig, query string, topK int, overrideKBIDs []string) ([]weknoraChunk, weknoraSearchMeta, error) {
	meta := weknoraSearchMeta{}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, meta, fmt.Errorf("查询不能为空")
	}
	base := normalizeWeKnoraBaseURL(cfg.WeKnoraURL)
	key := strings.TrimSpace(cfg.WeKnoraAPIKey)
	if base == "" || key == "" {
		return nil, meta, fmt.Errorf("未配置 WeKnora URL 或 API Key")
	}
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}

	kbIDs := overrideKBIDs
	if len(kbIDs) == 0 {
		kbIDs = parseWeKnoraKBIDs(cfg.WeKnoraKnowledgeBaseIDs)
	}
	if len(kbIDs) == 0 {
		listed, err := weknoraListKnowledgeBases(base, key, false)
		if err != nil {
			return nil, meta, fmt.Errorf("未配置知识库 ID，且拉取可见库失败: %w（可手动填写知识库 ID，或检查 API Key 权限）", err)
		}
		kbIDs = weknoraKBInfoIDs(listed)
		meta.AutoListed = true
		if len(kbIDs) == 0 {
			return nil, meta, fmt.Errorf("当前 API Key 下没有可见知识库；请在 WeKnora 创建文档库或填写限定知识库 ID")
		}
	}
	meta.KBIDs = append([]string(nil), kbIDs...)
	meta.KBCount = len(kbIDs)

	client := newGuardedHTTPClient(25 * time.Second)

	// 1) 优先批量 knowledge-search（官方多库参数）
	chunks, err := weknoraDoKnowledgeSearch(client, base, key, query, topK, kbIDs)
	if err == nil && len(chunks) >= weknoraFanOutThresh {
		meta.Strategy = "batch"
		meta.HitCount = len(chunks)
		return truncateWeKnoraChunks(chunks, topK), meta, nil
	}
	batchErr := err
	batchHits := len(chunks)

	// 2) 批量失败、或命中过少：按库 fan-out（knowledge-search → hybrid-search）
	if len(kbIDs) >= 1 {
		fanHits, fanErrs := weknoraFanOutSearch(client, base, key, query, topK, kbIDs)
		merged := mergeWeKnoraChunks(chunks, fanHits)
		if len(merged) > 0 {
			meta.Strategy = "fanout"
			if batchHits == 0 && len(fanHits) > 0 {
				meta.Strategy = "fanout"
			}
			meta.HitCount = len(merged)
			return truncateWeKnoraChunks(merged, topK), meta, nil
		}
		if batchErr != nil && len(fanErrs) > 0 {
			return nil, meta, fmt.Errorf("WeKnora 批量检索失败: %v；分库回退亦失败: %s", batchErr, strings.Join(fanErrs, "; "))
		}
		if batchErr != nil {
			return nil, meta, batchErr
		}
	}

	// 3) 极端兜底：无 KB 范围再试一次（兼容旧部署）
	if batchErr != nil {
		legacy, lerr := weknoraDoKnowledgeSearch(client, base, key, query, topK, nil)
		if lerr == nil {
			meta.Strategy = "legacy"
			meta.HitCount = len(legacy)
			return truncateWeKnoraChunks(legacy, topK), meta, nil
		}
		return nil, meta, batchErr
	}

	meta.Strategy = "batch"
	meta.HitCount = len(chunks)
	return truncateWeKnoraChunks(chunks, topK), meta, nil
}

func weknoraKBInfoIDs(kbs []weknoraKBInfo) []string {
	out := make([]string, 0, len(kbs))
	for _, kb := range kbs {
		if id := strings.TrimSpace(kb.ID); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// weknoraListKnowledgeBases 拉取当前 Key 可见的知识库列表（可走短缓存）。
func weknoraListKnowledgeBases(base, apiKey string, bypassCache bool) ([]weknoraKBInfo, error) {
	base = normalizeWeKnoraBaseURL(base)
	apiKey = strings.TrimSpace(apiKey)
	if base == "" || apiKey == "" {
		return nil, fmt.Errorf("未配置 WeKnora URL 或 API Key")
	}
	ck := weknoraCacheKey(base, apiKey)
	if !bypassCache {
		weknoraKBCacheMu.Lock()
		if ent, ok := weknoraKBCache[ck]; ok && time.Since(ent.at) < weknoraKBCacheTTL {
			out := append([]weknoraKBInfo(nil), ent.kbs...)
			weknoraKBCacheMu.Unlock()
			return out, nil
		}
		weknoraKBCacheMu.Unlock()
	}

	req, err := http.NewRequest(http.MethodGet, base+weknoraListKBPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := newGuardedHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取知识库列表失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("拉取知识库列表 HTTP %d: %s", resp.StatusCode, trimLine(string(raw), 200))
	}
	kbs, err := parseWeKnoraKBListResponse(raw)
	if err != nil {
		return nil, err
	}
	// 过滤临时库；保留有文档/切片或未知计数的库
	filtered := make([]weknoraKBInfo, 0, len(kbs))
	for _, kb := range kbs {
		if kb.Temporary || strings.TrimSpace(kb.ID) == "" {
			continue
		}
		filtered = append(filtered, kb)
	}
	weknoraKBCacheMu.Lock()
	weknoraKBCache[ck] = weknoraKBCacheEntry{at: time.Now(), kbs: filtered}
	weknoraKBCacheMu.Unlock()
	return append([]weknoraKBInfo(nil), filtered...), nil
}

func parseWeKnoraKBListResponse(raw []byte) ([]weknoraKBInfo, error) {
	// 官方: {success,data:[{id,name,...}]}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析知识库列表失败: %w", err)
	}
	if env.Error != "" {
		return nil, fmt.Errorf("WeKnora: %s", env.Error)
	}
	if len(env.Data) == 0 {
		// 兼容直接返回数组
		var arr []weknoraKBInfo
		if err := json.Unmarshal(raw, &arr); err == nil {
			return arr, nil
		}
		return nil, nil
	}
	var arr []weknoraKBInfo
	if err := json.Unmarshal(env.Data, &arr); err == nil {
		return arr, nil
	}
	// 兼容 {data:{items:[]}} / {data:{list:[]}} / {data:{knowledge_bases:[]}}
	var nested struct {
		Items          []weknoraKBInfo `json:"items"`
		List           []weknoraKBInfo `json:"list"`
		KnowledgeBases []weknoraKBInfo `json:"knowledge_bases"`
		Data           []weknoraKBInfo `json:"data"`
	}
	if err := json.Unmarshal(env.Data, &nested); err != nil {
		return nil, fmt.Errorf("解析知识库列表 data 失败: %w", err)
	}
	switch {
	case len(nested.Items) > 0:
		return nested.Items, nil
	case len(nested.List) > 0:
		return nested.List, nil
	case len(nested.KnowledgeBases) > 0:
		return nested.KnowledgeBases, nil
	case len(nested.Data) > 0:
		return nested.Data, nil
	}
	if env.Message != "" {
		return nil, fmt.Errorf("WeKnora: %s", env.Message)
	}
	return nil, nil
}

func weknoraDoKnowledgeSearch(client *http.Client, base, apiKey, query string, topK int, kbIDs []string) ([]weknoraChunk, error) {
	body := weknoraSearchReq{Query: query, TopK: topK, MatchCount: topK}
	switch {
	case len(kbIDs) == 1:
		// 单库同时填两种字段，兼容只认其一的部署
		body.KnowledgeBaseID = kbIDs[0]
		body.KnowledgeBaseIDs = kbIDs
	case len(kbIDs) > 1:
		body.KnowledgeBaseIDs = kbIDs
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, base+weknoraSearchPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WeKnora 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("WeKnora HTTP %d: %s", resp.StatusCode, trimLine(string(raw), 200))
	}
	return parseWeKnoraSearchResponse(raw)
}

func weknoraFanOutSearch(client *http.Client, base, apiKey, query string, topK int, kbIDs []string) ([]weknoraChunk, []string) {
	type result struct {
		chunks []weknoraChunk
		err    string
	}
	sem := make(chan struct{}, weknoraFanOutMax)
	outCh := make(chan result, len(kbIDs))
	var wg sync.WaitGroup
	perKB := topK
	if perKB < 3 {
		perKB = 3
	}
	for _, id := range kbIDs {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			chunks, err := weknoraDoKnowledgeSearch(client, base, apiKey, query, perKB, []string{id})
			if err == nil && len(chunks) > 0 {
				for i := range chunks {
					if chunks[i].KnowledgeBaseID == "" {
						chunks[i].KnowledgeBaseID = id
					}
				}
				outCh <- result{chunks: chunks}
				return
			}
			// hybrid-search 回退
			hchunks, herr := weknoraDoHybridSearch(client, base, apiKey, id, query, perKB)
			if herr == nil && len(hchunks) > 0 {
				for i := range hchunks {
					if hchunks[i].KnowledgeBaseID == "" {
						hchunks[i].KnowledgeBaseID = id
					}
				}
				outCh <- result{chunks: hchunks}
				return
			}
			msg := ""
			if err != nil {
				msg = id + ": " + err.Error()
			}
			if herr != nil {
				if msg != "" {
					msg += " / hybrid: " + herr.Error()
				} else {
					msg = id + " hybrid: " + herr.Error()
				}
			}
			if msg != "" {
				outCh <- result{err: msg}
			}
		}()
	}
	wg.Wait()
	close(outCh)
	var all []weknoraChunk
	var errs []string
	for r := range outCh {
		if len(r.chunks) > 0 {
			all = append(all, r.chunks...)
		}
		if r.err != "" {
			errs = append(errs, r.err)
		}
	}
	return all, errs
}

// weknoraDoHybridSearch 调用 GET /knowledge-bases/:id/hybrid-search（文档要求 GET + JSON body）。
func weknoraDoHybridSearch(client *http.Client, base, apiKey, kbID, query string, topK int) ([]weknoraChunk, error) {
	kbID = strings.TrimSpace(kbID)
	if kbID == "" {
		return nil, fmt.Errorf("空知识库 ID")
	}
	payload, _ := json.Marshal(map[string]any{
		"query_text":        query,
		"match_count":       topK,
		"vector_threshold":  0.3,
		"keyword_threshold": 0.3,
	})
	url := base + "/knowledge-bases/" + kbID + "/hybrid-search"
	do := func(method string) ([]weknoraChunk, error) {
		req, err := http.NewRequest(method, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimLine(string(raw), 160))
		}
		return parseWeKnoraSearchResponse(raw)
	}
	chunks, err := do(http.MethodGet)
	if err == nil {
		return chunks, nil
	}
	// 部分网关/反向代理禁止 GET body，再试 POST
	chunks2, err2 := do(http.MethodPost)
	if err2 == nil {
		return chunks2, nil
	}
	return nil, fmt.Errorf("%v; POST: %v", err, err2)
}

func mergeWeKnoraChunks(parts ...[]weknoraChunk) []weknoraChunk {
	seen := map[string]bool{}
	var out []weknoraChunk
	for _, list := range parts {
		for _, c := range list {
			key := strings.TrimSpace(c.ID)
			if key == "" {
				key = strings.TrimSpace(c.KnowledgeID) + "|" + trimLine(c.Content, 80)
			}
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func truncateWeKnoraChunks(chunks []weknoraChunk, topK int) []weknoraChunk {
	if topK <= 0 || len(chunks) <= topK {
		return chunks
	}
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].Score > chunks[j].Score })
	return chunks[:topK]
}

// parseWeKnoraSearchResponse 兼容官方 {success,data:[]} 与社区 {chunks:[]} / {results:[]}。
func parseWeKnoraSearchResponse(raw []byte) ([]weknoraChunk, error) {
	var envelope struct {
		Success bool           `json:"success"`
		Data    []weknoraChunk `json:"data"`
		Chunks  []weknoraChunk `json:"chunks"`
		Results []weknoraChunk `json:"results"`
		Total   int            `json:"total"`
		Error   string         `json:"error"`
		Message string         `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("解析 WeKnora 响应失败: %w", err)
	}
	if envelope.Error != "" || (envelope.Message != "" && len(envelope.Data) == 0 && len(envelope.Chunks) == 0 && len(envelope.Results) == 0) {
		msg := envelope.Error
		if msg == "" {
			msg = envelope.Message
		}
		return nil, fmt.Errorf("WeKnora: %s", msg)
	}
	switch {
	case len(envelope.Data) > 0:
		return envelope.Data, nil
	case len(envelope.Chunks) > 0:
		return envelope.Chunks, nil
	case len(envelope.Results) > 0:
		return envelope.Results, nil
	}
	return nil, nil
}

func formatWeKnoraChunks(chunks []weknoraChunk) string {
	if len(chunks) == 0 {
		return "未在 WeKnora 知识库中找到相关文档片段"
	}
	var b strings.Builder
	b.WriteString("WeKnora 知识库检索结果：\n")
	for i, c := range chunks {
		title := strings.TrimSpace(c.KnowledgeTitle)
		if title == "" {
			title = strings.TrimSpace(c.KnowledgeFilename)
		}
		if title == "" {
			title = strings.TrimSpace(c.SourceDoc)
		}
		if title == "" {
			title = "未命名文档"
		}
		score := ""
		if c.Score > 0 {
			score = fmt.Sprintf(" 相关度 %.2f", c.Score)
		}
		kbHint := ""
		if id := strings.TrimSpace(c.KnowledgeBaseID); id != "" {
			kbHint = fmt.Sprintf(" [库:%s]", trimLine(id, 36))
		}
		fmt.Fprintf(&b, "  %d. 【%s】%s%s\n     %s\n", i+1, title, score, kbHint, trimLine(c.Content, 400))
	}
	return b.String()
}
