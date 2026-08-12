package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (h *SreyunCore) registerResourceTools() {
	h.tools["list_hosts"] = SreyunTool{
		Name: "list_hosts",
		Description: "列出纳管主机（轻量：id/主机名/IP/在线状态/系统/CPU·内存摘要）。" +
			"需要「全部主机清单」或查找 host_id 时优先用本工具；不要用 query_containers 代替列主机。" +
			"支持 status=all|online|offline、q 搜索、limit/offset 分页。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]string{"type": "string", "description": "all|online|offline，默认 all"},
				"q":      map[string]string{"type": "string", "description": "按主机名/IP/id/分类模糊匹配"},
				"limit":  map[string]string{"type": "integer", "description": "每页条数，默认 50，最大 200"},
				"offset": map[string]string{"type": "integer", "description": "分页偏移，默认 0"},
			},
		},
		Execute: h.execListHosts,
	}
	h.tools["query_containers"] = SreyunTool{
		Name: "query_containers",
		Description: "查询 Docker/Podman 容器。" +
			"不传 host_id：返回各主机摘要（数量/运行中/退出，不含完整容器列表）；" +
			"传 host_id：返回该主机容器明细（可用 limit/offset/status 分页过滤）。" +
			"列全部主机请用 list_hosts，勿对本工具做全量明细拉取。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host_id": map[string]string{"type": "string", "description": "主机 ID；空=全平台摘要"},
				"status":  map[string]string{"type": "string", "description": "明细过滤：all|running|exited|paused，默认 all"},
				"limit":   map[string]string{"type": "integer", "description": "明细每页容器数，默认 50，最大 200"},
				"offset":  map[string]string{"type": "integer", "description": "明细分页偏移"},
				"detail":  map[string]string{"type": "boolean", "description": "无 host_id 时若 true 仍禁止全量展开；请指定 host_id"},
			},
		},
		Execute: h.execQueryContainers,
	}
	h.tools["query_k8s"] = SreyunTool{
		Name: "query_k8s",
		Description: "查询已登记的 Kubernetes 集群资源。kind=clusters|nodes|pods|deployments|events|log。" +
			"需要 cluster_id（clusters 除外）。排查集群/Pod/Node 时优先用此工具，勿依赖服务端本机 kubectl。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":       map[string]string{"type": "string", "description": "clusters|nodes|pods|deployments|events|log"},
				"cluster_id": map[string]string{"type": "string", "description": "集群 ID"},
				"namespace":  map[string]string{"type": "string", "description": "命名空间，空=全部/默认"},
				"pod":        map[string]string{"type": "string", "description": "kind=log 时的 Pod 名"},
				"limit":      map[string]string{"type": "integer", "description": "列表上限"},
			},
		},
		Execute: h.execQueryK8s,
	}
	h.tools["k8s_scale"] = SreyunTool{
		Name:        "k8s_scale",
		Description: "调整 Deployment 副本数（写操作，需审批策略允许）。参数：cluster_id、namespace、name、replicas。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cluster_id": map[string]string{"type": "string"},
				"namespace":  map[string]string{"type": "string"},
				"name":       map[string]string{"type": "string"},
				"replicas":   map[string]string{"type": "integer"},
			},
			"required": []string{"cluster_id", "namespace", "name", "replicas"},
		},
		Execute: h.execK8sScale,
	}
	h.tools["k8s_restart"] = SreyunTool{
		Name:        "k8s_restart",
		Description: "对 Deployment 执行 rollout restart（写操作）。参数：cluster_id、namespace、name。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cluster_id": map[string]string{"type": "string"},
				"namespace":  map[string]string{"type": "string"},
				"name":       map[string]string{"type": "string"},
			},
			"required": []string{"cluster_id", "namespace", "name"},
		},
		Execute: h.execK8sRestart,
	}
	h.tools["locate_resource"] = SreyunTool{
		Name: "locate_resource",
		Description: "跨层定位资源：输入 host:<id> / vm:<host>/<vm> / container:<host>/<id> / pod:<cluster>/<ns>/<name> / svc:<name>，" +
			"返回硬件→虚拟机→主机→容器/Pod 关联链与告警摘要。容器/服务异常时先用它定位落点。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]string{"type": "string", "description": "资源引用，如 host:abc、pod:prod/default/api-0"},
			},
			"required": []string{"ref"},
		},
		Execute: h.execLocateResource,
	}
}

