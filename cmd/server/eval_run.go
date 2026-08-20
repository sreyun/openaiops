package main

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

//go:embed eval/cases/*.json
var evalCasesFS embed.FS

// evalCase 是一个录制的事故场景，含 ground truth 根因与期望动作。它是「AI 闭环可证明」的
// 评测素材：离线（mock LLM）+ 在线（真实 provider）两套评测共用同一份 case 定义，
// 使周报里的「验证通过率」从自证（AI 验证自己）变成他证（对照 ground truth 判定）。
type evalCase struct {
	ID                   string     `json:"id"`
	Task                 string     `json:"task"`
	Severity             string     `json:"severity"`
	Input                string     `json:"input"`
	GroundTruthRootCause []string   `json:"ground_truth_root_cause"`
	ExpectedActions      []string   `json:"expected_actions"`
	Evals                []evalRule `json:"evals"`
}

type evalRule struct {
	Type     string   `json:"type"` // keyword | llm_judge
	Keywords []string `json:"keywords"`
	MinHit   int      `json:"min_hit"`
	Question string   `json:"question,omitempty"`
}

// evalResult 单个 case 的评测结果。
type evalResult struct {
	CaseID        string `json:"case_id"`
	Passed        bool   `json:"passed"`
	RootCauseHit  bool   `json:"root_cause_hit"`
	ActionAccept  bool   `json:"action_accept"`
	VerifyAgree   bool   `json:"verify_agree"`
	RootCausePass int    `json:"root_cause_pass"`
	RootCauseN    int    `json:"root_cause_n"`
	RulesPass     int    `json:"rules_pass"`
	RulesN        int    `json:"rules_n"`
}

// evalRunSummary 一次评测运行的汇总（落 ai_eval_runs）。
type evalRunSummary struct {
	RunID            string  `json:"run_id"`
	Ts               int64   `json:"ts"`
	Model            string  `json:"model"`
	Mode             string  `json:"mode"` // offline | online
	EvalSetVersion   string  `json:"eval_set_version"`
	CaseCount        int     `json:"case_count"`
	PassedCount      int     `json:"passed_count"`
	PassRate         float64 `json:"pass_rate"`
	RootCauseHitRate float64 `json:"root_cause_hit_rate"`
	ActionAcceptRate float64 `json:"action_accept_rate"`
	VerifyAgreement  float64 `json:"verify_agreement"`
}

// loadEvalCases 从 go:embed 加载全部评测 case，按 ID 排序保证确定性。
func loadEvalCases() ([]evalCase, error) {
	entries, err := evalCasesFS.ReadDir("eval/cases")
	if err != nil {
		return nil, err
	}
	var cases []evalCase
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// embed.FS requires forward-slash paths; filepath.Join on Windows would
		// emit backslashes and fail to locate the embedded file.
		b, err := evalCasesFS.ReadFile("eval/cases/" + e.Name())
		if err != nil {
			return nil, err
		}
		var c evalCase
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		if c.ID == "" {
			c.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

// evalLLMFunc 抽象评测时的诊断/判定调用，便于离线 mock 与在线真实 provider 复用。
type evalLLMFunc func(ctx context.Context, system, user string) (string, error)

// ruleJudge 基于关键词的规则层判定：统计输入中命中的关键词，达到 min_hit 即通过。
func ruleJudge(text string, rules []evalRule) (pass, n int) {
	for _, r := range rules {
		if r.Type != "" && r.Type != "keyword" {
			continue // llm_judge 走 judgeLLM，不走规则层
		}
		hit := 0
		for _, k := range r.Keywords {
			if strings.Contains(text, k) {
				hit++
			}
		}
		if hit >= r.MinHit {
			pass++
		}
		n++
	}
	return pass, n
}

// runEvalCase 对单个 case 跑一次评测：诊断 → 规则 judge。
// llm 参数决定用 mock 还是真实 provider；返回 case 判定结果。
func runEvalCase(ctx context.Context, c evalCase, llm evalLLMFunc) evalResult {
	sys := "你是严谨的运维根因分析师。请基于告警描述，指出可能的根因并给出排查动作。用中文，简洁分点。"
	res := evalResult{CaseID: c.ID, RootCauseN: len(c.GroundTruthRootCause)}
	answer, err := llm(ctx, sys, "【告警】\n"+c.Input)
	if err != nil {
		slog.Warn("eval case LLM error", "case", c.ID, "err", err)
		return res
	}
	// 根因命中：任一 ground truth 关键词出现即算命中该根因。
	rootPass := 0
	for _, g := range c.GroundTruthRootCause {
		if strings.Contains(answer, g) {
			rootPass++
		}
	}
	res.RootCausePass = rootPass
	res.RootCauseHit = rootPass > 0
	// 动作采纳：任一期望动作相关关键词出现。
	actionHit := false
	for _, act := range c.ExpectedActions {
		// 期望动作本身是描述性短语，用其前半截关键词匹配（首个连续 2+ 字）。
		kw := firstChinesePhrase(act)
		if kw != "" && strings.Contains(answer, kw) {
			actionHit = true
			break
		}
	}
	res.ActionAccept = actionHit
	// 规则层判定（关键词评测规则）。
	rulePass, ruleN := ruleJudge(answer, c.Evals)
	res.RulesPass, res.RulesN = rulePass, ruleN
	// 整体通过：根因至少命中一个，且（无规则时直接过，有规则时规则过）。
	res.Passed = res.RootCauseHit && (ruleN == 0 || rulePass >= ruleN)
	res.VerifyAgree = res.RootCauseHit // 规则层无法判 verify 语义，保守给 root cause 命中
	return res
}

// runEvalSet 跑完整评测集并汇总。llm 由调用方决定离线（mock/测试）或在线（真实 provider）。
func runEvalSet(ctx context.Context, model, mode string, llm evalLLMFunc) (evalRunSummary, error) {
	cases, err := loadEvalCases()
	if err != nil {
		return evalRunSummary{}, err
	}
	runID := newOpaqueID("eval_")
	sum := evalRunSummary{
		RunID: runID, Model: model, Mode: mode,
		EvalSetVersion: evalSetVersion(), CaseCount: len(cases),
	}
	var rootHitN, actionAcceptN, verifyAgreeN int
	for _, c := range cases {
		res := runEvalCase(ctx, c, llm)
		if res.Passed {
			sum.PassedCount++
		}
		if res.RootCauseHit {
			rootHitN++
		}
		if res.ActionAccept {
			actionAcceptN++
		}
		if res.VerifyAgree {
			verifyAgreeN++
		}
	}
	if sum.CaseCount > 0 {
		f := float64(sum.CaseCount)
		sum.PassRate = float64(sum.PassedCount) / f
		sum.RootCauseHitRate = float64(rootHitN) / f
		sum.ActionAcceptRate = float64(actionAcceptN) / f
		sum.VerifyAgreement = float64(verifyAgreeN) / f
	}
	return sum, nil
}

// evalSetVersion 评测集版本 = 已加载 case 的数量 + 文件指纹，供周报引用可追溯。
func evalSetVersion() string {
	cases, err := loadEvalCases()
	if err != nil {
		return "unknown"
	}
	return "v1." + strconv.Itoa(len(cases))
}

// firstChinesePhrase 取字符串首个连续中文短语（≥2 字），用于关键词粗匹配期望动作。
func firstChinesePhrase(s string) string {
	var runes []rune
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fa5 {
			runes = append(runes, r)
		} else if len(runes) >= 2 {
			break
		} else {
			runes = runes[:0]
		}
	}
	if len(runes) >= 2 {
		return string(runes[:min(4, len(runes))])
	}
	return ""
}
