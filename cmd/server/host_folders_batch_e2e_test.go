package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 批量变更分组走一遍真实的 handler：请求打进去，配置里要真的改掉，
// /api/v1/hosts 立刻就能读出新的 folder_id 与分类名。
//
// "点了确定没生效"这种问题，只有端到端跑一遍才说得清是前端没发、后端没写，还是没回读。
func TestBatchFolderAssignEndToEnd(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, id := range []string{"h1", "h2", "h3"} {
		h := srv.store.RegisterHost(id, id, "fp-"+id)
		h.LastSeen = time.Now().Unix()
	}
	f, err := srv.cfg.addHostFolder("", "数据库")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := srv.cfg.addHostFolder(f.ID, "主库")
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"host_ids": []string{"h1", "h2"}, "folder_id": sub.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/folder/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.handleSetHostFolderBatch(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("batch 返回 %d: %s", rr.Code, rr.Body.String())
	}

	_, assign := srv.cfg.hostFoldersSnapshot()
	for _, id := range []string{"h1", "h2"} {
		if assign[id] != sub.ID {
			t.Errorf("%s 的归属没改：%q want %q", id, assign[id], sub.ID)
		}
	}
	if assign["h3"] == sub.ID {
		t.Error("没选中的主机被一起改了")
	}

	// 回读：界面拿到的 folder_id / folder_path 必须立刻是新的，否则用户看到的还是旧分组。
	rr2 := httptest.NewRecorder()
	srv.handleHosts(rr2, httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("/hosts 返回 %d", rr2.Code)
	}
	var hosts []struct {
		ID         string `json:"id"`
		Category   string `json:"category"`
		FolderID   string `json:"folder_id"`
		FolderPath string `json:"folder_path"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &hosts); err != nil {
		t.Fatalf("解析 /hosts 失败: %v", err)
	}
	seen := 0
	for _, h := range hosts {
		if h.ID != "h1" && h.ID != "h2" {
			continue
		}
		seen++
		if h.FolderID != sub.ID {
			t.Errorf("%s folder_id=%q want %q", h.ID, h.FolderID, sub.ID)
		}
		if h.FolderPath != "数据库 / 主库" {
			t.Errorf("%s folder_path=%q want 数据库 / 主库", h.ID, h.FolderPath)
		}
		if h.Category != "主库" {
			t.Errorf("%s category=%q want 主库", h.ID, h.Category)
		}
	}
	if seen != 2 {
		t.Fatalf("只在 /hosts 里看到 %d 台被改的主机", seen)
	}
}

// 路由必须真的挂上：批量端点与 POST /hosts/{id}/folder 只差一个段的位置，
// 挂错了前端拿到的是 404，而 404 的响应体不是 JSON，界面上只会显示一句笼统的失败。
func TestBatchFolderRouteIsRegistered(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h1", "h1", "fp-h1")
	// Routes() 返回 http.Handler；用 ServeMux 自己再注册一遍无意义，这里直接打真实请求，
	// 只断言"不是 404 / 405"——路由挂错位置时这两个码是唯一的症状。
	mux := srv.Routes()
	body, _ := json.Marshal(map[string]any{"host_ids": []string{"h1"}, "folder_id": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/folder/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code == http.StatusNotFound || rr.Code == http.StatusMethodNotAllowed {
		t.Fatalf("批量端点没挂上：%d %s", rr.Code, rr.Body.String())
	}
}

// 批里混进已经删掉的主机：不整批失败，能改的照改，不存在的跳过且绝不写进配置。
//
// （早先这里是"整批拒绝"。那条规则把用户堵死过：界面上的勾选是几分钟前的快照，
// 中途删掉一台机器，整批就怎么点都失败，用户既不知道是哪台、也没有别的入口。）
func TestBatchFolderSkipsUnknownHost(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h1", "h1", "fp-h1")
	f, _ := srv.cfg.addHostFolder("", "数据库")

	body, _ := json.Marshal(map[string]any{"host_ids": []string{"h1", "ghost"}, "folder_id": f.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/folder/batch", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.handleSetHostFolderBatch(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("含已删除主机的批量请求不应整批失败，得到 %d: %s", rr.Code, rr.Body.String())
	}
	_, assign := srv.cfg.hostFoldersSnapshot()
	if assign["h1"] != f.ID {
		t.Errorf("能改的那台没改：%q want %q", assign["h1"], f.ID)
	}
	if _, orphan := assign["ghost"]; orphan {
		t.Error("给不存在的主机写了归属记录（配置里会留下回收不掉的孤儿）")
	}
}