func (h *SreyunCore) execListHosts(args map[string]any) (string, error) {
	if h.s == nil || h.s.store == nil {
		return toolResultJSON(map[string]any{"ok": false, "error": "store unavailable"}), nil
	}
	status := strings.ToLower(strings.TrimSpace(argString(args, "status")))
	if status == "" {
		status = "all"
	}
	q := strings.ToLower(strings.TrimSpace(argString(args, "q")))
	limit := argInt(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	offset := argInt(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	actor := scopeActorFromArgs(args)
	hosts := h.s.store.ListHosts()
	if actor != "" {
		if u, ok := h.s.cfg.UserByName(actor); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
			filtered := make([]*Host, 0, len(hosts))
			for _, ht := range hosts {
				if ht != nil && h.s.userCanAccessHost(u, ht.ID) {
					filtered = append(filtered, ht)
				}
			}
			hosts = filtered
		}
	}
	now := time.Now().Unix()
	offlineSec := int64(120)
	if h.s.cfg != nil {
		if sec := int64(h.s.cfg.Thresholds().OfflineAfter.Seconds()); sec > 0 {
			offlineSec = sec
		}
	}
	type row struct {
		ID           string  `json:"id"`
		Hostname     string  `json:"hostname"`
		IP           string  `json:"ip,omitempty"`
		OS           string  `json:"os,omitempty"`
		Platform     string  `json:"platform,omitempty"`
		Category     string  `json:"category,omitempty"`
		Status       string  `json:"status"`
		LastSeenAgoS int64   `json:"last_seen_ago_s,omitempty"`
		CPUPercent   float64 `json:"cpu_percent,omitempty"`
		MemPercent   float64 `json:"mem_percent,omitempty"`
		AgentVersion string  `json:"agent_version,omitempty"`
	}
	matched := make([]row, 0, len(hosts))
	onlineN, offlineN := 0, 0
	for _, ht := range hosts {
		if ht == nil {
			continue
		}
		online := ht.LastSeen > 0 && now-ht.LastSeen <= offlineSec
		st := "offline"
		if online {
			st = "online"
			onlineN++
		} else {
			offlineN++
		}
		if status == "online" && !online {
			continue
		}
		if status == "offline" && online {
			continue
		}
		if q != "" {
			blob := strings.ToLower(strings.Join([]string{ht.ID, ht.Hostname, ht.IP, ht.OS, ht.Category, ht.Platform}, " "))
			if !strings.Contains(blob, q) {
				continue
			}
		}
		r := row{
			ID: ht.ID, Hostname: ht.Hostname, IP: ht.IP, OS: ht.OS, Platform: ht.Platform,
			Category: ht.Category, Status: st, AgentVersion: ht.AgentVersion,
		}
		if ht.LastSeen > 0 {
			r.LastSeenAgoS = now - ht.LastSeen
		}
		if ht.Latest != nil {
			r.CPUPercent = ht.Latest.CPUPercent
			r.MemPercent = ht.Latest.MemPercent
		}
		matched = append(matched, r)
	}
	totalMatched := len(matched)
	if offset > totalMatched {
		offset = totalMatched
	}
	end := offset + limit
	if end > totalMatched {
		end = totalMatched
	}
	page := matched[offset:end]
	next := -1
	if end < totalMatched {
		next = end
	}
	return toolResultJSON(map[string]any{
		"ok":          true,
		"total_hosts": len(hosts),
		"online":      onlineN,
		"offline":     offlineN,
		"matched":     totalMatched,
		"limit":       limit,
		"offset":      offset,
		"next_offset": next,
		"hosts":       page,
		"hint":        "单机健康用 check_host_health(host_id)；容器明细用 query_containers(host_id=...)",
	}), nil
}

func containerInventorySummary(row map[string]any) map[string]any {
	running, exited, other, samples := countContainerStates(row["containers"])
	total := running + exited + other
	if n, ok := row["container_count"].(int); ok && n > total {
		total = n
	}
	out := map[string]any{
		"host_id":         row["host_id"],
		"host_name":       row["host_name"],
		"runtime":         row["runtime"],
		"container_count": total,
		"running":         running,
		"exited":          exited,
		"other":           other,
		"updated_at":      row["updated_at"],
	}
	if len(samples) > 0 {
		out["sample_names"] = samples
	}
	return out
}

func countContainerStates(containers any) (running, exited, other int, samples []string) {
	arr, ok := containers.([]any)
	if !ok {
		return
	}
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			other++
			continue
		}
		state := strings.ToLower(firstNonEmpty(fmt.Sprint(m["state"]), fmt.Sprint(m["Status"]), fmt.Sprint(m["status"])))
		name := strings.TrimSpace(firstNonEmpty(fmt.Sprint(m["name"]), fmt.Sprint(m["Names"]), fmt.Sprint(m["id"]), fmt.Sprint(m["Id"])))
		if name != "" && name != "<nil>" && len(samples) < 5 {
			samples = append(samples, name)
		}
		switch {
		case strings.Contains(state, "run"):
			running++
		case strings.Contains(state, "exit") || strings.Contains(state, "dead") || strings.Contains(state, "stop"):
			exited++
		default:
			other++
		}
	}
	return
}

