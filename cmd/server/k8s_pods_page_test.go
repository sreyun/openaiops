package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestK8sRESTClientListPodsReturnsContinueMetadata(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/default/pods" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Fatalf("limit=%q", got)
		}
		if got := r.URL.Query().Get("continue"); got != "next-1" {
			t.Fatalf("continue=%q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{"continue": "next-2", "remainingItemCount": 37},
			"items": []map[string]any{{
				"metadata": map[string]any{"name": "web-0", "namespace": "default"},
			}},
		})
	}))
	defer api.Close()

	cli, err := newK8sRESTClient(K8sClusterConfig{APIServer: api.URL, Token: "tok", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := cli.ListPods("default", 50, "next-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Continue != "next-2" || res.RemainingApprox != 37 {
		t.Fatalf("result=%+v", res)
	}
}

func TestK8sPodsLimitClamp(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantLimit string
	}{
		{name: "default limit", query: "", wantLimit: "50"},
		{name: "max limit", query: "?limit=999", wantLimit: "200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("limit"); got != tc.wantLimit {
					t.Fatalf("limit=%q want %q", got, tc.wantLimit)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"metadata": map[string]any{"continue": "next", "remainingItemCount": 9},
					"items": []map[string]any{{
						"metadata": map[string]any{"name": "web-0", "namespace": "default"},
						"spec":     map[string]any{"nodeName": "node-a"},
						"status":   map[string]any{"phase": "Running", "podIP": "10.42.0.1"},
					}},
				})
			}))
			defer api.Close()

			cfg, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.UpsertK8sCluster(K8sClusterConfig{
				ID: "c1", Name: "test", Enabled: true, APIServer: api.URL, Token: "tok", Insecure: true, DefaultNS: "default",
			}); err != nil {
				t.Fatal(err)
			}
			s := &Server{store: NewStore(), cfg: cfg}
			req := httptest.NewRequest(http.MethodGet, "/api/k8s/c1/pods"+tc.query, nil)
			req.SetPathValue("id", "c1")
			rec := httptest.NewRecorder()

			s.handleK8sPods(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var out map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if got := int(out["limit"].(float64)); got != mustAtoi(tc.wantLimit) {
				t.Fatalf("response limit=%d want %s", got, tc.wantLimit)
			}
			if out["continue"] != "next" || out["truncated"] != true || int(out["remaining"].(float64)) != 9 {
				t.Fatalf("pagination fields=%v", out)
			}
		})
	}
}

func TestK8sPodsHostIndexOnce(t *testing.T) {
	s := &Server{store: NewStore()}
	h := s.store.RegisterHost("h1", "node-a", "fp1")
	h.IP = "10.0.0.5"
	idx := s.buildK8sHostIndex()

	s.store.RegisterHost("h2", "node-b", "fp2")
	if got := s.matchHostForK8sNodeWithIndex("node-b", nil, idx); got != nil {
		t.Fatalf("prebuilt index should not see hosts added later: %+v", got)
	}
	if got := s.matchHostForK8sNodeWithIndex("node-a", nil, idx); got == nil || got.ID != "h1" {
		t.Fatalf("indexed match=%+v", got)
	}
}

func mustAtoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
