package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestK8sRESTClientListScaleRestart(t *testing.T) {
	var gotScale, gotRestart bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer test-token") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"gitVersion": "v1.29.0"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pods":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{
					"metadata": map[string]any{"name": "web-0", "namespace": "default"},
					"status":   map[string]any{"phase": "Running", "podIP": "10.0.0.1"},
					"spec":     map[string]any{"nodeName": "node-a"},
				}},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scale"):
			_ = json.NewEncoder(w).Encode(map[string]any{"spec": map[string]any{"replicas": 2}})
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/scale"):
			gotScale = true
			if r.Header.Get("Content-Type") != "application/merge-patch+json" {
				http.Error(w, "bad ct", 400)
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/deployments/"):
			gotRestart = true
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cli, err := newK8sRESTClient(K8sClusterConfig{
		APIServer: srv.URL,
		Token:     "test-token",
		Insecure:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ver, err := cli.Version()
	if err != nil || ver["gitVersion"] != "v1.29.0" {
		t.Fatalf("version=%v err=%v", ver, err)
	}
	pods, err := cli.ListPods("", 100, "")
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("pods=%v err=%v", pods, err)
	}
	if n, err := cli.GetDeploymentScale("default", "web"); err != nil || n != 2 {
		t.Fatalf("scale get=%d err=%v", n, err)
	}
	if err := cli.ScaleDeployment("default", "web", 3); err != nil {
		t.Fatal(err)
	}
	if err := cli.RestartDeployment("default", "web"); err != nil {
		t.Fatal(err)
	}
	if !gotScale || !gotRestart {
		t.Fatalf("scale=%v restart=%v", gotScale, gotRestart)
	}
}

func TestParseKubeconfigToken(t *testing.T) {
	raw := `
apiVersion: v1
kind: Config
current-context: c1
clusters:
- name: cl
  cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
contexts:
- name: c1
  context:
    cluster: cl
    user: u1
users:
- name: u1
  user:
    token: abc123
`
	ep, err := parseKubeconfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Server != "https://127.0.0.1:6443" || ep.Token != "abc123" || !ep.Insecure {
		t.Fatalf("%+v", ep)
	}
}

func TestMaskK8sCluster(t *testing.T) {
	m := maskK8sCluster(K8sClusterConfig{
		Token: "secret", KubeconfigYAML: "apiVersion: v1", CACert: "-----BEGIN CERTIFICATE-----\nX\n",
	})
	if m.Token != "****" || m.KubeconfigYAML != "****" {
		t.Fatalf("%+v", m)
	}
	if !m.HasToken || !m.HasKubeconfig || !m.HasCA {
		t.Fatalf("flags=%+v", m)
	}
	if m.CACert == "" {
		t.Fatal("CA should remain visible for edit form")
	}
}
