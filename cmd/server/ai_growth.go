package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// AI growth helpers: preferences, forecast bias notes, ops-pattern → skill proposals.
// All persist via existing rememberAI / skills tables — no new dependencies.

type opsPatternHit struct {
	Key   string
	Count int
	Last  int64
	Steps string
}

type aiGrowthHub struct {
	mu       sync.Mutex
	patterns map[string]*opsPatternHit // actor|fingerprint → hit
}

// 运维路径指纹是自由文本（由诊断步骤拼出来），键的取值空间没有上限，而这张表
// 原本只增不减：跑上几个月，一个个几百字节的 Steps 就在内存里一直堆着，
// 且**永远不会有人发现**——它不出现在任何指标或页面上。
//
// 两条界限一起用：太久没再出现的直接丢（重复运维路径的意义就在"最近还在重复"，
// 半个月前只出现过一次的那条早就没有参考价值了），以及一个硬上限兜底，
// 防止短时间内涌入大量互不相同的指纹。
const (
	opsPatternTTLSec  = 14 * 24 * 3600
	opsPatternMaxKeys = 5000
)

// pruneLocked 丢掉过期条目；仍然超限时按最后出现时间从旧到新继续丢。
// 调用方必须已持有 h.mu。
func (h *aiGrowthHub) pruneLocked(now int64) {
	for k, v := range h.patterns {
		if v == nil || now-v.Last > opsPatternTTLSec {
			delete(h.patterns, k)
		}
	}
	if len(h.patterns) <= opsPatternMaxKeys {
		return
	}
	type kv struct {
		key  string
		last int64
	}
	all := make([]kv, 0, len(h.patterns))
	for k, v := range h.patterns {
		all = append(all, kv{k, v.Last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last < all[j].last })
	for _, e := range all[:len(all)-opsPatternMaxKeys] {
		delete(h.patterns, e.key)
	}
}

func newAIGrowthHub() *aiGrowthHub {
	return &aiGrowthHub{patterns: make(map[string]*opsPatternHit)}
}

var growthHub = newAIGrowthHub()

// rememberUserPreference stores durable UI/ops preferences (kind=preference).
func (s *Server) rememberUserPreference(actor, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	content := fmt.Sprintf("用户偏好[%s] %s=%s", strings.TrimSpace(actor), key, value)
	src := "preference:" + key
	if actor != "" {
		src += ":" + actor
	}
	s.rememberAI("preference", src, content)
}

// loadPreferenceHints returns recent preference memories for prompt injection.
func (s *Server) loadPreferenceHints(actor string, limit int) string {
	if s.pg == nil || limit <= 0 {
		return ""
	}
	q := "用户偏好"
	if actor != "" {
		q += " " + actor
	}
	text, hits, _ := s.retrieveMemoryDetailed("preference", q, limit)
	if hits == 0 || strings.TrimSpace(text) == "" {
		return ""
	}
	return "【用户长期偏好】\n" + trimLine(text, 1200)
}

// recordForecastOutcome stores bias note when predicted vs actual diverge (>thresholdPct).
func (s *Server) recordForecastOutcome(metric string, predicted, actual, thresholdPct float64) {
	if thresholdPct <= 0 {
		thresholdPct = 15
	}
	if predicted == 0 && actual == 0 {
		return
	}
	base := mathAbs(predicted)
	if base < 1e-9 {
		base = mathAbs(actual)
	}
	if base < 1e-9 {
		return
	}
	biasPct := (actual - predicted) / base * 100
	if mathAbs(biasPct) < thresholdPct {
		return
	}
	dir := "偏高"
	if biasPct > 0 {
		dir = "偏低" // prediction was lower than actual
	} else {
		dir = "偏高"
	}
	content := fmt.Sprintf("预测偏差修正：指标 %s 上次预测值 %.2f，实际 %.2f，预测%s约 %.1f%%；后续同类预测请酌情修正。",
		strings.TrimSpace(metric), predicted, actual, dir, mathAbs(biasPct))
	s.rememberAI("forecast_bias", "forecast:"+strings.TrimSpace(metric), content)
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// forecastBiasHints injects recent bias corrections into AI prompts.
func (s *Server) forecastBiasHints(query string, limit int) string {
	if s.pg == nil || limit <= 0 {
		return ""
	}
	text, hits, _ := s.retrieveMemoryDetailed("forecast_bias", query, limit)
	if hits == 0 || strings.TrimSpace(text) == "" {
		return ""
	}
	return "【预测误差自省】\n" + trimLine(text, 800)
}

// trackOpsPattern records a repeated diagnostic/ops fingerprint; returns a skill proposal when count≥3.
func (s *Server) trackOpsPattern(actor, fingerprint, stepsSummary string) (propose bool, name, trigger, steps string) {
	fingerprint = strings.TrimSpace(strings.ToLower(fingerprint))
	if fingerprint == "" {
		return false, "", "", ""
	}
	key := actor + "|" + fingerprint
	now := time.Now().Unix()
	growthHub.mu.Lock()
	defer growthHub.mu.Unlock()
	h := growthHub.patterns[key]
	if h == nil {
		// 只在**新增键**时清理：命中已有键不会让表变大，没必要每次都扫一遍。
		growthHub.pruneLocked(now)
		h = &opsPatternHit{Key: fingerprint, Steps: stepsSummary}
		growthHub.patterns[key] = h
	}
	h.Count++
	h.Last = now
	if stepsSummary != "" {
		h.Steps = stepsSummary
	}
	if h.Count < 3 {
		return false, "", "", ""
	}
	// Reset counter after proposing to avoid spam.
	h.Count = 0
	name = "自动提议：" + trimLine(fingerprint, 40)
	trigger = "当出现与「" + fingerprint + "」相似的重复运维路径时"
	steps = h.Steps
	if steps == "" {
		steps = "1) 复核现场指标与日志\n2) 定位根因\n3) 执行既定处置并验证\n4) 必要时回滚"
	}
	return true, name, trigger, steps
}

// proposeSkillDraft inserts a draft skill after user-facing confirmation path.
func (s *Server) proposeSkillDraft(name, trigger, steps, tags string) (int64, error) {
	if s.pg == nil {
		return 0, fmt.Errorf("PG 不可用")
	}
	name = strings.TrimSpace(name)
	steps = strings.TrimSpace(steps)
	if name == "" || steps == "" {
		return 0, fmt.Errorf("技能名称与步骤必填")
	}
	if tags == "" {
		tags = "auto-proposed"
	}
	cfg := s.cfg.AIConfig()
	emb := embedText(cfg, name+" "+trigger)
	var id int64
	var err error
	if len(emb) > 0 {
		id, err = s.pg.insertSkill(name, trigger, steps, tags, "auto_proposed", emb)
	} else {
		id, err = s.pg.insertSkillNoEmbed(name, trigger, steps, tags, "auto_proposed")
	}
	if err != nil {
		return 0, err
	}
	_ = s.pg.setSkillStatus(id, "draft")
	return id, nil
}
