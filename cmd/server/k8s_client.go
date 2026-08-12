package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	k8sProbeTimeout  = 8 * time.Second
	k8sDialTimeout   = 5 * time.Second
	k8sTLSHandshake  = 5 * time.Second
	k8sHeaderTimeout = 15 * time.Second
	k8sClientTimeout = 20 * time.Second
)

type k8sEndpoint struct {
	Server        string
	Token         string
	CACert        string // PEM
	Insecure      bool
	ClientCertPEM string
	ClientKeyPEM  string
}

type k8sRESTClient struct {
	base   string
	token  string
	client *http.Client
}

type k8sListResult struct {
	Items           []map[string]any
	Continue        string
	RemainingApprox int
}

func resolveK8sEndpoint(cfg K8sClusterConfig) (k8sEndpoint, error) {
	var ep k8sEndpoint
	kc := strings.TrimSpace(cfg.KubeconfigYAML)
	if kc != "" && kc != "****" {
		parsed, err := parseKubeconfig(kc)
		if err != nil {
			return ep, err
		}
		return parsed, nil
	}
	server := strings.TrimRight(strings.TrimSpace(cfg.APIServer), "/")
	token := strings.TrimSpace(cfg.Token)
	if server == "" || token == "" {
		return ep, fmt.Errorf("需要填写 API Server + Token，或粘贴 kubeconfig")
	}
	ep.Server = server
	ep.Token = token
	ep.CACert = strings.TrimSpace(cfg.CACert)
	ep.Insecure = cfg.Insecure
	if ep.CACert == "" && !ep.Insecure {
		return ep, fmt.Errorf("未提供 CA 证书时请勾选跳过 TLS 校验（仅内网临时使用）")
	}
	return ep, nil
}

