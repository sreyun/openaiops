package main

// Write-dedup cache for the periodic PG flush.
//
// 背景（生产 500 台机群实测：pg-data 9.3 GiB，vm-data 只有 93 MiB）：
// pgFlush 每 15 秒把一批「内存态镜像」整体写回 PG，而不管内容有没有变化：
//
//	· hosts          —— DELETE 全表 + 重插全部主机（500 行 JSONB）
//	· oncall_pages   —— DELETE 全表 + 重插
//	· change_records —— DELETE 全表 + 重插
//	· incidents/tickets —— 每行 UPSERT 一遍
//	· kv_state 里 9 个 blob（sessions / messages / alert_states / ...）
//	· logs blob（8000 行活动日志整块序列化，约 1.4 MB）每 30 秒一次
//
// PG 是 MVCC：每次 UPDATE/DELETE 都留下死元组，还要写等量 WAL。上面这些写绝大
// 多数是「内容一个字节都没变」的重复写，于是磁盘涨的不是数据，是 **膨胀 + WAL**。
// 粗算每天 8～10 GB 的写放大，而真正的存量数据只有几百 MB —— 这正是 PG 远大于
// VictoriaMetrics 的原因（VM 那 93 MiB 才是压缩后的真实时序数据）。
//
// 这里用「写前比对内容哈希」把没变的写全部掐掉。要点：
//   - 哈希只在**事务提交成功之后**才记账（remember），否则一次失败的事务会让缓存
//     误以为已写入，PG 从此停在旧值上。
//   - 需要镜像删除语义的表（原本靠 DELETE 全表实现）改为「首次从 PG 播种 id 集合
//     → 之后只删差集」，保证行为等价而不再全表重写。

import (
	"crypto/sha256"
	"sync"
)

// pgWriteCache tracks, per logical key, the hash of the last value successfully
// written to PG, plus the id set known to exist for tables with mirror-delete
// semantics.
type pgWriteCache struct {
	mu     sync.Mutex
	hash   map[string][32]byte
	ids    map[string]map[string]bool // table → ids believed present in PG
	seeded map[string]bool            // table → id set已从 PG 播种
}

func newPGWriteCache() *pgWriteCache {
	return &pgWriteCache{
		hash:   map[string][32]byte{},
		ids:    map[string]map[string]bool{},
		seeded: map[string]bool{},
	}
}

// isChanged reports whether raw differs from the last value remembered for key.
// It does NOT record anything — call remember only after a successful commit.
func (c *pgWriteCache) isChanged(key string, raw []byte) bool {
	if c == nil {
		return true
	}
	sum := sha256.Sum256(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, ok := c.hash[key]
	return !ok || prev != sum
}

// remember records raw as the value now living in PG.
func (c *pgWriteCache) remember(key string, raw []byte) {
	if c == nil {
		return
	}
	sum := sha256.Sum256(raw)
	c.mu.Lock()
	c.hash[key] = sum
	c.mu.Unlock()
}

// forget drops a key so the next write is unconditional (used when a row is
// deleted, and when a write failed and the cache must not claim success).
func (c *pgWriteCache) forget(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.hash, key)
	c.mu.Unlock()
}

// needsSeed reports whether table's id set still has to be loaded from PG.
// The first flush after start must learn what is already there, otherwise rows
// deleted while the process was down would never be mirrored away.
func (c *pgWriteCache) needsSeed(table string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.seeded[table]
}

func (c *pgWriteCache) seed(table string, ids []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	c.ids[table] = set
	c.seeded[table] = true
	c.mu.Unlock()
}

// missingIDs returns the ids believed to be in PG but absent from live, i.e.
// exactly the rows the old "DELETE 全表 + 重插" did away with implicitly.
func (c *pgWriteCache) missingIDs(table string, live map[string]bool) []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for id := range c.ids[table] {
		if !live[id] {
			out = append(out, id)
		}
	}
	return out
}

// setIDs replaces the known id set for table after a successful commit.
func (c *pgWriteCache) setIDs(table string, live map[string]bool) {
	if c == nil {
		return
	}
	set := make(map[string]bool, len(live))
	for id := range live {
		set[id] = true
	}
	c.mu.Lock()
	c.ids[table] = set
	c.mu.Unlock()
}

// invalidateTable drops every cached hash whose key belongs to table and forces
// the id set to be re-seeded. Used when a flush transaction fails, so the next
// attempt rewrites everything instead of trusting a half-applied state.
func (c *pgWriteCache) invalidateTable(table string) {
	if c == nil {
		return
	}
	prefix := table + "/"
	c.mu.Lock()
	for k := range c.hash {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(c.hash, k)
		}
	}
	delete(c.ids, table)
	delete(c.seeded, table)
	c.mu.Unlock()
}

// selectIDsText reads a single text-id column into a slice (seed helper).
func (p *pgStore) selectIDsText(query string) ([]string, error) {
	rows, err := p.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
