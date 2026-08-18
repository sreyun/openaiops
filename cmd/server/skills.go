package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// ============================================================================
// AI 技能库（自进化核心）
//
// 把散落的经验/诊断/解决记忆，定期提炼成命名化、带「触发条件 + 操作步骤」的可复用技能(SOP)，
// 检索后注入提示词，让 AI 直接复用被现实验证过的做法——这是比原始 RAG 更高阶的自我进化：
// 系统不只是「记住发生过什么」，而是「总结出该怎么做」，并在使用中不断强化 / 改写。
// 借鉴自 Nous Hermes Agent 的 skill 机制，落到本项目 Go + pgvector 的既有底座上。
// ============================================================================

const (
	skillDistillDupDist = 0.12 // 与既有技能语义距离 < 此值视为重复 → 覆盖改进而非新增
	skillRelevantDist   = 0.55 // 注入提示词时的相关性阈值，超过则不注入（避免噪声）
)

// distillInProgress 防并发：每日维护与手动「立即提炼」可能同时触发，重入则跳过，
// 避免"检查相似→插入"的 TOCTOU 竞争产生近重复技能。
var distillInProgress atomic.Bool

// distilledSkill 是 AI 提炼输出的单条技能。
type distilledSkill struct {
	Name    string `json:"name"`
	Trigger string `json:"trigger"`
	Steps   string `json:"steps"`
	Tags    string `json:"tags"`
}

// distillSkills 提炼主流程：取近期高价值经验 → AI 提炼成若干可复用技能 → 去重(覆盖改进)后入库。
// 由每日维护循环驱动，也可手动触发。返回新增技能数。
func (s *Server) distillSkills(lookbackDays int) (int, error) {
	if s.pg == nil {
		return 0, fmt.Errorf("PG 不可用")
	}
	if !distillInProgress.CompareAndSwap(false, true) {
		return 0, nil // 已有提炼在跑，跳过（防并发去重竞争）
	}
	defer distillInProgress.Store(false)
	cfg := s.cfg.AIConfig()
	if !embedReady(cfg) {
		return 0, fmt.Errorf("AI 未配置")
	}
	if lookbackDays <= 0 {
		lookbackDays = 14
	}
	since := time.Now().Add(-time.Duration(lookbackDays) * 24 * time.Hour).Unix()
	mems := s.pg.memoriesForDistill(since, 40)
	if len(mems) < 3 { // 经验太少，不值得提炼
		return 0, nil
	}
	var corpus strings.Builder
	for i, m := range mems {
		fmt.Fprintf(&corpus, "[%d](%s) %s\n", i+1, m.Kind, trimLine(m.Content, 400))
	}
	sys := "你是资深 SRE 知识工程师。请从以下真实运维经验片段中，提炼出【可复用的运维技能(SOP)】。" +
		"每条技能要高度可复用、不绑定单次具体事件（把具体主机名/事件号抽象成通用条件）。" +
		"宁缺毋滥，只提炼有明确复用价值的，最多 6 条。" +
		"严格只输出一个 JSON 数组，每个元素为 {\"name\":\"技能名(简短祈使句)\",\"trigger\":\"何时适用(症状/场景)\"," +
		"\"steps\":\"分步操作(可含命令与判断)\",\"tags\":\"逗号分隔标签\"}；不要输出数组以外的任何文字。"
	out, err := aiComplete(cfg, sys, "运维经验片段：\n"+corpus.String())
	if err != nil {
		return 0, err
	}
	created := 0
	for _, sk := range parseDistilledSkills(out) {
		name, trigger, steps := strings.TrimSpace(sk.Name), strings.TrimSpace(sk.Trigger), strings.TrimSpace(sk.Steps)
		if name == "" || steps == "" {
			continue
		}
		emb := embedText(cfg, name+" "+trigger)
		if len(emb) == 0 {
			continue
		}
		if id, dup := s.pg.findSimilarSkill(emb, skillDistillDupDist); dup {
			// 已有相似技能：已被现实验证的优质技能(有成功记录/多次使用)只强化、保留其经过验证的步骤；
			// 未验证的才用新一版「改进」覆盖——避免一次较差的新生成把优质 SOP 回退。
			if !s.pg.skillProven(id) {
				_ = s.pg.updateSkill(id, name, trigger, steps, emb)
			}
			s.pg.recordSkillUse(id, true)
			continue
		}
		if _, err := s.pg.insertSkill(name, trigger, steps, sk.Tags, "distilled", emb); err == nil {
			created++
		}
	}
	if created > 0 {
		slog.Info("技能提炼完成", "新增技能", created, "候选经验", len(mems))
	}
	return created, nil
}

