package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 部署指纹一旦被覆盖，客户按旧指纹签发的授权立刻变成 install mismatch，平台降级只读，
// 而旧指纹已经被写没了——连"拿指纹重新签发"这条退路都断了。
// 这条用例把两件事钉死：已有指纹必须原样沿用；写入必须是 INSERT ... DO NOTHING，
// 并发/重复启动都抢不走已有的那条。
//
// 需要 AIOPS_TEST_PG_DSN，与其余 PG 集成套件同一门槛。
func TestLoadInstallIDNeverOverwritesExistingIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AIOPS_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("AIOPS_TEST_PG_DSN 未设置，跳过部署指纹持久化校验")
	}
	ps, err := openPGStore(dsn)
	if err != nil {
		t.Fatalf("连接 PostgreSQL 失败: %v", err)
	}
	defer ps.close()

	// 备份并在结束时还原真实的 install_id，别把开发库的指纹搞乱。
	before, _ := ps.loadKV(licenseInstallKV)
	t.Cleanup(func() {
		if len(before) > 0 {
			_ = ps.saveKV(licenseInstallKV, before)
		} else {
			_, _ = ps.db.Exec(`DELETE FROM kv_state WHERE k=$1`, licenseInstallKV)
		}
	})

	const pinned = "AIO-KEEP-THIS-ONE-PLZ"
	blob, _ := json.Marshal(map[string]any{"id": pinned, "created": 1})
	if err := ps.saveKV(licenseInstallKV, blob); err != nil {
		t.Fatalf("预置指纹失败: %v", err)
	}

	s := &Server{pg: ps}
	licMu.Lock()
	prevInstall := licInstallID
	licInstallID = "AIO-FRESH-RANDOM-ONE" // 模拟启动时刚生成的临时指纹
	licMu.Unlock()
	// 指纹是包级变量，跑完必须还原，否则后面的授权用例读到的是这里留下的值。
	t.Cleanup(func() {
		licMu.Lock()
		licInstallID = prevInstall
		licMu.Unlock()
	})

	s.loadInstallID()

	licMu.RLock()
	got := licInstallID
	licMu.RUnlock()
	if got != pinned {
		t.Fatalf("已有指纹应被沿用，得到 %q（期望 %q）", got, pinned)
	}

	// 库里那条也不许被改写。
	raw, err := ps.loadKV(licenseInstallKV)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &v) != nil || v.ID != pinned {
		t.Fatalf("库里的指纹被改写成了 %q", v.ID)
	}
}

// saveKVIfAbsent 的语义：键已存在时不写、返回 false；不存在时写入、返回 true。
func TestSaveKVIfAbsentDoesNotOverwriteIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AIOPS_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("AIOPS_TEST_PG_DSN 未设置，跳过 saveKVIfAbsent 校验")
	}
	ps, err := openPGStore(dsn)
	if err != nil {
		t.Fatalf("连接 PostgreSQL 失败: %v", err)
	}
	defer ps.close()

	const key = "aiops_test_kv_if_absent"
	t.Cleanup(func() { _, _ = ps.db.Exec(`DELETE FROM kv_state WHERE k=$1`, key) })
	_, _ = ps.db.Exec(`DELETE FROM kv_state WHERE k=$1`, key)

	inserted, err := ps.saveKVIfAbsent(key, []byte(`{"v":1}`))
	if err != nil || !inserted {
		t.Fatalf("首次写入应成功：inserted=%v err=%v", inserted, err)
	}
	inserted, err = ps.saveKVIfAbsent(key, []byte(`{"v":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("键已存在时不应写入")
	}
	raw, err := ps.loadKV(key)
	if err != nil {
		t.Fatal(err)
	}
	// kv_state.data 是 jsonb：PG 会把 {"v":1} 规范化成 {"v": 1}，所以按语义比而不是按字节比。
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("读回的不是合法 JSON：%s", raw)
	}
	if v, _ := got["v"].(float64); v != 1 {
		t.Fatalf("已有值被覆盖成了 %s", raw)
	}
}
