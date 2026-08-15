package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// vmHistoryCache memoizes the VictoriaMetrics half of a host-history read.
//
// 为什么值得缓存：每打开一次主机详情、每一次刷新、每个 AI/预测/SLO 读取，都会对 VM 发一条
//
//	{__name__=~"<57 个指标名>",host="…",path!~".*(overlay2|/docker/|/kubelet/pods|containerd).*"}
//
// 的 query_range。这个选择器没有任何精确的 __name__，VM 必须扫元数据索引，再把每个磁盘
// 分区、每块 GPU、每个 (proto,state) 展开成独立序列——24h/7d 窗口下是几百毫秒到数秒级的
// 查询。前端每次刷新原样重来一遍，这就是"加载比较缓慢"。
//
// 为什么缓存不会让曲线变旧：loadDurableHostHistory 永远把内存环最近 15 分钟叠在 VM 结果
// 之上（recentHistoryTail + spliceHistory）。缓存只覆盖 VM 那一半，最新的点始终来自内存，
// 所以只要 TTL 远小于 memHistoryOverlaySec，用户看到的尾部就一直是实时的。
// 不完整窗口（覆盖不足）不会进缓存，见 vmHistoryCacheable。
type vmHistoryCache struct {
	mu      sync.Mutex
	entries map[string]vmHistoryCacheEntry
}

type vmHistoryCacheEntry struct {
	samples []shared.Sample
	expires time.Time
}

const (
	// Entries are per (host, window, step). A few hundred hosts browsing at once
	// still fits, and eviction is oldest-expiry-first.
	vmHistoryCacheMaxEntries = 256
	vmHistoryCacheMinTTL     = 15 * time.Second
	// Hard ceiling well under memHistoryOverlaySec (15 min), so the live RAM
	// overlay always covers whatever the cached VM half is missing.
	vmHistoryCacheMaxTTL = 120 * time.Second
)

func newVMHistoryCache() *vmHistoryCache {
	return &vmHistoryCache{entries: map[string]vmHistoryCacheEntry{}}
}

// vmHistoryCacheTTL ties freshness to the query's own resolution: a 7d chart is
// drawn at 1260s per point, so re-querying it every few seconds buys nothing.
func vmHistoryCacheTTL(step int64) time.Duration {
	ttl := time.Duration(step) * time.Second
	if ttl < vmHistoryCacheMinTTL {
		return vmHistoryCacheMinTTL
	}
	if ttl > vmHistoryCacheMaxTTL {
		return vmHistoryCacheMaxTTL
	}
	return ttl
}

// vmHistoryCacheKey snaps from/to onto a bucket the size of the TTL.
//
// 必须同时对 from 和 to 取整：相对窗口（from=now-24h, to=now）两端每秒都在变，不取整的话
// 每次请求都是新键，缓存命中率恒为零。取整带来的偏移最多一个 TTL（≤120s），而这段永远落在
// 内存叠加窗口里，对图形不可见。
func vmHistoryCacheKey(hostID string, from, to, step int64, names []string) string {
	bucket := int64(vmHistoryCacheTTL(step) / time.Second)
	if bucket < 1 {
		bucket = 1
	}
	var b strings.Builder
	b.WriteString(hostID)
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(from/bucket, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(to/bucket, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(step, 10))
	b.WriteByte('|')
	b.WriteString(strings.Join(names, ","))
	return b.String()
}

func (c *vmHistoryCache) get(key string) ([]shared.Sample, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	// Hand out a copy of the slice header's contents: nothing mutates these
	// today (stabilizeSampleArrays runs once at construction, downsampleSamples
	// only selects), but a cached slice that a caller appends to would corrupt
	// every later reader.
	out := make([]shared.Sample, len(e.samples))
	copy(out, e.samples)
	return out, true
}

// vmHistoryCacheable is whether a VM result is complete enough to memoize.
// After a VM restart the first query_range often returns only the samples
// ingested since boot; caching that short slice made every chart freeze on
// "two minutes of data" for up to 120s. Reuse historyCoversWindow so a series
// that starts hours late is treated the same as an empty one.
func vmHistoryCacheable(samples []shared.Sample, from, to int64) bool {
	return historyCoversWindow(samples, from, to)
}

func (c *vmHistoryCache) put(key string, samples []shared.Sample, ttl time.Duration) {
	if c == nil || len(samples) == 0 || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= vmHistoryCacheMaxEntries {
		c.evictLocked()
	}
	c.entries[key] = vmHistoryCacheEntry{samples: samples, expires: time.Now().Add(ttl)}
}

// evictLocked drops everything already expired; if that frees nothing, it drops
// the entry closest to expiry so put() always has room.
func (c *vmHistoryCache) evictLocked() {
	now := time.Now()
	freed := false
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
			freed = true
		}
	}
	if freed {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, e := range c.entries {
		if oldestKey == "" || e.expires.Before(oldest) {
			oldestKey, oldest = k, e.expires
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
