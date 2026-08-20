package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 批量改分组的两条真实路径，都走完整的中间件链（会话 Cookie + CSRF Origin + 限流 + 鉴权 +
// 路由），而不是直接调 handler——"提示失败"这种问题，一半的可能性根本不在 handler 里，
// 而是在它前面那几层（401 / 403 / 404 的响应体不是业务 JSON，界面只能显示一句笼统的失败）。
//
//	场景一：从没分过组的主机 → 选中一批 → 指定分组
//	场景二：已经在 A 组的主机 → 选中一批 → 改到 B 组 / 改回未分组
func TestBatchFolderThroughMiddleware(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, id := range []string{"h1", "h2", "h3"} {
		h := srv.store.RegisterHost(id, id, "fp-"+id)
		h.LastSeen = time.Now().Unix()
	}
	groupA, err := srv.cfg.addHostFolder("", "A组")
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := srv.cfg.addHostFolder("", "B组")
	if err != nil {
		t.Fatal(err)
	}
	token := srv.auth.issueSession("admin")

	// 场景一：未分组 → A 组
	if rr := postBatchFolder(t, srv, token, []string{"h1", "h2"}, groupA.ID); rr.Code != http.StatusOK {
		t.Fatalf("未分组主机批量归组失败：%d %s", rr.Code, rr.Body.String())
	}
	assertFolderAssign(t, srv, map[string]string{"h1": groupA.ID, "h2": groupA.ID})

	// 场景二：A 组 → B 组（整组改名的等价操作：把这批机器一次挪到另一个分组）
	if rr := postBatchFolder(t, srv, token, []string{"h1", "h2"}, groupB.ID); rr.Code != http.StatusOK {
		t.Fatalf("已分组主机批量改组失败：%d %s", rr.Code, rr.Body.String())
	}
	assertFolderAssign(t, srv, map[string]string{"h1": groupB.ID, "h2": groupB.ID})

	// 场景二之二：已分组 → 未分组（界面上的"未分组"选项传的是哨兵值）
	if rr := postBatchFolder(t, srv, token, []string{"h1"}, HostFolderUngroupedID); rr.Code != http.StatusOK {
		t.Fatalf("批量改回未分组失败：%d %s", rr.Code, rr.Body.String())
	}
	assertFolderAssign(t, srv, map[string]string{"h1": HostFolderUngroupedID, "h2": groupB.ID})
}

// 选中的主机里混进了已经被删掉的机器时，不能整批失败——界面上的选择是几分钟前的快照，
// 中途有机器下线删除是常态。能改的照改，改不了的报出来，用户不至于卡在一句"失败"上。
func TestBatchFolderSkipsMissingHostsInsteadOfFailing(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h1", "h1", "fp-h1")
	f, err := srv.cfg.addHostFolder("", "数据库")
	if err != nil {
		t.Fatal(err)
	}
	token := srv.auth.issueSession("admin")

	rr := postBatchFolder(t, srv, token, []string{"h1", "ghost"}, f.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("含已删除主机的批量请求不应整批失败：%d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Count   int      `json:"count"`
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v（%s）", err, rr.Body.String())
	}
	if resp.Count != 1 {
		t.Errorf("应改动 1 台，实际 count=%d", resp.Count)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0] != "ghost" {
		t.Errorf("跳过的主机应被如实报出，实际 skipped=%v", resp.Skipped)
	}
	assertFolderAssign(t, srv, map[string]string{"h1": f.ID})
	// 不存在的主机绝不能写进配置：那会留下永远回收不掉的孤儿记录。
	_, assign := srv.cfg.hostFoldersSnapshot()
	if _, orphan := assign["ghost"]; orphan {
		t.Error("给不存在的主机写了归属记录")
	}
}

// 一台都改不动时才算失败，并且要说清楚为什么。
func TestBatchFolderFailsOnlyWhenNothingApplies(t *testing.T) {
	srv, _ := newTestServer(t)
	f, _ := srv.cfg.addHostFolder("", "数据库")
	token := srv.auth.issueSession("admin")

	rr := postBatchFolder(t, srv, token, []string{"ghost1", "ghost2"}, f.ID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("全部主机都不存在时应返回 400，实际 %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error == "" {
		t.Error("失败响应必须带 error 字段，否则界面只能显示一句笼统的失败")
	}
}

// 目标分组在别的标签页里被删掉了：报错要说得出是分组没了，而不是一句 folder not found。
func TestBatchFolderMissingTargetFolderIsExplained(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h1", "h1", "fp-h1")
	token := srv.auth.issueSession("admin")

	rr := postBatchFolder(t, srv, token, []string{"h1"}, "hf-deadbeef")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("目标分组不存在应返回 400，实际 %d", rr.Code)
	}
	var resp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error == "" {
		t.Fatal("缺少 error 字段")
	}
}

func postBatchFolder(t *testing.T, srv *Server, token string, ids []string, folderID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"host_ids": ids, "folder_id": folderID})
	req := httptest.NewRequest(http.MethodPost, "http://panel.example.com/api/v1/hosts/folder/batch", bytes.NewReader(body))
	req.Host = "panel.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://panel.example.com")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rr := httptest.NewRecorder()
	handler := srv.csrfOriginMiddleware(srv.apiRateLimitMiddleware(srv.authMiddleware(srv.Routes())))
	handler.ServeHTTP(rr, req)
	return rr
}

func assertFolderAssign(t *testing.T, srv *Server, want map[string]string) {
	t.Helper()
	_, assign := srv.cfg.hostFoldersSnapshot()
	for id, folder := range want {
		if assign[id] != folder {
			t.Errorf("%s 的归属是 %q，期望 %q", id, assign[id], folder)
		}
	}
}