// parseDistilledSkills 容错解析 AI 输出的技能 JSON 数组（可能含代码块围栏或前后噪声）。
// 亦接受单对象 {"name",...}，方便升格路径偶发只返回一条对象。
func parseDistilledSkills(text string) []distilledSkill {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "```"); i >= 0 { // 去代码块围栏
		text = text[i+3:]
		if j := strings.LastIndex(text, "```"); j >= 0 {
			text = text[:j]
		}
		if nl := strings.IndexByte(text, '\n'); nl >= 0 && nl < 12 { // 去掉 ```json 语言标记行
			text = text[nl+1:]
		}
	}
	l, r := strings.IndexByte(text, '['), strings.LastIndexByte(text, ']')
	if l >= 0 && r > l {
		var arr []distilledSkill
		if err := json.Unmarshal([]byte(text[l:r+1]), &arr); err == nil {
			return arr
		}
	}
	// 单对象兜底
	ol, or := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if ol >= 0 && or > ol {
		var one distilledSkill
		if err := json.Unmarshal([]byte(text[ol:or+1]), &one); err == nil &&
			(strings.TrimSpace(one.Name) != "" || strings.TrimSpace(one.Steps) != "") {
			return []distilledSkill{one}
		}
	}
	return nil
}

// retrieveSkillsDetailed 同 retrieveSkillsForPrompt，额外返回命中技能名、命中数与降级原因。
func (s *Server) retrieveSkillsDetailed(query string, topK int) (text string, names []string, hits int, degraded string) {
	if s.pg == nil {
		return "", nil, 0, "no_pg"
	}
	cfg := s.cfg.AIConfig()
	if !embedReady(cfg) || strings.TrimSpace(query) == "" {
		return "", nil, 0, "no_embed"
	}
	if topK <= 0 {
		topK = 4
	}
	emb := embedText(cfg, query)
	if len(emb) == 0 {
		return "", nil, 0, "no_embed"
	}
	serviceID, category := s.skillRetrievalScope(query)
	skills, err := s.pg.searchSkillsScoped(emb, topK, skillRelevantDist, serviceID, category)
	if err != nil || len(skills) == 0 {
		return "", nil, 0, ""
	}
	var b strings.Builder
	b.WriteString("\n\n【已掌握技能（历史提炼的可复用 SOP，相关时优先套用）】\n")
	ids := make([]int64, 0, len(skills))
	names = make([]string, 0, len(skills))
	for _, sk := range skills {
		if sk.Distance > skillRelevantDist {
			continue
		}
		fmt.Fprintf(&b, "- 【%s】适用：%s\n  步骤：%s\n", sk.Name, trimLine(sk.Trigger, 120), trimLine(sk.Steps, 500))
		ids = append(ids, sk.ID)
		if n := strings.TrimSpace(sk.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(ids) == 0 {
		return "", nil, 0, ""
	}
	go func() {
		for _, id := range ids {
			s.pg.recordSkillUse(id, false)
		}
	}()
	return b.String(), names, len(ids), ""
}

// skillRetrievalScope best-effort resolves BusinessService / host category from query text
// (hostname / host id / service name) for scoped skill retrieval.
func (s *Server) skillRetrievalScope(query string) (serviceID, category string) {
	if s == nil || strings.TrimSpace(query) == "" {
		return "", ""
	}
	q := strings.ToLower(query)
	if s.store != nil {
		for _, h := range s.store.ListHosts() {
			if h == nil {
				continue
			}
			if (h.Hostname != "" && strings.Contains(q, strings.ToLower(h.Hostname))) ||
				(h.ID != "" && strings.Contains(q, strings.ToLower(h.ID))) {
				category = h.Category
				break
			}
		}
	}
	if s.cfg != nil {
		for _, svc := range s.cfg.BusinessServices() {
			if svc.Name != "" && strings.Contains(q, strings.ToLower(svc.Name)) {
				serviceID = svc.ID
				break
			}
			if svc.ID != "" && strings.Contains(q, strings.ToLower(svc.ID)) {
				serviceID = svc.ID
				break
			}
		}
	}
	return serviceID, category
}

// reinforceSkill 在事件解决 / 建议被采纳时，语义定位并强化最相关技能——技能层的学习闭环。异步。
func (s *Server) reinforceSkill(text string, factor float64) {
	if s.pg == nil {
		return
	}
	cfg := s.cfg.AIConfig()
	if !embedReady(cfg) || strings.TrimSpace(text) == "" {
		return
	}
	go func() {
		if emb := embedText(cfg, text); len(emb) > 0 {
			if factor < 1.0 {
				s.pg.penalizeSkillNearest(emb, factor)
			} else {
				s.pg.boostSkillNearest(emb, factor)
			}
		}
	}()
}

// promoteTextToSkill 把一段被验证的经验（结案卡 / 好评诊断）异步升格为一条可复用 Skill。
// reason 如 resolution / diagnosis_feedback；sourceRef 如 incident:12。
func (s *Server) promoteTextToSkill(reason, sourceRef, corpus string) {
	if s.pg == nil {
		return
	}
	corpus = strings.TrimSpace(corpus)
	if corpus == "" {
		return
	}
	go func() {
		created, updated, err := s.promoteTextToSkillSync(reason, sourceRef, corpus)
		if err != nil {
			slog.Debug("经验升格技能跳过", "reason", reason, "source", sourceRef, "err", err)
			return
		}
		if created || updated {
			slog.Info("经验已升格为技能", "reason", reason, "source", sourceRef, "created", created, "updated", updated)
		}
	}()
}

// promoteTextToSkillSync 同步升格为 active Skill（已验证路径）。
func (s *Server) promoteTextToSkillSync(reason, sourceRef, corpus string) (created, updated bool, err error) {
	return s.promoteTextToSkillSyncStatus(reason, sourceRef, corpus, "active")
}

// promoteTextToSkillSyncStatus 同步升格；status 为 active|draft（draft 不参与检索）。
func (s *Server) promoteTextToSkillSyncStatus(reason, sourceRef, corpus, status string) (created, updated bool, err error) {
	if s.pg == nil {
		return false, false, fmt.Errorf("需要 PostgreSQL")
	}
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "draft" && status != "active" {
		status = "active"
	}
	cfg := s.cfg.AIConfig()
	if !embedReady(cfg) {
		return false, false, fmt.Errorf("AI 未配置")
	}
	sys := "你是资深 SRE 知识工程师。请把下面这段【已验证】的运维经验，提炼成 1 条可复用技能(SOP)。" +
		"技能不要绑定具体事件号/一次性主机名；触发条件写清症状与场景；步骤可执行。" +
		"若内容不足以形成可复用技能，输出空数组 []。" +
		"严格只输出 JSON 数组，元素为 {\"name\",\"trigger\",\"steps\",\"tags\"}，最多 1 条。"
	out, err := aiComplete(cfg, sys, "经验来源："+reason+"/"+sourceRef+"\n\n"+trimLine(corpus, 3500))
	if err != nil {
		return false, false, err
	}
	skills := parseDistilledSkills(out)
	if len(skills) == 0 {
		return false, false, nil
	}
	sk := skills[0]
	name, trigger, steps := strings.TrimSpace(sk.Name), strings.TrimSpace(sk.Trigger), strings.TrimSpace(sk.Steps)
	if name == "" || steps == "" {
		return false, false, nil
	}
	emb := embedText(cfg, name+" "+trigger)
	if len(emb) == 0 {
		return false, false, fmt.Errorf("embed 失败")
	}
	src := "auto:" + reason
	if sourceRef != "" {
		src = src + ":" + sourceRef
	}
	if status == "active" {
		if id, dup := s.pg.findSimilarSkill(emb, skillDistillDupDist); dup {
			if !s.pg.skillProven(id) {
				if err := s.pg.updateSkill(id, name, trigger, steps, emb); err != nil {
					return false, false, err
				}
				updated = true
			}
			s.pg.recordSkillUse(id, true)
			return false, updated, nil
		}
	}
	id, err := s.pg.insertSkill(name, trigger, steps, sk.Tags, src, emb)
	if err != nil {
		return false, false, err
	}
	if status != "active" {
		_ = s.pg.setSkillStatus(id, status)
	}
	return true, false, nil
}
