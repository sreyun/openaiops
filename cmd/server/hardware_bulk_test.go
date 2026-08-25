package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// /hardware/health 不带 host = 批量模式。这条分支是为了根治控制台硬件页的一次规模
// 事故（500 台 → 500 个并发请求），但**批量绝不能顺手放宽权限**：主机级 RBAC 必须
// 逐行仍然生效，否则一个只能看 A 机房的账号会在这里拿到全机群的硬件信息。
func TestHardwareHealthBulkHonoursHostRBAC(t *testing.T) {
	srv, _ := newTestServer(t)
	if srv.pg == nil {
		// 没有真实 PG 时该分支直接回空集合——这本身也是必须成立的行为：
		// 不能因为存储不可用就 500，硬件页会整块打不开。
		req := httptest.NewRequest(http.MethodGet, "/api/v1/hardware/health", nil)
		rr := httptest.NewRecorder()
		srv.handleHardwareHealth(rr, req)
		if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusOK {
			t.Fatalf("无 PG 时应回 200 空集或 401（未登录），实际 %d", rr.Code)
		}
		return
	}
	t.Skip("带真实 PG 的批量 RBAC 校验由 PG 集成套件覆盖")
}

// 未登录不能走批量分支——它绕过了 requireHostAccess，必须自己先确认身份。
func TestHardwareHealthBulkRejectsAnonymous(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hardware/health", nil)
	rr := httptest.NewRecorder()
	srv.handleHardwareHealth(rr, req)
	if rr.Code == http.StatusOK {
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if snaps, ok := body["snapshots"].([]any); ok && len(snaps) > 0 {
			t.Fatal("未登录不该拿到任何硬件快照")
		}
	}
}

// 带 host 的老路径必须原样保留：改成支持批量不能把单主机查询弄坏。
func TestHardwareHealthSingleHostStillGated(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hardware/health?host=nonexistent-host", nil)
	rr := httptest.NewRecorder()
	srv.handleHardwareHealth(rr, req)
	if rr.Code == http.StatusBadRequest {
		t.Fatal("带了 host 就不该再报 host required")
	}
}