func filterContainersPage(containers any, status string, limit, offset int) (page []any, total int, next int) {
	arr, _ := containers.([]any)
	if arr == nil {
		arr = []any{}
	}
	status = strings.ToLower(strings.TrimSpace(status))
	filtered := make([]any, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		st := strings.ToLower(firstNonEmpty(fmt.Sprint(m["state"]), fmt.Sprint(m["Status"]), fmt.Sprint(m["status"])))
		switch status {
		case "", "all":
			filtered = append(filtered, compactContainerRow(m))
		case "running":
			if strings.Contains(st, "run") {
				filtered = append(filtered, compactContainerRow(m))
			}
		case "exited", "stopped":
			if strings.Contains(st, "exit") || strings.Contains(st, "dead") || strings.Contains(st, "stop") {
				filtered = append(filtered, compactContainerRow(m))
			}
		case "paused":
			if strings.Contains(st, "pause") {
				filtered = append(filtered, compactContainerRow(m))
			}
		default:
			filtered = append(filtered, compactContainerRow(m))
		}
	}
	total = len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page = filtered[offset:end]
	next = -1
	if end < total {
		next = end
	}
	return page, total, next
}

func compactContainerRow(m map[string]any) map[string]any {
	name := firstNonEmpty(fmt.Sprint(m["name"]), fmt.Sprint(m["Names"]))
	id := firstNonEmpty(fmt.Sprint(m["id"]), fmt.Sprint(m["Id"]), fmt.Sprint(m["ID"]))
	state := firstNonEmpty(fmt.Sprint(m["state"]), fmt.Sprint(m["Status"]), fmt.Sprint(m["status"]))
	image := firstNonEmpty(fmt.Sprint(m["image"]), fmt.Sprint(m["Image"]))
	created := firstNonEmpty(fmt.Sprint(m["created"]), fmt.Sprint(m["Created"]), fmt.Sprint(m["CreatedAt"]))
	out := map[string]any{}
	if name != "" && name != "<nil>" {
		out["name"] = name
	}
	if id != "" && id != "<nil>" {
		if len(id) > 12 {
			out["id"] = id[:12]
		} else {
			out["id"] = id
		}
	}
	if state != "" && state != "<nil>" {
		out["state"] = state
	}
	if image != "" && image != "<nil>" {
		out["image"] = image
	}
	if created != "" && created != "<nil>" {
		out["created"] = created
	}
	if p := m["ports"]; p != nil {
		out["ports"] = p
	} else if p := m["Ports"]; p != nil {
		out["ports"] = p
	}
	if proj := firstNonEmpty(fmt.Sprint(m["compose_project"]), fmt.Sprint(m["ComposeProject"])); proj != "" && proj != "<nil>" {
		out["compose_project"] = proj
	}
	if svc := firstNonEmpty(fmt.Sprint(m["compose_service"]), fmt.Sprint(m["ComposeService"])); svc != "" && svc != "<nil>" {
		out["compose_service"] = svc
	}
	return out
}

