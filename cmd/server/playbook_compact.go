package main

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	playbookOutputPreview    = 4 << 10 // 4 KiB preview for compact poll / UI
	playbookHostInspectStore = 8 << 10 // keep short preview in playbook after ingest
	playbookPersistMinGap    = 3 * time.Second
)

// clonePlaybookExecution deep-copies host results so callers can mutate safely.
func clonePlaybookExecution(e PlaybookExecution) PlaybookExecution {
	out := e
	if e.HostResults == nil {
		return out
	}
	out.HostResults = make(map[string]HostExecResult, len(e.HostResults))
	for k, v := range e.HostResults {
		v2 := v
		if len(v.Steps) > 0 {
			v2.Steps = append([]StepResult(nil), v.Steps...)
		}
		out.HostResults[k] = v2
	}
	return out
}

// compactPlaybookExecution truncates step/host outputs for lightweight polling.
// Full output remains in memory/PG; FE requests without compact when expanding.
func compactPlaybookExecution(e PlaybookExecution, preview int) PlaybookExecution {
	if preview <= 0 {
		preview = playbookOutputPreview
	}
	out := clonePlaybookExecution(e)
	for hid, hr := range out.HostResults {
		hr.Output = truncateUTF8(hr.Output, preview)
		for i := range hr.Steps {
			hr.Steps[i].Output = truncateUTF8(hr.Steps[i].Output, preview)
		}
		out.HostResults[hid] = hr
	}
	return out
}

// summarizePlaybookExecution strips bulky outputs for history list APIs.
func summarizePlaybookExecution(e PlaybookExecution) PlaybookExecution {
	out := clonePlaybookExecution(e)
	for hid, hr := range out.HostResults {
		hr.Output = ""
		for i := range hr.Steps {
			hr.Steps[i].Output = ""
		}
		out.HostResults[hid] = hr
	}
	return out
}

func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Prefer rune-safe cut near max bytes.
	if utf8.ValidString(s[:max]) {
		return s[:max] + "\n…(truncated)"
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut <= 0 {
		cut = max
	}
	return s[:cut] + "\n…(truncated)"
}

func truncatePlaybookStoreOutput(module, output string) string {
	mod := strings.TrimSpace(module)
	limit := playbookOutputPreview
	if mod == "host_inspect" || mod == "host_security_scan" {
		limit = playbookHostInspectStore
	}
	if len(output) <= limit {
		return output
	}
	note := ""
	if mod == "host_inspect" {
		note = "\n\n（完整巡检报告已写入「主机巡检」；此处仅保留预览以降低多机执行卡顿）"
	} else if mod == "host_security_scan" {
		note = "\n\n（完整扫描结果见「主机安全」；此处仅保留预览）"
	} else {
		note = "\n\n…(truncated)"
	}
	return truncateUTF8(output, limit) + note
}

// playbookHeavyModule returns true when fleet concurrency should be capped lower.
func playbookHeavyModule(module string) bool {
	switch strings.TrimSpace(module) {
	case "host_inspect", "host_security_scan", "clamav_scan":
		return true
	default:
		return false
	}
}

func playbookHasHeavySteps(steps []PlaybookStep) bool {
	for _, st := range steps {
		if playbookHeavyModule(st.Module) {
			return true
		}
	}
	return false
}

// Debounced PG upsert for playbook executions (in-memory stays real-time).
var (
	pbPersistMu   sync.Mutex
	pbPersistLast = map[int64]time.Time{}
)

// 去抖表按**执行实例 id** 建键，而 id 只增不减：一台跑了半年的服务端上，这张表会
// 攒下每一次剧本执行的时间戳，永远不删。单条不大，但它是纯粹的只涨不落——
// 超过阈值就把早已超过去抖窗口的键清掉（那些键留着也起不到任何去抖作用）。
const pbPersistLastMax = 4096

func pbPrunePersistLocked(now time.Time) {
	if len(pbPersistLast) <= pbPersistLastMax {
		return
	}
	for id, t := range pbPersistLast {
		if now.Sub(t) >= playbookPersistMinGap {
			delete(pbPersistLast, id)
		}
	}
}

func (s *Server) persistPlaybookExecutionDebounced(id int64, force bool) {
	if s == nil || s.pg == nil || id == 0 {
		return
	}
	now := time.Now()
	if !force {
		pbPersistMu.Lock()
		last := pbPersistLast[id]
		if now.Sub(last) < playbookPersistMinGap {
			pbPersistMu.Unlock()
			return
		}
		pbPrunePersistLocked(now)
		pbPersistLast[id] = now
		pbPersistMu.Unlock()
	} else {
		pbPersistMu.Lock()
		pbPrunePersistLocked(now)
		pbPersistLast[id] = now
		pbPersistMu.Unlock()
	}
	s.persistPlaybookExecution(id)
}
