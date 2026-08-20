package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContainerListPagedDefaults(t *testing.T) {
	// Catches: ignoring the paged query path would return full host inventories
	// instead of a bounded flat page.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/list?limit=50&offset=0", nil)
	limit, offset, paged := parseListLimitOffset(req)
	if !paged {
		t.Fatal("limit query should select paged container response")
	}
	if limit != 50 || offset != 0 {
		t.Fatalf("limit=%d offset=%d, want 50/0", limit, offset)
	}

	items, total, _ := flattenContainerInventoriesPage([]map[string]any{
		containerInventoryForTest("h1", "web-01", "docker", containersForTest(35, "web", "running")),
		containerInventoryForTest("h2", "db-01", "podman", containersForTest(25, "db", "exited")),
	}, "", "", "", "", limit, offset)

	if total != 60 {
		t.Fatalf("total=%d, want 60", total)
	}
	if len(items) != 50 {
		t.Fatalf("len(items)=%d, want 50", len(items))
	}
	first := items[0]
	if first["host_id"] != "h1" || first["host_name"] != "web-01" || first["runtime"] != "docker" {
		t.Fatalf("first item missing host metadata: %#v", first)
	}
	if first["id"] != "web-000" || first["name"] != "web-000" || first["image"] != "registry/web:0" || first["state"] != "running" {
		t.Fatalf("first item compact row mismatch: %#v", first)
	}
	last := items[49]
	if last["host_id"] != "h2" || last["id"] != "db-014" {
		t.Fatalf("page should continue across hosts, got last=%#v", last)
	}
}

func TestContainerListOffsetAndStatus(t *testing.T) {
	// Catches: applying offset before status filtering would return too few or
	// wrong containers on later pages.
	items, total, _ := flattenContainerInventoriesPage([]map[string]any{
		containerInventoryForTest("h1", "web-01", "docker", containersForTest(65, "web", "running")),
		containerInventoryForTest("h2", "db-01", "podman", append(
			containersForTest(60, "db", "running"),
			containersForTest(5, "db-old", "exited")...,
		)),
	}, "running", "", "", "", 50, 50)

	if total != 125 {
		t.Fatalf("running total=%d, want 125", total)
	}
	if len(items) != 50 {
		t.Fatalf("len(items)=%d, want 50", len(items))
	}
	if items[0]["host_id"] != "h1" || items[0]["id"] != "web-050" {
		t.Fatalf("offset should start at the 51st running row, got %#v", items[0])
	}
	if items[49]["host_id"] != "h2" || items[49]["id"] != "db-034" {
		t.Fatalf("page should include only running rows across hosts, got %#v", items[49])
	}
}

func TestContainerListStatusOtherAndQuery(t *testing.T) {
	// Catches: q/status filters must be applied to the flat container rows and
	// include host metadata in the searchable text.
	items, total, _ := flattenContainerInventoriesPage([]map[string]any{
		containerInventoryForTest("h1", "web-01", "docker", []any{
			map[string]any{"id": "a1111111111111", "name": "api", "image": "registry/api:latest", "state": "running"},
			map[string]any{"id": "b2222222222222", "name": "cache", "image": "registry/redis:7", "state": "paused"},
		}),
		containerInventoryForTest("h2", "gpu-node", "podman", []any{
			map[string]any{"id": "c3333333333333", "name": "trainer", "image": "registry/ml:latest", "state": "restarting"},
		}),
	}, "other", "gpu", "", "", 50, 0)

	if total != 1 {
		t.Fatalf("other+q total=%d, want 1", total)
	}
	if len(items) != 1 || items[0]["name"] != "trainer" || items[0]["host_name"] != "gpu-node" {
		t.Fatalf("unexpected q/status filtered page: %#v", items)
	}
}

func TestContainerListStatusBucketsMatchClassic(t *testing.T) {
	rows := []map[string]any{
		containerInventoryForTest("h1", "web-01", "docker", []any{
			map[string]any{"id": "a1111111111111", "name": "healthy-api", "image": "registry/api:latest", "state": "healthy"},
			map[string]any{"id": "b2222222222222", "name": "not-running-api", "image": "registry/api:old", "state": "not running"},
			map[string]any{"id": "c3333333333333", "name": "restarting-api", "image": "registry/api:next", "state": "restarting"},
			map[string]any{"id": "d4444444444444", "name": "created-api", "image": "registry/api:new", "state": "created"},
		}),
	}

	running, runningTotal, _ := flattenContainerInventoriesPage(rows, "running", "", "", "", 50, 0)
	if runningTotal != 1 || running[0]["name"] != "healthy-api" {
		t.Fatalf("running bucket should only include healthy/running states, got total=%d rows=%#v", runningTotal, running)
	}

	stopped, stoppedTotal, _ := flattenContainerInventoriesPage(rows, "stopped", "", "", "", 50, 0)
	if stoppedTotal != 2 || stopped[0]["name"] != "not-running-api" || stopped[1]["name"] != "created-api" {
		t.Fatalf("stopped bucket should include not running and created, got total=%d rows=%#v", stoppedTotal, stopped)
	}

	other, otherTotal, _ := flattenContainerInventoriesPage(rows, "other", "", "", "", 50, 0)
	if otherTotal != 1 || other[0]["name"] != "restarting-api" {
		t.Fatalf("other bucket should include restarting only, got total=%d rows=%#v", otherTotal, other)
	}
}

