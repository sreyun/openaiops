package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// API 合成事务编排（迭代 C）
//
// 真实业务是链式的：登录 → 拿 token → 下单 → 查询。单接口独立探测覆盖不了这种依赖。
// 合成事务把多步 HTTP 请求按序串起来，步骤间用 {{var}} 传递从上一步响应提取的变量
// （如 token），任一步断言失败即判定事务失败并记录失败步。复用 probeHTTPAdvanced 的
// 完整探测/断言引擎（CaptureBody 拿响应体，jsonPathValue 提取变量）。
// ============================================================================

// APIStep 是合成事务的一个步骤（一次 HTTP 请求 + 断言 + 变量提取）。
type APIStep struct {
	Name          string            `json:"name"`
	Method        string            `json:"method,omitempty"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	Body          string            `json:"body,omitempty"`
	ExpectStatus  int               `json:"expect_status,omitempty"`
	ExpectKeyword string            `json:"expect_keyword,omitempty"`
	JSONPath      string            `json:"json_path,omitempty"`
	JSONExpect    string            `json:"json_expect,omitempty"`
	Extract       map[string]string `json:"extract,omitempty"`     // 变量名 -> 响应 JSON 路径，供后续步骤 {{变量名}} 引用
	TimeoutSec    int               `json:"timeout_sec,omitempty"` // 步骤超时（秒，默认 10）
}

// APITransaction 是一个合成事务（多步链式监控）。
type APITransaction struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	IntervalSec int               `json:"interval_sec"`
	Level       string            `json:"level"` // warning | critical
	Enabled     bool              `json:"enabled"`
	Vars        map[string]string `json:"vars,omitempty"` // 初始变量（base_url / 静态密钥等），供 {{var}} 引用
	Steps       []APIStep         `json:"steps"`
	CreatedAt   int64             `json:"created_at"`
}

var txnVarRe = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)

// substVars 把字符串里的 {{var}} 用上下文变量替换；未知变量保留原样（便于排错）。
func substVars(s string, vars map[string]string) string {
	if s == "" || !strings.Contains(s, "{{") {
		return s
	}
	return txnVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := txnVarRe.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		return m
	})
}

// substVarsMap 对 map 的 key 与 value 都做 {{var}} 替换。
func substVarsMap(in, vars map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[substVars(k, vars)] = substVars(v, vars)
	}
	return out
}

// txnStepResult 是一步的执行结果。
type txnStepResult struct {
	Name      string  `json:"name"`
	OK        bool    `json:"ok"`
	Msg       string  `json:"msg"`
	Code      int     `json:"code"`
	LatencyMs float64 `json:"latency_ms"`
}

// txnResult 是一次事务执行的结果（最新态存内存，供看板）。
type txnResult struct {
	OK         bool            `json:"ok"`
	FailedStep int             `json:"failed_step"` // -1=全过
	TotalMs    float64         `json:"total_ms"`
	Steps      []txnStepResult `json:"steps"`
	CheckedAt  int64           `json:"checked_at"`
}

// txnState 是合成事务调度/状态的内存态。
type txnState struct {
	mu      sync.Mutex
	status  map[string]txnResult
	down    map[string]bool
	lastRun map[string]time.Time
	fail    map[string]int
	ok      map[string]int
}

func newTxnState() *txnState {
	return &txnState{
		status: map[string]txnResult{}, down: map[string]bool{},
		lastRun: map[string]time.Time{}, fail: map[string]int{}, ok: map[string]int{},
	}
}

// ---- ConfigStore CRUD（与 APISystem 同机制，持久化到 PG/JSON） ----

func (cs *ConfigStore) APITransactions() []APITransaction {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]APITransaction, len(cs.cfg.APITransactions))
	copy(out, cs.cfg.APITransactions)
	return out
}

func (cs *ConfigStore) UpsertAPITransaction(t APITransaction) (APITransaction, error) {
	cs.mu.Lock()
	if t.ID == "" {
		t.ID = genToken()[:8]
		t.CreatedAt = time.Now().Unix()
		cs.cfg.APITransactions = append(cs.cfg.APITransactions, t)
	} else {
		found := false
		for i := range cs.cfg.APITransactions {
			if cs.cfg.APITransactions[i].ID == t.ID {
				t.CreatedAt = cs.cfg.APITransactions[i].CreatedAt
				cs.cfg.APITransactions[i] = t
				found = true
				break
			}
		}
		if !found {
			t.CreatedAt = time.Now().Unix()
			cs.cfg.APITransactions = append(cs.cfg.APITransactions, t)
		}
	}
	cs.mu.Unlock()
	return t, cs.save()
}

func (cs *ConfigStore) DeleteAPITransaction(id string) error {
	cs.mu.Lock()
	kept := cs.cfg.APITransactions[:0]
	for _, t := range cs.cfg.APITransactions {
		if t.ID != id {
			kept = append(kept, t)
		}
	}
	cs.cfg.APITransactions = kept
	cs.mu.Unlock()
	return cs.save()
}

// ---- 执行器 ----

// runTxn 按序执行事务的全部步骤，步骤间传递提取的变量；任一步失败即中断并记录失败步。
func (ar *apiRunner) runTxn(t APITransaction) txnResult {
	vars := map[string]string{}
	for k, v := range t.Vars {
		vars[k] = v
	}
	res := txnResult{OK: true, FailedStep: -1, CheckedAt: time.Now().Unix()}
	start := time.Now()
	for i, step := range t.Steps {
		to := step.TimeoutSec
		if to <= 0 {
			to = 10
		}
		c := CustomCheck{
			Type: "http", Advanced: true, CaptureBody: true,
			Target:       substVars(step.URL, vars),
			Method:       step.Method,
			Headers:      substVarsMap(step.Headers, vars),
			Body:         substVars(step.Body, vars),
			ExpectStatus: step.ExpectStatus, ExpectKeyword: step.ExpectKeyword,
			JSONPath: step.JSONPath, JSONExpect: step.JSONExpect,
			TimeoutSec: to, TraceParent: true,
		}
		pr := ar.cr.probeHTTPAdvanced(c)
		res.Steps = append(res.Steps, txnStepResult{
			Name: step.Name, OK: pr.ok, Msg: pr.msg, Code: pr.code, LatencyMs: pr.totalMs,
		})
		if !pr.ok {
			res.OK = false
			res.FailedStep = i
			break
		}
		// 提取变量供后续步骤引用（{{name}}）
		for name, path := range step.Extract {
			if val, ok := jsonPathValue(pr.body, path); ok {
				vars[name] = val
			}
		}
	}
	res.TotalMs = ms(time.Since(start))
	return res
}

// sweepTxn 按各事务的间隔调度执行（受 sem 限流 + panic 恢复）。
func (ar *apiRunner) sweepTxn() {
	now := time.Now()
	for _, t := range ar.cfg.APITransactions() {
		if !t.Enabled || len(t.Steps) == 0 {
			continue
		}
		iv := t.IntervalSec
		if iv < 5 {
			iv = 60
		}
		ar.txn.mu.Lock()
		last := ar.txn.lastRun[t.ID]
		due := last.IsZero() || now.Sub(last) >= time.Duration(iv)*time.Second
		if due {
			ar.txn.lastRun[t.ID] = now
		}
		ar.txn.mu.Unlock()
		if due {
			go ar.probeTxnLimited(t)
		}
	}
}

func (ar *apiRunner) probeTxnLimited(t APITransaction) {
	ar.sem <- struct{}{}
	defer func() { <-ar.sem }()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("apimon txn panic recovered", "txn", t.Name, "err", r)
		}
	}()
	ar.probeTxn(t)
}

// probeTxn 执行一次事务、更新内存状态、按去抖(连续 2 次)做异常/恢复告警。
func (ar *apiRunner) probeTxn(t APITransaction) {
	res := ar.runTxn(t)
	const debounce = 2
	ar.txn.mu.Lock()
	ar.txn.status[t.ID] = res
	wasDown := ar.txn.down[t.ID]
	nowDown := wasDown
	if !res.OK {
		ar.txn.fail[t.ID]++
		ar.txn.ok[t.ID] = 0
		if ar.txn.fail[t.ID] >= debounce && !wasDown {
			nowDown = true
			ar.txn.down[t.ID] = true
		}
	} else {
		ar.txn.ok[t.ID]++
		ar.txn.fail[t.ID] = 0
		if ar.txn.ok[t.ID] >= debounce && wasDown {
			nowDown = false
			ar.txn.down[t.ID] = false
		}
	}
	ar.txn.mu.Unlock()

	if nowDown != wasDown {
		ar.txnTransition(t, !nowDown, res)
	}
}

// txnTransition 在事务异常/恢复时写日志并推送告警（走 pushChannels，已含治理）。
func (ar *apiRunner) txnTransition(t APITransaction, up bool, res txnResult) {
	lvl := t.Level
	if lvl == "" {
		lvl = "critical"
	}
	var msg string
	if up {
		lvl = "info"
		msg = fmt.Sprintf("合成事务已恢复：%s", t.Name)
	} else if res.FailedStep >= 0 && res.FailedStep < len(res.Steps) {
		sr := res.Steps[res.FailedStep]
		msg = fmt.Sprintf("合成事务失败：%s（第 %d 步「%s」：%s）", t.Name, res.FailedStep+1, sr.Name, sr.Msg)
	} else {
		msg = fmt.Sprintf("合成事务失败：%s", t.Name)
	}
	a := Alert{Level: lvl, Type: "api_txn", Scope: t.ID, Hostname: t.Name, Message: msg, Timestamp: time.Now().Unix()}
	ar.store.AddLog(LogEntry{Kind: KindSystem, Level: a.Level, Actor: "合成事务", Host: t.Name, Message: msg})
	if cfg := ar.cfg.Get(); cfg.AlertsEnabled {
		ar.notifier.enqueuePush(cfg, a, !up)
	}
}

// txnStatusSnapshot 返回所有事务的最新执行结果副本（供看板）。
func (ar *apiRunner) txnStatusSnapshot() map[string]txnResult {
	ar.txn.mu.Lock()
	defer ar.txn.mu.Unlock()
	out := make(map[string]txnResult, len(ar.txn.status))
	for k, v := range ar.txn.status {
		out[k] = v
	}
	return out
}

// runTxnNow 立即执行某事务（新增/编辑后触发，快速出结果）。
func (ar *apiRunner) runTxnNow(id string) {
	for _, t := range ar.cfg.APITransactions() {
		if t.ID == id && t.Enabled {
			ar.txn.mu.Lock()
			ar.txn.lastRun[t.ID] = time.Now()
			ar.txn.mu.Unlock()
			go ar.probeTxnLimited(t)
			return
		}
	}
}
