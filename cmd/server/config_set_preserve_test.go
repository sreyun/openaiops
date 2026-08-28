package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandleSetConfigPreservesDedicatedSecrets reproduces the alert-settings /
// thresholds save path: GET /api/v1/config (masked) → POST the same payload.
// Dedicated-endpoint secrets (MySQL / K8s / prev install token / CI/CD) must
// survive — otherwise a routine threshold tweak permanently replaces passwords
// with "****" and breaks the install-token grace window.
func TestHandleSetConfigPreservesDedicatedSecrets(t *testing.T) {
	cfg := newTestConfigStore(t)
	cfg.mu.Lock()
	cfg.cfg.MySQLConnections = []MySQLConnection{{
		ID: "db1", Name: "prod", Host: "10.0.0.1", Password: "MYSQL-REAL-PASSWORD", Enabled: true,
	}}
	cfg.cfg.K8sClusters = []K8sClusterConfig{{
		ID: "k1", Name: "prod-k8s", APIServer: "https://k8s.example", Token: "K8S-REAL-TOKEN-VALUE",
		KubeconfigYAML: "apiVersion: v1\nkind: Config\n", Enabled: true,
	}}
	cfg.cfg.CICDConnections = []CICDConnection{{
		ID: "ci1", Name: "gitlab", Provider: "gitlab", BaseURL: "https://gitlab.example", Token: "CICD-REAL-TOKEN", Enabled: true,
	}}
	cfg.cfg.DataSources = []DataSource{{
		ID: "ds1", Name: "loki", Type: "loki", URL: "http://loki:3100", AuthPass: "DS-REAL-PASS",
	}}
	cfg.cfg.PrevInstallToken = "PREVINSTALLTOKENABCDEF"
	cfg.cfg.PrevTokenExpiresAt = time.Now().Add(48 * time.Hour).Unix()
	cfg.mu.Unlock()

	srv := &Server{cfg: cfg}

	getReq := httptest.NewRequest("GET", "/api/v1/config", nil)
	getW := httptest.NewRecorder()
	srv.handleGetConfig(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET /config: %d", getW.Code)
	}
	var roundTrip ServerConfig
	if err := json.Unmarshal(getW.Body.Bytes(), &roundTrip); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	// Sanity: secrets left the wire masked.
	if len(roundTrip.MySQLConnections) != 1 || !strings.Contains(roundTrip.MySQLConnections[0].Password, "****") {
		t.Fatalf("expected masked MySQL password in GET, got %+v", roundTrip.MySQLConnections)
	}
	if len(roundTrip.K8sClusters) != 1 || roundTrip.K8sClusters[0].Token != "****" {
		t.Fatalf("expected masked K8s token in GET, got %+v", roundTrip.K8sClusters)
	}

	// Mirror handleSetConfig without spawning notifier.Trigger (needs a live Notifier).
	mergeSecrets(&roundTrip, cfg.Get())
	if err := cfg.Set(roundTrip); err != nil {
		t.Fatalf("Set after masked round-trip: %v", err)
	}

	got := cfg.Get()
	if len(got.MySQLConnections) != 1 || got.MySQLConnections[0].Password != "MYSQL-REAL-PASSWORD" {
		t.Fatalf("MySQL password corrupted after settings save: %+v", got.MySQLConnections)
	}
	if len(got.K8sClusters) != 1 || got.K8sClusters[0].Token != "K8S-REAL-TOKEN-VALUE" {
		t.Fatalf("K8s token corrupted after settings save: %+v", got.K8sClusters)
	}
	if got.K8sClusters[0].KubeconfigYAML == "" || got.K8sClusters[0].KubeconfigYAML == "****" {
		t.Fatalf("K8s kubeconfig corrupted after settings save: %q", got.K8sClusters[0].KubeconfigYAML)
	}
	if len(got.CICDConnections) != 1 || got.CICDConnections[0].Token != "CICD-REAL-TOKEN" {
		t.Fatalf("CICD connections wiped/corrupted after settings save: %+v", got.CICDConnections)
	}
	if len(got.DataSources) != 1 || got.DataSources[0].AuthPass != "DS-REAL-PASS" {
		t.Fatalf("DataSource auth corrupted after settings save: %+v", got.DataSources)
	}
	if got.PrevInstallToken != "PREVINSTALLTOKENABCDEF" {
		t.Fatalf("PrevInstallToken corrupted after settings save: %q", got.PrevInstallToken)
	}
	if !cfg.ValidInstallToken("PREVINSTALLTOKENABCDEF") {
		t.Fatal("prev install token grace broken after settings save")
	}
}