func (h *SreyunCore) execQueryContainers(args map[string]any) (string, error) {
	if h.s == nil || h.s.pg == nil {
		return toolResultJSON(map[string]any{"ok": false, "error": "无 PostgreSQL，无法查询容器清单"}), nil
	}
	hostID := strings.TrimSpace(argString(args, "host_id"))
	actor := scopeActorFromArgs(args)
	status := argString(args, "status")
	limit := argInt(args, "limit", 50)
	offset := argInt(args, "offset", 0)

	if hostID == "" {
		rows, err := h.s.pg.getAllContainerInventories()
		if err != nil {
			return "", err
		}
		if actor != "" {
			if u, ok := h.s.cfg.UserByName(actor); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
				filtered := rows[:0]
				for _, row := range rows {
					hid, _ := row["host_id"].(string)
					if hid != "" && h.s.userCanAccessHost(u, hid) {
						filtered = append(filtered, row)
					}
				}
				rows = filtered
			}
		}
		summaries := make([]map[string]any, 0, len(rows))
		totalCtr, runningSum := 0, 0
		for _, row := range rows {
			sum := containerInventorySummary(row)
			summaries = append(summaries, sum)
			if n, ok := sum["container_count"].(int); ok {
				totalCtr += n
			}
			if n, ok := sum["running"].(int); ok {
				runningSum += n
			}
		}
		return toolResultJSON(map[string]any{
			"ok":              true,
			"mode":            "summary",
			"hosts":           len(summaries),
			"containers":      totalCtr,
			"running":         runningSum,
			"items":           summaries,
			"hint":            "查看某主机容器明细：query_containers(host_id=...)。列全部主机：list_hosts。",
			"detail_rejected": argBool(args, "detail", false),
		}), nil
	}

	if hst := h.resolveHostRef(hostID); hst != nil {
		hostID = hst.ID
	}
	if !h.actorCanAccessHost(actor, hostID) {
		return toolResultJSON(map[string]any{"ok": false, "error": "无权访问该主机的容器清单"}), nil
	}
	inv, ok := h.s.pg.getContainerInventory(hostID)
	if !ok {
		return toolResultJSON(map[string]any{
			"ok": true, "mode": "detail", "host_id": hostID,
			"containers": []any{}, "total": 0,
			"hint": "该主机暂无容器清单（Agent 未上报或无 Docker/Podman）",
		}), nil
	}
	page, total, next := filterContainersPage(inv["containers"], status, limit, offset)
	out := map[string]any{
		"ok":              true,
		"mode":            "detail",
		"host_id":         inv["host_id"],
		"host_name":       inv["host_name"],
		"runtime":         inv["runtime"],
		"container_count": inv["container_count"],
		"updated_at":      inv["updated_at"],
		"status_filter":   firstNonEmpty(strings.ToLower(strings.TrimSpace(status)), "all"),
		"limit":           limit,
		"offset":          offset,
		"total_matched":   total,
		"next_offset":     next,
		"containers":      page,
	}
	if next >= 0 {
		out["hint"] = fmt.Sprintf("还有更多：query_containers(host_id=%s, offset=%d, limit=%d)", hostID, next, limit)
	}
	return toolResultJSONBounded(out, toolJSONSoftLimit), nil
}

