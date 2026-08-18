package main

import (
	"fmt"
	"strings"
	"time"
)

// registerPlatformTools 让 AI 能查平台自己的毛病。
//
// 这是「医生也得给自己看病」在 AI 侧的落点：此前对话里能问遍主机、容器、K8s、告警、
// 事件，唯独问不到「平台自身有没有出问题」——而排障时这恰恰是要先排除的一项。指标断了
// 到底是主机没上报，还是写入泵在 panic 重启？升级没动静到底是没触发，还是连续失败被
// 熔断了？没有这个工具，AI 只能从「别人的症状」倒推，答不到根因上。
func (h *SreyunCore) registerPlatformTools() {
	h.tools["query_platform_faults"] = SreyunTool{
		Name: "query_platform_faults",
		Description: "查询**平台自身**的故障（不是被监控主机的故障）：后台循环 panic、" +
			"持久层写入失败、Agent 升级连续同因失败等，已按指纹聚合并带出现次数与证据。" +
			"排查「监控/告警/升级/曲线本身不工作」时**先用本工具**排除平台自身原因，" +
			"再去查被监控对象。component 可选过滤：loop|agent_update|pg|vm|notify|ai|scan。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"component": map[string]string{"type": "string", "description": "只看某个子系统，空=全部"},
				"level":     map[string]string{"type": "string", "description": "critical|warning，空=全部"},
				"limit":     map[string]string{"type": "integer", "description": "返回条数，默认 20，最大 100"},
			},
		},
		Execute: h.execQueryPlatformFaults,
	}
}

func (h *SreyunCore) execQueryPlatformFaults(args map[string]any) (string, error) {
	if h == nil || h.s == nil || h.s.faults == nil {
		return "", fmt.Errorf("平台自检不可用")
	}
	component := strings.ToLower(strings.TrimSpace(argString(args, "component")))
	level := strings.ToLower(strings.TrimSpace(argString(args, "level")))
	limit := argInt(args, "limit", 20)
	if limit > 100 {
		limit = 100
	}
	all := h.s.faults.snapshot(0)
	type row struct {
		Component  string `json:"component"`
		Kind       string `json:"kind"`
		Level      string `json:"level"`
		Host       string `json:"host,omitempty"`
		Count      int    `json:"count"`
		FirstAt    string `json:"first_at"`
		LastAt     string `json:"last_at"`
		Message    string `json:"message"`
		Evidence   string `json:"evidence,omitempty"`
		IncidentID int64  `json:"incident_id,omitempty"`
	}
	out := make([]row, 0, limit)
	for _, f := range all {
		if component != "" && !strings.EqualFold(f.Component, component) {
			continue
		}
		if level != "" && !strings.EqualFold(f.Level, level) {
			continue
		}
		out = append(out, row{
			Component: f.Component, Kind: f.Kind, Level: f.Level,
			Host:  firstNonEmpty(f.Hostname, f.HostID),
			Count: f.Count,
			// 时间给人读的格式：模型对 unix 秒的推理经常出错，而这里的相对先后是关键信息。
			FirstAt: time.Unix(f.FirstAt, 0).Format("2006-01-02 15:04:05"),
			LastAt:  time.Unix(f.LastAt, 0).Format("2006-01-02 15:04:05"),
			Message: trimLine(f.Message, 800),
			// 证据（堆栈 / 助手日志尾巴）是这个工具存在的意义，给足长度但仍封顶。
			Evidence:   trimLine(f.Evidence, 2000),
			IncidentID: f.IncidentID,
		})
		if len(out) >= limit {
			break
		}
	}
	hint := "平台自身当前没有记录在案的故障；若现象仍在，应从被监控对象一侧继续排查。"
	if len(out) > 0 {
		hint = "以上是平台**自身**的故障。count 大于 1 说明它在重复发生；带 incident_id 的已经进了 SRE 事件闭环，可用 diagnose_incident 深入。"
	}
	return toolResultJSONBounded(map[string]any{
		"ok": true, "count": len(out), "faults": out,
		"threshold_for_incident": platformFaultIncidentThreshold,
		"hint":                   hint,
	}, 0), nil
}