func TestContainerListComposeProjectFilter(t *testing.T) {
	rows := []map[string]any{
		containerInventoryForTest("h1", "web-01", "docker", []any{
			map[string]any{"id": "a1", "name": "api", "image": "api:1", "state": "running", "compose_project": "shop", "compose_service": "api"},
			map[string]any{"id": "a2", "name": "db", "image": "pg:16", "state": "running", "compose_project": "shop", "compose_service": "db"},
			map[string]any{"id": "a3", "name": "redis", "image": "redis:7", "state": "running", "compose_project": "cache", "compose_service": "redis"},
			map[string]any{"id": "a4", "name": "orphan", "image": "busybox", "state": "exited"},
		}),
	}

	items, total, projects := flattenContainerInventoriesPage(rows, "", "", "shop", "", 50, 0)
	if total != 2 || len(items) != 2 {
		t.Fatalf("compose_project=shop total=%d items=%d, want 2", total, len(items))
	}
	if items[0]["compose_project"] != "shop" || items[0]["compose_service"] != "api" {
		t.Fatalf("expected compose fields on row: %#v", items[0])
	}
	if len(projects) != 2 || projects[0] != "cache" || projects[1] != "shop" {
		t.Fatalf("compose_projects=%#v, want [cache shop]", projects)
	}

	svcItems, svcTotal, _ := flattenContainerInventoriesPage(rows, "", "", "shop", "db", 50, 0)
	if svcTotal != 1 || svcItems[0]["name"] != "db" {
		t.Fatalf("compose service filter failed: total=%d items=%#v", svcTotal, svcItems)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/list?compose_project=shop", nil)
	_, _, paged := parseListLimitOffset(req)
	if !paged {
		t.Fatal("compose_project query should select paged response")
	}
}

func TestCompactContainerRowPreservesCreated(t *testing.T) {
	row := compactContainerRow(map[string]any{
		"id":              "abcdefabcdef",
		"name":            "api",
		"image":           "api:1",
		"state":           "running",
		"created":         "2026-08-08 10:00:00 +0000 UTC",
		"compose_project": "shop",
		"compose_service": "api",
	})
	if row["created"] != "2026-08-08 10:00:00 +0000 UTC" {
		t.Fatalf("created missing from compact row: %#v", row)
	}
	if row["compose_project"] != "shop" || row["compose_service"] != "api" {
		t.Fatalf("compose fields missing from compact row: %#v", row)
	}
}

func TestContainerListHTTPShapeWithNilPG(t *testing.T) {
	var s Server

	legacyReq := httptest.NewRequest(http.MethodGet, "/api/v1/containers/list", nil)
	legacyRR := httptest.NewRecorder()
	s.handleContainerList(legacyRR, legacyReq)
	var legacy map[string]any
	if err := json.NewDecoder(legacyRR.Body).Decode(&legacy); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacy["inventories"]; !ok {
		t.Fatalf("legacy response missing inventories key: %#v", legacy)
	}
	if _, ok := legacy["items"]; ok {
		t.Fatalf("legacy response should not include items key: %#v", legacy)
	}

	pagedReq := httptest.NewRequest(http.MethodGet, "/api/v1/containers/list?limit=50&offset=100", nil)
	pagedRR := httptest.NewRecorder()
	s.handleContainerList(pagedRR, pagedReq)
	var paged map[string]any
	if err := json.NewDecoder(pagedRR.Body).Decode(&paged); err != nil {
		t.Fatal(err)
	}
	if _, ok := paged["items"]; !ok {
		t.Fatalf("paged response missing items key: %#v", paged)
	}
	if _, ok := paged["total"]; !ok {
		t.Fatalf("paged response missing total key: %#v", paged)
	}
	if _, ok := paged["inventories"]; ok {
		t.Fatalf("paged response should not include inventories key: %#v", paged)
	}
	if paged["offset"] != float64(0) {
		t.Fatalf("paged response offset=%#v, want clamped 0 for empty result", paged["offset"])
	}
}

func TestContainerListLegacyInventoriesWithoutLimit(t *testing.T) {
	// Catches: old clients without paging/filter keys must keep the legacy
	// inventories response shape.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/list?host=h1", nil)
	limit, offset, paged := parseListLimitOffset(req)
	if paged {
		t.Fatalf("host-only query should stay legacy, got limit=%d offset=%d", limit, offset)
	}

	for _, path := range []string{
		"/api/v1/containers/list?status=stopped",
		"/api/v1/containers/list?q=web",
		"/api/v1/containers/list?offset=-20",
		"/api/v1/containers/list?limit=500",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		limit, offset, paged := parseListLimitOffset(req)
		if !paged {
			t.Fatalf("%s should select paged response", path)
		}
		if limit <= 0 || limit > 200 || offset < 0 {
			t.Fatalf("%s normalized to invalid limit=%d offset=%d", path, limit, offset)
		}
	}
}

func containerInventoryForTest(hostID, hostName, runtime string, containers []any) map[string]any {
	return map[string]any{
		"host_id":    hostID,
		"host_name":  hostName,
		"runtime":    runtime,
		"containers": containers,
	}
}

func containersForTest(n int, prefix, state string) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s-%03d", prefix, i)
		out = append(out, map[string]any{
			"id":    name + "-abcdefabcdef",
			"name":  name,
			"image": fmt.Sprintf("registry/%s:%d", prefix, i),
			"state": state,
		})
	}
	return out
}