func (h *SreyunCore) execQueryK8s(args map[string]any) (string, error) {
	kind, _ := args["kind"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "clusters"
	}
	if kind == "clusters" {
		list := h.s.cfg.ListK8sClusters()
		out := make([]map[string]any, 0, len(list))
		for _, c := range list {
			out = append(out, map[string]any{"id": c.ID, "name": c.Name, "enabled": c.Enabled, "default_namespace": c.DefaultNS})
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return string(b), nil
	}
	cid, _ := args["cluster_id"].(string)
	c, ok := h.s.cfg.GetK8sCluster(cid)
	if !ok || !c.Enabled {
		return "", fmt.Errorf("cluster not found or disabled")
	}
	cli, err := newK8sRESTClient(c)
	if err != nil {
		return "", err
	}
	ns, _ := args["namespace"].(string)
	if ns == "" {
		ns = c.DefaultNS
	}
	limit := 100
	if v, ok := args["limit"].(float64); ok && int(v) > 0 {
		limit = int(v)
	}
	if v, ok := args["limit"].(string); ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			limit = n
		}
	}
	switch kind {
	case "nodes":
		items, err := cli.ListNodes()
		if err != nil {
			return "", err
		}
		rows := []map[string]any{}
		for _, it := range items {
			_, name := k8sMetaName(it)
			row := map[string]any{"name": name, "ready": k8sNodeReady(it)}
			h.s.enrichK8sNodeRow(it, row)
			rows = append(rows, row)
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		return string(b), nil
	case "pods":
		res, err := cli.ListPods(ns, limit, "")
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(summarizeK8sPods(h.s, res.Items), "", "  ")
		return string(b), nil
	case "deployments":
		res, err := cli.ListDeployments(ns, limit, "")
		if err != nil {
			return "", err
		}
		rows := []map[string]any{}
		for _, it := range res.Items {
			dns, name := k8sMetaName(it)
			d, ready, avail := k8sDeployReplicas(it)
			rows = append(rows, map[string]any{"namespace": dns, "name": name, "replicas": d, "ready": ready, "available": avail})
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		return string(b), nil
	case "events":
		items, err := cli.ListEvents(ns, limit)
		if err != nil {
			return "", err
		}
		return toolResultJSONBounded(map[string]any{
			"ok": true, "kind": "events", "namespace": ns, "count": len(items), "items": items,
		}, toolJSONSoftLimit), nil
	case "log":
		pod, _ := args["pod"].(string)
		if pod == "" || ns == "" {
			return "", fmt.Errorf("log 需要 namespace 与 pod")
		}
		text, err := cli.PodLogs(ns, pod, 200)
		if err != nil {
			return "", err
		}
		truncated := false
		if len(text) > toolJSONSoftLimit {
			text = text[:toolJSONSoftLimit]
			truncated = true
		}
		return toolResultJSON(map[string]any{
			"ok": true, "kind": "log", "namespace": ns, "pod": pod,
			"truncated": truncated, "text": text,
		}), nil
	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}
}

func summarizeK8sPods(s *Server, items []map[string]any) []map[string]any {
	rows := make([]map[string]any, 0, len(items))
	idx := s.buildK8sHostIndex()
	for _, it := range items {
		pns, name := k8sMetaName(it)
		node := ""
		if spec, _ := it["spec"].(map[string]any); spec != nil {
			node, _ = spec["nodeName"].(string)
		}
		row := map[string]any{"namespace": pns, "name": name, "phase": k8sPodPhase(it), "node": node}
		if node != "" {
			if hid, hname := s.hostIDForK8sNodeNameWithIndex(node, idx); hid != "" {
				row["linked_host_id"] = hid
				row["linked_host_name"] = hname
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (h *SreyunCore) sreyunWriteBlocked(tool, detail string, args map[string]any) (string, bool) {
	approvalID, _ := args["approval_id"].(string)
	argsHash := argsHashForApproval(tool, args)
	cfg := AIConfig{}
	if h.s != nil && h.s.cfg != nil {
		cfg = h.s.cfg.AIConfig()
	}
	// Default: require short-lived approval_id.
	// Escape hatch only when hermes_auto_approve=true AND write_tools_require_approval=false.
	requireApproval := true
	if cfg.SreyunAutoApprove && !cfg.WriteToolsRequireApproval {
		requireApproval = false
	}
	if !requireApproval {
		if h.s.aiGov != nil {
			h.s.aiGov.recordTool(aiToolAuditEntry{
				Actor: "sreyun", Tool: tool, Action: "auto_approve", Approved: true,
				Detail: detail + " args_hash=" + argsHash + " reason=ai_auto_approve",
			})
		}
		return "", false
	}
	if strings.TrimSpace(approvalID) == "" {
		if h.s.aiGov != nil {
			h.s.aiGov.recordTool(aiToolAuditEntry{
				Actor: "sreyun", Tool: tool, Action: tool, Approved: false, Blocked: true,
				Detail: detail + " args_hash=" + argsHash + " reason=missing_approval_id",
			})
		}
		return fmt.Sprintf("工具 %s 属于高风险写操作：请先 POST /api/v1/ai/write-approval 签发 approval_id（可附 args_hash=%s），再调用工具。", tool, argsHash), true
	}
	if h.s.aiGov == nil || !h.s.aiGov.consumeWriteApproval(approvalID, tool, argsHash) {
		if h.s.aiGov != nil {
			h.s.aiGov.recordTool(aiToolAuditEntry{
				Actor: "sreyun", Tool: tool, Action: "bad_approval", Approved: false, Blocked: true,
				Detail: detail + " approval_id=" + approvalID,
			})
		}
		return "approval_id 无效、已过期或不匹配工具参数。", true
	}
	h.s.aiGov.recordTool(aiToolAuditEntry{
		Actor: "sreyun", Tool: tool, Action: "approved_token", Approved: true,
		Detail: detail + " approval_id=" + approvalID,
	})
	return "", false
}

func (h *SreyunCore) execK8sScale(args map[string]any) (string, error) {
	cid, _ := args["cluster_id"].(string)
	ns, _ := args["namespace"].(string)
	name, _ := args["name"].(string)
	if msg, blocked := h.sreyunWriteBlocked("k8s_scale", cid+"/"+ns+"/"+name, args); blocked {
		return msg, nil
	}
	var replicas int32
	switch v := args["replicas"].(type) {
	case float64:
		replicas = int32(v)
	case string:
		n, _ := strconv.Atoi(v)
		replicas = int32(n)
	case int:
		replicas = int32(v)
	}
	c, ok := h.s.cfg.GetK8sCluster(cid)
	if !ok || !c.Enabled {
		return "", fmt.Errorf("cluster not found or disabled")
	}
	cli, err := newK8sRESTClient(c)
	if err != nil {
		return "", err
	}
	old, _ := cli.GetDeploymentScale(ns, name)
	if err := cli.ScaleDeployment(ns, name, replicas); err != nil {
		return "", err
	}
	if h.s.store != nil {
		h.s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: "sreyun",
			Message: fmt.Sprintf("Sreyun K8s Scale：集群=%s ns=%s deploy=%s replicas=%d→%d", c.Name, ns, name, old, replicas)})
	}
	return fmt.Sprintf("ok scale %s/%s %d→%d", ns, name, old, replicas), nil
}

func (h *SreyunCore) execK8sRestart(args map[string]any) (string, error) {
	cid, _ := args["cluster_id"].(string)
	ns, _ := args["namespace"].(string)
	name, _ := args["name"].(string)
	if msg, blocked := h.sreyunWriteBlocked("k8s_restart", cid+"/"+ns+"/"+name, args); blocked {
		return msg, nil
	}
	c, ok := h.s.cfg.GetK8sCluster(cid)
	if !ok || !c.Enabled {
		return "", fmt.Errorf("cluster not found or disabled")
	}
	cli, err := newK8sRESTClient(c)
	if err != nil {
		return "", err
	}
	if err := cli.RestartDeployment(ns, name); err != nil {
		return "", err
	}
	if h.s.store != nil {
		h.s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: "sreyun",
			Message: fmt.Sprintf("Sreyun K8s Restart：集群=%s ns=%s deploy=%s", c.Name, ns, name)})
	}
	return fmt.Sprintf("ok restart %s/%s", ns, name), nil
}

func (h *SreyunCore) execLocateResource(args map[string]any) (string, error) {
	ref, _ := args["ref"].(string)
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("ref required")
	}
	res := h.s.locateResource(ref)
	actor := scopeActorFromArgs(args)
	hid := res.HostID
	if hid == "" {
		hid = res.HyperVHostID
	}
	if hid != "" && !h.actorCanAccessHost(actor, hid) {
		return "无权访问该资源所属主机", nil
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	return string(b), nil
}