func newK8sRESTClient(cfg K8sClusterConfig) (*k8sRESTClient, error) {
	ep, err := resolveK8sEndpoint(cfg)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ep.Insecure {
		tlsCfg.InsecureSkipVerify = true
	}
	if ep.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ep.CACert)) {
			return nil, fmt.Errorf("无效的 CA 证书 PEM")
		}
		tlsCfg.RootCAs = pool
	}
	if ep.ClientCertPEM != "" && ep.ClientKeyPEM != "" {
		cert, err := tls.X509KeyPair([]byte(ep.ClientCertPEM), []byte(ep.ClientKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("客户端证书无效: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return &k8sRESTClient{
		base:  strings.TrimRight(ep.Server, "/"),
		token: ep.Token,
		client: &http.Client{
			Timeout: k8sClientTimeout,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   k8sDialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSClientConfig:       tlsCfg,
				TLSHandshakeTimeout:   k8sTLSHandshake,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       60 * time.Second,
				ResponseHeaderTimeout: k8sHeaderTimeout,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}, nil
}

func (c *k8sRESTClient) do(method, path string, query url.Values, body []byte, contentType string) ([]byte, int, error) {
	return c.doCtx(context.Background(), method, path, query, body, contentType)
}

func (c *k8sRESTClient) doCtx(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) ([]byte, int, error) {
	full := c.base + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, rdr)
	if err != nil {
		return nil, 0, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return raw, resp.StatusCode, fmt.Errorf("k8s API %d: %s", resp.StatusCode, msg)
	}
	return raw, resp.StatusCode, nil
}

func (c *k8sRESTClient) getJSON(path string, query url.Values, out any) error {
	raw, _, err := c.do(http.MethodGet, path, query, nil, "")
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *k8sRESTClient) getJSONCtx(ctx context.Context, path string, query url.Values, out any) error {
	raw, _, err := c.doCtx(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *k8sRESTClient) Version() (map[string]any, error) {
	return c.versionWithTimeout(k8sClientTimeout)
}

// VersionProbe is a short connectivity check used by test/overview so a dead
// API server fails fast instead of blocking the UI for ~30s.
func (c *k8sRESTClient) VersionProbe() (map[string]any, error) {
	return c.versionWithTimeout(k8sProbeTimeout)
}

func (c *k8sRESTClient) versionWithTimeout(d time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out := map[string]any{}
	if err := c.getJSONCtx(ctx, "/version", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// friendlyK8sErr turns raw net/http errors into actionable Chinese messages for the UI.
func friendlyK8sErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "context deadline exceeded"),
		strings.Contains(low, "client.timeout"),
		strings.Contains(low, "i/o timeout"),
		strings.Contains(low, "timeout exceeded"):
		return "连接 Kubernetes API 超时：请确认本平台服务端能访问该 API（网络/防火墙/VPN/路由），并核对地址与端口（通常 6443）"
	case strings.Contains(low, "connection refused"):
		return "连接被拒绝：API Server 未监听，或地址/端口不正确"
	case strings.Contains(low, "no such host"), strings.Contains(low, "server misbehaving"):
		return "无法解析 API Server 主机名，请检查地址拼写与 DNS"
	case strings.Contains(low, "network is unreachable"), strings.Contains(low, "no route to host"):
		return "网络不可达：服务端到该内网地址没有路由（常见于平台与集群不在同一网段）"
	case strings.Contains(low, "x509"), strings.Contains(low, "certificate"), strings.Contains(low, "tls:"):
		msg := "TLS 证书校验失败：请粘贴正确 CA，或仅在可信内网勾选「跳过 TLS 校验」"
		if len(s) > 160 {
			return msg + "（" + s[:160] + "…）"
		}
		return msg + "（" + s + "）"
	case strings.Contains(s, "401"), strings.Contains(low, "unauthorized"):
		return "认证失败：Token 无效、过期或与集群不匹配"
	case strings.Contains(s, "403"), strings.Contains(low, "forbidden"):
		return "权限不足：ServiceAccount 缺少 list/get 等访问权限"
	default:
		if len(s) > 280 {
			return s[:280] + "…"
		}
		return s
	}
}

func (c *k8sRESTClient) ListNamespaces() ([]map[string]any, error) {
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.getJSON("/api/v1/namespaces", nil, &doc); err != nil {
		return nil, err
	}
	return doc.Items, nil
}

func (c *k8sRESTClient) ListNodes() ([]map[string]any, error) {
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.getJSON("/api/v1/nodes", nil, &doc); err != nil {
		return nil, err
	}
	return doc.Items, nil
}

func (c *k8sRESTClient) ListPods(namespace string, limit int, cont string) (k8sListResult, error) {
	path := "/api/v1/pods"
	if ns := strings.TrimSpace(namespace); ns != "" && ns != "*" && !strings.EqualFold(ns, "all") {
		path = "/api/v1/namespaces/" + url.PathEscape(ns) + "/pods"
	}
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if cont = strings.TrimSpace(cont); cont != "" {
		q.Set("continue", cont)
	}
	var doc struct {
		Metadata struct {
			Continue           string `json:"continue"`
			RemainingItemCount any    `json:"remainingItemCount"`
		} `json:"metadata"`
		Items []map[string]any `json:"items"`
	}
	if err := c.getJSON(path, q, &doc); err != nil {
		return k8sListResult{}, err
	}
	return k8sListResult{
		Items:           doc.Items,
		Continue:        doc.Metadata.Continue,
		RemainingApprox: k8sInt(doc.Metadata.RemainingItemCount),
	}, nil
}

func (c *k8sRESTClient) ListDeployments(namespace string, limit int, cont string) (k8sListResult, error) {
	path := "/apis/apps/v1/deployments"
	if ns := strings.TrimSpace(namespace); ns != "" && ns != "*" && !strings.EqualFold(ns, "all") {
		path = "/apis/apps/v1/namespaces/" + url.PathEscape(ns) + "/deployments"
	}
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if cont = strings.TrimSpace(cont); cont != "" {
		q.Set("continue", cont)
	}
	var doc struct {
		Metadata struct {
			Continue           string `json:"continue"`
			RemainingItemCount any    `json:"remainingItemCount"`
		} `json:"metadata"`
		Items []map[string]any `json:"items"`
	}
	if err := c.getJSON(path, q, &doc); err != nil {
		return k8sListResult{}, err
	}
	return k8sListResult{
		Items:           doc.Items,
		Continue:        doc.Metadata.Continue,
		RemainingApprox: k8sInt(doc.Metadata.RemainingItemCount),
	}, nil
}

func k8sInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func (c *k8sRESTClient) ListEvents(namespace string, limit int) ([]map[string]any, error) {
	path := "/api/v1/events"
	if ns := strings.TrimSpace(namespace); ns != "" && ns != "*" && !strings.EqualFold(ns, "all") {
		path = "/api/v1/namespaces/" + url.PathEscape(ns) + "/events"
	}
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.getJSON(path, q, &doc); err != nil {
		return nil, err
	}
	return doc.Items, nil
}

func (c *k8sRESTClient) PodLogs(namespace, name string, tailLines int) (string, error) {
	ns := strings.TrimSpace(namespace)
	pod := strings.TrimSpace(name)
	if ns == "" || pod == "" {
		return "", fmt.Errorf("namespace and pod name required")
	}
	if tailLines <= 0 {
		tailLines = 200
	}
	q := url.Values{}
	q.Set("tailLines", fmt.Sprintf("%d", tailLines))
	q.Set("timestamps", "true")
	path := "/api/v1/namespaces/" + url.PathEscape(ns) + "/pods/" + url.PathEscape(pod) + "/log"
	raw, _, err := c.do(http.MethodGet, path, q, nil, "")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *k8sRESTClient) GetDeploymentScale(namespace, name string) (int32, error) {
	ns := strings.TrimSpace(namespace)
	dep := strings.TrimSpace(name)
	if ns == "" || dep == "" {
		return 0, fmt.Errorf("namespace and deployment name required")
	}
	path := "/apis/apps/v1/namespaces/" + url.PathEscape(ns) + "/deployments/" + url.PathEscape(dep) + "/scale"
	raw, _, err := c.do(http.MethodGet, path, nil, nil, "")
	if err != nil {
		return 0, err
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, err
	}
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return 0, nil
	}
	switch v := spec["replicas"].(type) {
	case float64:
		return int32(v), nil
	case int:
		return int32(v), nil
	case json.Number:
		n, _ := v.Int64()
		return int32(n), nil
	default:
		return 0, nil
	}
}

func (c *k8sRESTClient) ScaleDeployment(namespace, name string, replicas int32) error {
	ns := strings.TrimSpace(namespace)
	dep := strings.TrimSpace(name)
	if ns == "" || dep == "" {
		return fmt.Errorf("namespace and deployment name required")
	}
	if replicas < 0 {
		return fmt.Errorf("replicas must be >= 0")
	}
	body, _ := json.Marshal(map[string]any{"spec": map[string]any{"replicas": replicas}})
	path := "/apis/apps/v1/namespaces/" + url.PathEscape(ns) + "/deployments/" + url.PathEscape(dep) + "/scale"
	_, _, err := c.do(http.MethodPatch, path, nil, body, "application/merge-patch+json")
	return err
}

func (c *k8sRESTClient) RestartDeployment(namespace, name string) error {
	ns := strings.TrimSpace(namespace)
	dep := strings.TrimSpace(name)
	if ns == "" || dep == "" {
		return fmt.Errorf("namespace and deployment name required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body, _ := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{
						"kubectl.kubernetes.io/restartedAt": now,
					},
				},
			},
		},
	})
	path := "/apis/apps/v1/namespaces/" + url.PathEscape(ns) + "/deployments/" + url.PathEscape(dep)
	_, _, err := c.do(http.MethodPatch, path, nil, body, "application/strategic-merge-patch+json")
	return err
}

