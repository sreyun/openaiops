package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 资源注解：给采集来的资源（Hyper-V 虚机、容器…）挂一份"人写的"信息。
//
// 为什么需要：资源页上的名字全部来自被采集端，而那边重名是常态——同一个模板批量克隆出
// 来的虚机、不同宿主上同名的容器。列表里并排两个 CentOS-30.124，运维只能靠点开看 IP 才
// 分得清哪个是哪个。别名/备注/标签是运维自己的那一份区分依据，不能指望被采集端提供。
//
// 存在 ServerConfig 里而不是单独建表：它和主机分组树是同一类东西——量小（一台宿主机几十
// 个虚机）、改动低频、需要跟着配置一起备份还原。

// ResourceNote 是一个资源上的用户注解。三项全空 = 删除这条注解。
type ResourceNote struct {
	Alias     string   `json:"alias,omitempty"` // 别名：列表里显示在原名旁边
	Note      string   `json:"note,omitempty"`  // 备注：详情里展示
	Tags      []string `json:"tags,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
}

const (
	// maxResourceNotes 是注解总条数上限。配置整份读写、整份备份，无上限地长下去会把
	// 配置文件撑成负担；到顶时拒绝**新增**（更新已有的仍然放行），不静默丢数据。
	maxResourceNotes    = 5000
	maxResourceKeyLen   = 200
	maxResourceAliasLen = 64
	maxResourceNoteLen  = 500
	maxResourceTags     = 12
	maxResourceTagLen   = 32
)

// sanitizeResourceKey 归一化资源键。键由前端拼（"hyperv:<vm-guid>" / "container:<host>:<id>"），
// 是配置 map 的键，也会出现在审计里——控制字符和超长串都挡在门外。
func sanitizeResourceKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxResourceKeyLen {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
		// 键会作为一个路径段出现在 PUT /resource-notes/{key} 里：带斜杠的键 encode 之后
		// 会被 mux 拆成两段而匹配不上，报出来的是一个莫名其妙的 404。直接在这里拒掉，
		// 让调用方立刻知道键不合法。
		if r == '/' {
			return ""
		}
	}
	return s
}

func truncRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > n {
		return strings.TrimSpace(string(rs[:n]))
	}
	return s
}

// sanitizeResourceNote 裁剪到长度上限并去掉空标签。返回值的三项若全空，调用方应删除该条。
func sanitizeResourceNote(n ResourceNote) ResourceNote {
	out := ResourceNote{
		Alias: truncRunes(strings.ReplaceAll(n.Alias, "\n", " "), maxResourceAliasLen),
		Note:  truncRunes(n.Note, maxResourceNoteLen),
	}
	seen := map[string]struct{}{}
	for _, t := range n.Tags {
		t = truncRunes(strings.ReplaceAll(t, "\n", " "), maxResourceTagLen)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out.Tags = append(out.Tags, t)
		if len(out.Tags) >= maxResourceTags {
			break
		}
	}
	return out
}

func (n ResourceNote) isEmpty() bool {
	return n.Alias == "" && n.Note == "" && len(n.Tags) == 0
}

func (cs *ConfigStore) resourceNotesSnapshot() map[string]ResourceNote {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make(map[string]ResourceNote, len(cs.cfg.ResourceNotes))
	for k, v := range cs.cfg.ResourceNotes {
		out[k] = v
	}
	return out
}

// setResourceNote 写入/更新/删除一条注解。三项全空即删除——界面上"清空并保存"就是删除，
// 不需要再给一个删除按钮。
func (cs *ConfigStore) setResourceNote(key string, note ResourceNote, actor string) error {
	key = sanitizeResourceKey(key)
	if key == "" {
		return fmt.Errorf("invalid resource key")
	}
	note = sanitizeResourceNote(note)
	cs.mu.Lock()
	if cs.cfg.ResourceNotes == nil {
		cs.cfg.ResourceNotes = map[string]ResourceNote{}
	}
	_, exists := cs.cfg.ResourceNotes[key]
	if note.isEmpty() {
		if !exists {
			cs.mu.Unlock()
			return nil // 删一条本来就不存在的，不算错
		}
		delete(cs.cfg.ResourceNotes, key)
		cs.mu.Unlock()
		return cs.save()
	}
	if !exists && len(cs.cfg.ResourceNotes) >= maxResourceNotes {
		cs.mu.Unlock()
		return fmt.Errorf("resource notes limit reached (%d)", maxResourceNotes)
	}
	note.UpdatedAt = time.Now().Unix()
	note.UpdatedBy = truncRunes(actor, 64)
	cs.cfg.ResourceNotes[key] = note
	cs.mu.Unlock()
	return cs.save()
}

func (s *Server) handleGetResourceNotes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.resourceNotesSnapshot())
}

func (s *Server) handlePutResourceNote(w http.ResponseWriter, r *http.Request) {
	key := sanitizeResourceKey(r.PathValue("key"))
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid resource key"})
		return
	}
	var req ResourceNote
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.setResourceNote(key, req, s.actorName(r)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	saved := sanitizeResourceNote(req)
	verb := "更新资源注解"
	if saved.isEmpty() {
		verb = "清除资源注解"
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info",
		Message: verb + "：" + key + func() string {
			if saved.Alias != "" {
				return "（别名 " + saved.Alias + "）"
			}
			return ""
		}()})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "key": key, "note": saved})
}
