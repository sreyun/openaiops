package main

import (
	"fmt"
	"strings"
	"time"
)

// K8sClusterConfig is a server-side Kubernetes API endpoint registration.
// Auth: either APIServer+Token(+optional CA), or KubeconfigYAML (current-context).
type K8sClusterConfig struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	APIServer      string `json:"api_server,omitempty"`
	Token          string `json:"token,omitempty"`             // masked / encrypted
	CACert         string `json:"ca_cert,omitempty"`           // PEM
	KubeconfigYAML string `json:"kubeconfig_yaml,omitempty"`   // encrypted
	DefaultNS      string `json:"default_namespace,omitempty"` // empty = all namespaces
	Insecure       bool   `json:"insecure_skip_tls,omitempty"`
	CreatedAt      int64  `json:"created_at,omitempty"`
	// Response-only flags so the UI can show "已配置" without revealing secrets.
	HasToken      bool `json:"has_token,omitempty"`
	HasKubeconfig bool `json:"has_kubeconfig,omitempty"`
	HasCA         bool `json:"has_ca,omitempty"`
}

func maskK8sCluster(c K8sClusterConfig) K8sClusterConfig {
	out := c
	out.HasToken = strings.TrimSpace(c.Token) != ""
	out.HasKubeconfig = strings.TrimSpace(c.KubeconfigYAML) != ""
	out.HasCA = strings.TrimSpace(c.CACert) != ""
	// List/UI should show the real API address even for kubeconfig-only clusters.
	if strings.TrimSpace(out.APIServer) == "" && out.HasKubeconfig {
		if ep, err := parseKubeconfig(c.KubeconfigYAML); err == nil && strings.TrimSpace(ep.Server) != "" {
			out.APIServer = strings.TrimRight(strings.TrimSpace(ep.Server), "/")
		}
	}
	if out.HasToken {
		out.Token = "****"
	}
	if out.HasKubeconfig {
		out.KubeconfigYAML = "****"
	}
	return out
}

func (cs *ConfigStore) ListK8sClusters() []K8sClusterConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]K8sClusterConfig, 0, len(cs.cfg.K8sClusters))
	out = append(out, cs.cfg.K8sClusters...)
	return out
}

func (cs *ConfigStore) GetK8sCluster(id string) (K8sClusterConfig, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, c := range cs.cfg.K8sClusters {
		if c.ID == id {
			return c, true
		}
	}
	return K8sClusterConfig{}, false
}

func (cs *ConfigStore) UpsertK8sCluster(in K8sClusterConfig) (K8sClusterConfig, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return K8sClusterConfig{}, fmt.Errorf("cluster name required")
	}
	in.APIServer = strings.TrimSpace(in.APIServer)
	in.Token = strings.TrimSpace(in.Token)
	in.CACert = strings.TrimSpace(in.CACert)
	in.KubeconfigYAML = strings.TrimSpace(in.KubeconfigYAML)
	in.DefaultNS = strings.TrimSpace(in.DefaultNS)
	// Never persist response-only flags.
	in.HasToken, in.HasKubeconfig, in.HasCA = false, false, false

	keepSecret := func(v, prev string) string {
		if v == "" || strings.Contains(v, "****") {
			return prev
		}
		return v
	}
	cs.mu.Lock()
	if in.ID == "" {
		in.ID = termID()[:8]
		in.CreatedAt = time.Now().Unix()
		if err := validateK8sClusterAuth(in); err != nil {
			cs.mu.Unlock()
			return K8sClusterConfig{}, err
		}
		backfillK8sAPIServerFromKubeconfig(&in)
		cs.cfg.K8sClusters = append(cs.cfg.K8sClusters, in)
		cs.mu.Unlock()
		return in, cs.save()
	}
	for i, c := range cs.cfg.K8sClusters {
		if c.ID == in.ID {
			in.CreatedAt = c.CreatedAt
			in.Token = keepSecret(in.Token, c.Token)
			in.KubeconfigYAML = keepSecret(in.KubeconfigYAML, c.KubeconfigYAML)
			if err := validateK8sClusterAuth(in); err != nil {
				cs.mu.Unlock()
				return K8sClusterConfig{}, err
			}
			backfillK8sAPIServerFromKubeconfig(&in)
			cs.cfg.K8sClusters[i] = in
			cs.mu.Unlock()
			return in, cs.save()
		}
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = time.Now().Unix()
	}
	if err := validateK8sClusterAuth(in); err != nil {
		cs.mu.Unlock()
		return K8sClusterConfig{}, err
	}
	backfillK8sAPIServerFromKubeconfig(&in)
	cs.cfg.K8sClusters = append(cs.cfg.K8sClusters, in)
	cs.mu.Unlock()
	return in, cs.save()
}

// backfillK8sAPIServerFromKubeconfig fills api_server from kubeconfig when empty
// so the cluster list Endpoint column shows a real URL instead of "kubeconfig".
func backfillK8sAPIServerFromKubeconfig(c *K8sClusterConfig) {
	if c == nil || strings.TrimSpace(c.APIServer) != "" {
		return
	}
	kc := strings.TrimSpace(c.KubeconfigYAML)
	if kc == "" || kc == "****" {
		return
	}
	ep, err := parseKubeconfig(kc)
	if err != nil || strings.TrimSpace(ep.Server) == "" {
		return
	}
	c.APIServer = strings.TrimRight(strings.TrimSpace(ep.Server), "/")
}

func validateK8sClusterAuth(c K8sClusterConfig) error {
	kc := strings.TrimSpace(c.KubeconfigYAML)
	if kc != "" && kc != "****" {
		return nil
	}
	if strings.TrimSpace(c.APIServer) == "" || strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("请填写 API Server + Token，或粘贴完整 kubeconfig")
	}
	return nil
}

func (cs *ConfigStore) DeleteK8sCluster(id string) error {
	cs.mu.Lock()
	kept := make([]K8sClusterConfig, 0, len(cs.cfg.K8sClusters))
	found := false
	for _, c := range cs.cfg.K8sClusters {
		if c.ID == id {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		cs.mu.Unlock()
		return fmt.Errorf("cluster not found")
	}
	cs.cfg.K8sClusters = kept
	cs.mu.Unlock()
	return cs.save()
}