func (c *k8sRESTClient) DeletePod(namespace, name string) error {
	ns := strings.TrimSpace(namespace)
	pod := strings.TrimSpace(name)
	if ns == "" || pod == "" {
		return fmt.Errorf("namespace and pod name required")
	}
	path := "/api/v1/namespaces/" + url.PathEscape(ns) + "/pods/" + url.PathEscape(pod)
	_, code, err := c.do(http.MethodDelete, path, nil, nil, "")
	if err != nil {
		return err
	}
	if code >= 300 && code != http.StatusNotFound {
		return fmt.Errorf("delete pod HTTP %d", code)
	}
	return nil
}

// UndoDeploymentRollout patches the deployment template back to the previous ReplicaSet revision.
func (c *k8sRESTClient) UndoDeploymentRollout(namespace, name string) error {
	ns := strings.TrimSpace(namespace)
	dep := strings.TrimSpace(name)
	if ns == "" || dep == "" {
		return fmt.Errorf("namespace and deployment name required")
	}
	depPath := "/apis/apps/v1/namespaces/" + url.PathEscape(ns) + "/deployments/" + url.PathEscape(dep)
	var deploy map[string]any
	if err := c.getJSON(depPath, nil, &deploy); err != nil {
		return err
	}
	meta, _ := deploy["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	raw, _, err := c.do(http.MethodGet, "/apis/apps/v1/namespaces/"+url.PathEscape(ns)+"/replicasets", nil, nil, "")
	if err != nil {
		return err
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	type revRS struct {
		rev  int
		tmpl any
	}
	var owned []revRS
	for _, rs := range list.Items {
		rm, _ := rs["metadata"].(map[string]any)
		owners, _ := rm["ownerReferences"].([]any)
		match := false
		for _, o := range owners {
			om, _ := o.(map[string]any)
			if om == nil {
				continue
			}
			if strings.EqualFold(fmt.Sprint(om["kind"]), "Deployment") && fmt.Sprint(om["uid"]) == uid {
				match = true
				break
			}
			if strings.EqualFold(fmt.Sprint(om["kind"]), "Deployment") && strings.EqualFold(fmt.Sprint(om["name"]), dep) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		ann, _ := rm["annotations"].(map[string]any)
		revStr, _ := ann["deployment.kubernetes.io/revision"].(string)
		rev, _ := strconv.Atoi(revStr)
		spec, _ := rs["spec"].(map[string]any)
		owned = append(owned, revRS{rev: rev, tmpl: spec["template"]})
	}
	if len(owned) < 2 {
		return fmt.Errorf("没有可回滚的历史版本（需要至少 2 个 ReplicaSet）")
	}
	// Sort by revision desc; pick second (previous).
	for i := 0; i < len(owned); i++ {
		for j := i + 1; j < len(owned); j++ {
			if owned[j].rev > owned[i].rev {
				owned[i], owned[j] = owned[j], owned[i]
			}
		}
	}
	prev := owned[1]
	if prev.tmpl == nil {
		return fmt.Errorf("上一版本模板为空")
	}
	body, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": prev.tmpl}})
	_, _, err = c.do(http.MethodPatch, depPath, nil, body, "application/strategic-merge-patch+json")
	return err
}

func writeTempKubeconfig(cfg K8sClusterConfig) (path string, cleanup func(), err error) {
	tmp, err := os.CreateTemp("", "aiops-kubeconfig-*.yaml")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.Remove(tmp.Name()) }
	kc := strings.TrimSpace(cfg.KubeconfigYAML)
	if kc != "" && kc != "****" {
		if _, err := tmp.WriteString(kc); err != nil {
			tmp.Close()
			cleanup()
			return "", nil, err
		}
		tmp.Close()
		return tmp.Name(), cleanup, nil
	}
	ep, err := resolveK8sEndpoint(cfg)
	if err != nil {
		tmp.Close()
		cleanup()
		return "", nil, err
	}
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: %v
  name: aiops
contexts:
- context:
    cluster: aiops
    user: aiops
  name: aiops
current-context: aiops
users:
- name: aiops
  user:
    token: %s
`, ep.Server, ep.Insecure || ep.CACert == "", ep.Token)
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		cleanup()
		return "", nil, err
	}
	tmp.Close()
	return tmp.Name(), cleanup, nil
}

func kubectlBin() (string, error) {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return "", fmt.Errorf("服务端未安装 kubectl（请安装 kubectl）")
	}
	return kubectl, nil
}

// PodExecShort runs a non-interactive command via kubectl if available on the server.
// Native SPDY exec is not bundled; kubectl is the pragmatic path for short diagnostics.
func (c *k8sRESTClient) PodExecShort(namespace, name, command string, timeoutSec int) (string, error) {
	ns := strings.TrimSpace(namespace)
	pod := strings.TrimSpace(name)
	cmd := strings.TrimSpace(command)
	if ns == "" || pod == "" || cmd == "" {
		return "", fmt.Errorf("namespace, pod and command required")
	}
	if len(cmd) > 2000 {
		return "", fmt.Errorf("command too long")
	}
	if timeoutSec < 5 {
		timeoutSec = 15
	}
	if timeoutSec > 60 {
		timeoutSec = 60
	}
	kubectl, err := kubectlBin()
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "aiops-kubeconfig-*.yaml")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: true
  name: aiops
contexts:
- context:
    cluster: aiops
    user: aiops
  name: aiops
current-context: aiops
users:
- name: aiops
  user:
    token: %s
`, c.base, c.token)
	if _, err := tmp.WriteString(kc); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	args := []string{
		"--kubeconfig", tmp.Name(),
		"exec", "-n", ns, pod, "--",
		"sh", "-c", cmd,
	}
	out, err := exec.CommandContext(ctx, kubectl, args...).CombinedOutput()
	text := string(out)
	if len(text) > 256*1024 {
		text = text[:256*1024] + "\n…[truncated]"
	}
	if err != nil {
		if text == "" {
			return "", err
		}
		return text, fmt.Errorf("%w: %s", err, strings.TrimSpace(text))
	}
	return text, nil
}

// ApplyYAML runs kubectl apply -f - (multi-doc YAML). dryRun uses client/server dry-run when set.
func ApplyYAML(cfg K8sClusterConfig, yamlDoc, namespace string, dryRun bool) (string, error) {
	yamlDoc = strings.TrimSpace(yamlDoc)
	if yamlDoc == "" {
		return "", fmt.Errorf("yaml required")
	}
	if len(yamlDoc) > 2<<20 {
		return "", fmt.Errorf("yaml too large (≤2MiB)")
	}
	kubectl, err := kubectlBin()
	if err != nil {
		return "", err
	}
	path, cleanup, err := writeTempKubeconfig(cfg)
	if err != nil {
		return "", err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	args := []string{"--kubeconfig", path, "apply", "-f", "-"}
	if ns := strings.TrimSpace(namespace); ns != "" {
		args = append(args, "-n", ns)
	}
	if dryRun {
		args = append(args, "--dry-run=server")
	}
	cmd := exec.CommandContext(ctx, kubectl, args...)
	cmd.Stdin = strings.NewReader(yamlDoc)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if len(text) > 512*1024 {
		text = text[:512*1024] + "\n…[truncated]"
	}
	if err != nil {
		if text == "" {
			return "", err
		}
		return text, fmt.Errorf("%w: %s", err, strings.TrimSpace(text))
	}
	return text, nil
}

// CreateNamespace creates a namespace via kubectl.
func CreateNamespace(cfg K8sClusterConfig, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 63 {
		return "", fmt.Errorf("invalid namespace")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", fmt.Errorf("namespace 仅允许小写字母/数字/短横线")
	}
	kubectl, err := kubectlBin()
	if err != nil {
		return "", err
	}
	path, cleanup, err := writeTempKubeconfig(cfg)
	if err != nil {
		return "", err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, kubectl, "--kubeconfig", path, "create", "namespace", name).CombinedOutput()
	text := string(out)
	if err != nil {
		if text == "" {
			return "", err
		}
		return text, fmt.Errorf("%w: %s", err, strings.TrimSpace(text))
	}
	return text, nil
}

// ---- kubeconfig (subset) ----

type kubeconfigFile struct {
	APIVersion     string `yaml:"apiVersion"`
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token                 string `yaml:"token"`
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKeyData         string `yaml:"client-key-data"`
		} `yaml:"user"`
	} `yaml:"users"`
}

func parseKubeconfig(raw string) (k8sEndpoint, error) {
	var ep k8sEndpoint
	var kc kubeconfigFile
	if err := yaml.Unmarshal([]byte(raw), &kc); err != nil {
		return ep, fmt.Errorf("kubeconfig 解析失败: %w", err)
	}
	ctxName := strings.TrimSpace(kc.CurrentContext)
	if ctxName == "" && len(kc.Contexts) > 0 {
		ctxName = kc.Contexts[0].Name
	}
	var clusterName, userName string
	for _, ctx := range kc.Contexts {
		if ctx.Name == ctxName {
			clusterName = ctx.Context.Cluster
			userName = ctx.Context.User
			break
		}
	}
	if clusterName == "" {
		return ep, fmt.Errorf("kubeconfig 缺少 current-context")
	}
	for _, cl := range kc.Clusters {
		if cl.Name != clusterName {
			continue
		}
		ep.Server = strings.TrimRight(strings.TrimSpace(cl.Cluster.Server), "/")
		ep.Insecure = cl.Cluster.InsecureSkipTLSVerify
		if cl.Cluster.CertificateAuthorityData != "" {
			pem, err := base64.StdEncoding.DecodeString(cl.Cluster.CertificateAuthorityData)
			if err != nil {
				return ep, fmt.Errorf("CA data 无效: %w", err)
			}
			ep.CACert = string(pem)
		}
		break
	}
	for _, u := range kc.Users {
		if u.Name != userName {
			continue
		}
		ep.Token = strings.TrimSpace(u.User.Token)
		if u.User.ClientCertificateData != "" {
			pem, err := base64.StdEncoding.DecodeString(u.User.ClientCertificateData)
			if err != nil {
				return ep, fmt.Errorf("client cert 无效: %w", err)
			}
			ep.ClientCertPEM = string(pem)
		}
		if u.User.ClientKeyData != "" {
			pem, err := base64.StdEncoding.DecodeString(u.User.ClientKeyData)
			if err != nil {
				return ep, fmt.Errorf("client key 无效: %w", err)
			}
			ep.ClientKeyPEM = string(pem)
		}
		break
	}
	if ep.Server == "" {
		return ep, fmt.Errorf("kubeconfig 未找到 server")
	}
	if ep.Token == "" && (ep.ClientCertPEM == "" || ep.ClientKeyPEM == "") {
		return ep, fmt.Errorf("kubeconfig 用户缺少 token 或客户端证书")
	}
	if ep.CACert == "" && !ep.Insecure {
		return ep, fmt.Errorf("kubeconfig 未含 CA 且未设置 insecure-skip-tls-verify")
	}
	return ep, nil
}

// summarize helpers for API responses (flat rows for UI tables).

func k8sMetaName(obj map[string]any) (ns, name string) {
	md, _ := obj["metadata"].(map[string]any)
	if md == nil {
		return "", ""
	}
	name, _ = md["name"].(string)
	ns, _ = md["namespace"].(string)
	return ns, name
}

func k8sPodPhase(obj map[string]any) string {
	st, _ := obj["status"].(map[string]any)
	if st == nil {
		return ""
	}
	p, _ := st["phase"].(string)
	return p
}

func k8sNodeReady(obj map[string]any) string {
	st, _ := obj["status"].(map[string]any)
	if st == nil {
		return "Unknown"
	}
	conds, _ := st["conditions"].([]any)
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m == nil {
			continue
		}
		if m["type"] == "Ready" {
			if m["status"] == "True" {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

func k8sDeployReplicas(obj map[string]any) (desired, ready, available int) {
	spec, _ := obj["spec"].(map[string]any)
	st, _ := obj["status"].(map[string]any)
	if spec != nil {
		switch v := spec["replicas"].(type) {
		case float64:
			desired = int(v)
		case int:
			desired = v
		}
	}
	if st != nil {
		switch v := st["readyReplicas"].(type) {
		case float64:
			ready = int(v)
		case int:
			ready = v
		}
		switch v := st["availableReplicas"].(type) {
		case float64:
			available = int(v)
		case int:
			available = v
		}
	}
	return
}
