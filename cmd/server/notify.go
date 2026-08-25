package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"
)

// Notifier evaluates alerts on a timer and pushes deduplicated notifications
// to Feishu / DingTalk bots. Only alert transitions (fire / resolve) are sent,
// so a persistent condition never spams the channel.
type Notifier struct {
	store     *Store
	cfg       *ConfigStore
	httpc     *http.Client
	mu        sync.Mutex
	active    map[string]Alert // alertKey -> alert currently firing (已确认并通知)
	since     map[string]int64 // alertKey -> unix time the alert first fired
	recordIDs map[string]int64 // alertKey -> PG record ID (for resolve update)
	// 抖动抑制（flapping debounce）：候选告警需连续出现 alertConfirmTicks 次才真正触发通知；
	// 已触发的告警需连续消失 alertClearTicks 次才判恢复。避免阈值边界反复抖动造成"触发/恢复"刷屏。
	pending map[string]int // 候选告警连续出现计数（未达确认阈值前不通知）
	missing map[string]int // 已触发告警连续消失计数（未达清除阈值前不恢复）
	// SRE hooks (set during server wiring; nil-safe).
	incidents   *incidentManager
	remediation *remediationManager
	forward     *forwardManager // set after server startup
	hw          *hardwareStore  // set after server startup; feeds hardware alerts
	hv          *hypervStore    // set after server startup; feeds Hyper-V VM alerts
	snmp        *snmpStore      // set after server startup; feeds SNMP device alerts
	nf          *nfStore        // set after server startup; feeds NetFlow traffic-anomaly alerts

	// 投递队列：把「评估」与「往外发」拆开，见 enqueuePush。
	pushOnce sync.Once
	pushQ    chan notifyJob
	pushDrop atomic.Uint64
}

// notifyJob 是一次待投递的告警通知。cfg 随任务带走而不是投递时再读：
// 这一轮该按哪套渠道配置发，在评估那一刻就定下来了。
type notifyJob struct {
	cfg    ServerConfig
	alert  Alert
	firing bool
}

// notifyPushQueue 是投递队列深度。渠道正常时它几乎恒为空；
// 512 是「一次机房级故障（数百台同时离线）能整批装下」的量。
const notifyPushQueue = 512

// 抖动抑制阈值（tick 间隔 10s）：连续 2 次（~20s 持续）才触发/恢复，压制阈值边界抖动刷屏。
const (
	alertConfirmTicks = 2
	alertClearTicks   = 2
)

func NewNotifier(store *Store, cfg *ConfigStore) *Notifier {
	return &Notifier{
		store:     store,
		cfg:       cfg,
		httpc:     newGuardedHTTPClient(8 * time.Second), // SSRF：飞书/钉钉 webhook 用户可配，拦元数据/链路本地
		active:    map[string]Alert{},
		since:     map[string]int64{},
		recordIDs: map[string]int64{},
		pending:   map[string]int{},
		missing:   map[string]int{},
	}
}

func alertKey(a Alert) string { return a.HostID + "/" + a.Type + "/" + a.Scope }

// AlertActive reports whether the alert identified by alertKey() is still in the
// firing set.
//
// 这是自愈闭环缺失的那一半「观察」。remediation.go 的文件头写着 closed-loop，但它
// 判定一次自愈成功的唯一依据是**剧本退出码为 0**——「命令跑完了」和「问题没了」是
// 两个不同的断言。重启一个起来就挂的服务、清一块马上又满的磁盘，都会退出 0，于是
// 运维收到「自动修复成功」的通知、不再去看，而告警其实一直在响；同时冷却窗被这次
// 「成功」占用，真正的处置反而被推迟。
//
// active 集合本身已经带了抖动抑制（连续 alertClearTicks 次消失才判恢复），所以它
// 正是这里要的信号：拿它回看一眼，就能把「执行成功」和「确实修好了」分开。
func (n *Notifier) AlertActive(key string) bool {
	if n == nil || key == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.active[key]
	return ok
}

// Run evaluates alerts every interval and notifies on state transitions.
func (n *Notifier) Run(interval time.Duration) {
	n.tick() // evaluate promptly on startup
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		n.tick()
	}
}

// ResetState clears the fire/resolve memory so the next evaluation re-pushes
// every currently-active alert. Called after the alert config changes, so a
// freshly configured webhook receives the outstanding alerts instead of them
// being silently swallowed as "already seen".
func (n *Notifier) ResetState() {
	n.mu.Lock()
	n.active = map[string]Alert{}
	n.pending = map[string]int{}
	n.missing = map[string]int{}
	n.mu.Unlock()
}

// Trigger runs one evaluation immediately (used right after a config save).
func (n *Notifier) Trigger() { n.tick() }

// PushAdhoc sends a free-form notification to selected channels (empty = all enabled).
func (n *Notifier) PushAdhoc(level, title, body string, channels []string) {
	if n == nil || n.cfg == nil {
		return
	}
	cfg := n.cfg.Get()
	text := title
	if body != "" {
		text = title + "\n" + body
	}
	allow := map[string]bool{}
	for _, c := range channels {
		allow[strings.ToLower(strings.TrimSpace(c))] = true
	}
	use := func(name string) bool { return len(allow) == 0 || allow[name] }
	a := Alert{Level: level, Message: text, Timestamp: time.Now().Unix()}
	if use("feishu") && cfg.Feishu.Enabled && cfg.Feishu.Webhook != "" {
		_ = n.sendFeishu(cfg.Feishu, text)
	}
	if use("dingtalk") && cfg.Dingtalk.Enabled && cfg.Dingtalk.Webhook != "" {
		_ = n.sendDingtalk(cfg.Dingtalk, text)
	}
	if use("email") && cfg.SMTP.Enabled && cfg.SMTP.Host != "" {
		for _, to := range n.cfg.AlertEmails() {
			_ = sendEmail(cfg.SMTP, to, "["+level+"] "+title, "<pre>"+strings.ReplaceAll(text, "<", "&lt;")+"</pre>")
		}
	}
	if use("webhook") && cfg.CustomWebhook.Enabled && cfg.CustomWebhook.URL != "" {
		_ = sendCustomWebhook(cfg.CustomWebhook, text, a, true)
	}
	if use("sms") && cfg.SMS.Enabled {
		_ = n.sendSMS(cfg.SMS, text)
	}
	if use("voicecall") && cfg.VoiceCall.Enabled {
		_ = n.sendVoiceCall(cfg.VoiceCall, text)
	}
}

// ActiveAlerts returns a copy of the alerts currently firing (for AI inspection).
func (n *Notifier) ActiveAlerts() []Alert {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Alert, 0, len(n.active))
	for _, a := range n.active {
		out = append(out, a)
	}
	return out
}

// ActiveSince returns a copy of the first-fired times keyed by alertKey,
// letting the alerts API show "elapsed X minutes".
func (n *Notifier) ActiveSince() map[string]int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]int64, len(n.since))
	for k, v := range n.since {
		out[k] = v
	}
	return out
}

func (n *Notifier) tick() {
	cfg := n.cfg.Get()
	alerts := Evaluate(n.store.ListHosts(), n.cfg.Thresholds())
	if n.forward != nil {
		alerts = append(alerts, EvaluateForward(n.forward.Snapshot(), n.cfg.Thresholds())...)
	}
	// 硬件（Redfish/BMC）异常并入同一条告警链路：去重 → 触发/恢复 → 推送飞书/钉钉/短信…
	if n.hw != nil {
		alerts = append(alerts, EvaluateHardware(n.hw)...)
	}
	// Hyper-V 虚拟机异常同样并入：关机/暂停/健康/资源超阈值 → 推送 + critical 自动 AI 诊断。
	if n.hv != nil {
		alerts = append(alerts, EvaluateHyperV(n.hv)...)
	}
	// SNMP 网络设备异常并入：接口 up/down、带宽利用率、错误/丢包率、采集失败。
	if n.snmp != nil {
		alerts = append(alerts, EvaluateSNMP(n.snmp, n.cfg.Thresholds())...)
	}
	// NetFlow 流量异常并入：突增（EWMA 基线）、采集器丢包。
	if n.nf != nil {
		alerts = append(alerts, EvaluateNetFlow(n.nf, n.cfg.Thresholds())...)
	}
	// 补上完整分组路径：通知里只有主机名和 IP 时，值班的人得先回面板查这台机器属于谁。
	alerts = n.cfg.decorateAlertGroups(alerts)
	cur := make(map[string]Alert, len(alerts))
	for _, a := range alerts {
		cur[alertKey(a)] = a
	}
	// Compute transitions under the lock, then dispatch (network I/O) outside it.
	type transition struct {
		a      Alert
		firing bool
	}
	var todo []transition
	fires, resolves := n.reconcile(cur)
	// 状态始终维护（即便告警关闭），re-enable 不会重放；仅在启用时才真正派发通知。
	if cfg.AlertsEnabled {
		for _, a := range fires {
			todo = append(todo, transition{a, true})
		}
		for _, a := range resolves {
			todo = append(todo, transition{a, false})
		}
	}

	for _, t := range todo {
		n.dispatch(cfg, t.a, t.firing)
		// SRE closed loop: open/resolve an incident, and on a firing alert run any
		// matching auto-remediation rule (scoped to the affected host).
		if n.incidents != nil {
			incID := n.incidents.OnAlertTransition(t.a, alertKey(t.a), t.firing)
			if t.firing && incID != 0 {
				if n.cfg != nil {
					if w, ok := n.cfg.activeFreezeWindow(t.a.HostID, t.a.Type, time.Now().Unix()); ok {
						// Deduplicate freeze annotations on flapping re-fires of the same incident.
						note := fmt.Sprintf("处于变更窗/冻结期「%s」", w.Name)
						already := false
						if inc, found := n.incidents.Get(incID); found {
							for _, ev := range inc.Timeline {
								if ev.Actor == "change-window" && ev.Text == note {
									already = true
									break
								}
							}
						}
						if !already {
							n.incidents.AddEvent(incID, "note", "change-window", note)
						}
					}
				}
				if n.remediation != nil {
					n.remediation.OnAlert(t.a, incID)
				}
			}
		}
	}
}

// reconcile 把本轮评估得到的告警集合 cur 与已确认的 active 集合对账，返回本轮需要「触发通知」与
// 「恢复通知」的告警。核心是抖动抑制（flapping debounce）：候选需连续出现 alertConfirmTicks 次才
// 真正触发；已触发的需连续消失 alertClearTicks 次才判恢复。阈值边界的瞬时抖动因此不会刷屏。
// 该方法自带加锁，且始终维护内部状态（即便本轮不派发），从而保证「触发一次 / 恢复一次」语义。
func (n *Notifier) reconcile(cur map[string]Alert) (fires, resolves []Alert) {
	now := time.Now().Unix()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pending == nil {
		n.pending = map[string]int{}
	}
	if n.missing == nil {
		n.missing = map[string]int{}
	}
	// 触发确认：候选需连续出现 alertConfirmTicks 次才升级为已触发并通知；已触发的刷新其最新值。
	for k, a := range cur {
		if _, ok := n.active[k]; ok {
			n.active[k] = a // 持续告警：更新最新数值/消息，但不重复通知
			delete(n.missing, k)
			continue
		}
		n.pending[k]++
		if n.pending[k] >= alertConfirmTicks {
			n.active[k] = a
			n.since[k] = now
			delete(n.pending, k)
			fires = append(fires, a)
		}
	}
	// 候选在确认前消失：清掉其计数（抖动被吸收，不产生任何通知）。
	for k := range n.pending {
		if _, ok := cur[k]; !ok {
			delete(n.pending, k)
		}
	}
	// 恢复确认：已触发告警需连续消失 alertClearTicks 次才判恢复并通知恢复。
	for k, a := range n.active {
		if _, ok := cur[k]; ok {
			continue
		}
		n.missing[k]++
		if n.missing[k] >= alertClearTicks {
			resolves = append(resolves, a)
			delete(n.active, k)
			delete(n.missing, k)
			delete(n.since, k)
		}
	}
	return fires, resolves
}

// enqueuePush 把「往外发」交给一条固定的投递协程，评估循环不再等网络。
//
// 为什么必须拆开：tick() 每 10 秒跑一轮，之前在同一个 goroutine 里**串行**地把
// 每条告警推给飞书/钉钉/邮件/Webhook/短信。渠道健康时这没问题；渠道不健康时是灾难——
// 一次机房级故障让 100 台主机同时离线，而同一个网络故障往往也让 webhook 连不上，
// 于是 100 条 × 8 秒超时 = 13 分钟里**告警评估整个停摆**：新的危急告警发现不了，
// 已恢复的也判不出来。偏偏这正是最需要告警的时刻。
//
// 投递仍然是单协程串行的，这一条是刻意的：飞书/钉钉自定义机器人有每分钟条数限制，
// 并发打过去只会被对面限流，换来一批"发送失败"。这里要解决的是**评估被拖住**，
// 不是把消息发得更快。
//
// 队列满 = 某条渠道已经堵了很久。丢弃并留一条平台故障记录，比无限堆积到 OOM 好：
// 排在队尾的通知这时候早已过时，而 OOM 会把整个控制面一起带走。
func (n *Notifier) enqueuePush(cfg ServerConfig, a Alert, firing bool) {
	if n == nil {
		return
	}
	n.pushOnce.Do(func() {
		n.pushQ = make(chan notifyJob, notifyPushQueue)
		go func() {
			for j := range n.pushQ {
				n.pushChannels(j.cfg, j.alert, j.firing)
			}
		}()
	})
	select {
	case n.pushQ <- notifyJob{cfg: cfg, alert: a, firing: firing}:
	default:
		dropped := n.pushDrop.Add(1)
		reportFault("notify", "push_queue_full", "critical", a.HostID,
			fmt.Sprintf("告警投递队列已满（%d 条），本条通知被丢弃：%s；累计丢弃 %d 条——"+
				"通常意味着某条通知渠道长时间发不出去，请检查渠道配置与网络", notifyPushQueue, a.Message, dropped), "")
	}
}

func (n *Notifier) dispatch(cfg ServerConfig, a Alert, firing bool) {
	// activity log: the machine-detected threshold transition (intervention)
	verb, tlvl := Tz("notify.alert_fired"), a.Level
	if !firing {
		verb, tlvl = Tz("notify.alert_recovered"), "info"
	}
	n.store.AddLog(LogEntry{Kind: KindSystem, Level: tlvl, Actor: Tz("notify.alert_engine"), Host: a.Hostname, Message: verb + "：" + a.Message})
	// Persist alert lifecycle event: write on fire, resolve on recover.
	key := alertKey(a)
	if firing {
		id := n.store.AddAlertRecord(AlertRecord{
			Key:      key,
			HostID:   a.HostID,
			Hostname: a.Hostname,
			IP:       a.IP,
			Level:    a.Level,
			Type:     a.Type,
			Scope:    a.Scope,
			Message:  a.Message,
			Value:    a.Value,
			FiredAt:  a.Timestamp,
		})
		n.mu.Lock()
		n.recordIDs[key] = id
		n.mu.Unlock()
	} else {
		n.store.ResolveAlert(key, time.Now().Unix())
		n.mu.Lock()
		delete(n.recordIDs, key)
		n.mu.Unlock()
	}
	n.enqueuePush(cfg, a, firing)
}

// pushChannels sends the alert text to every enabled bot channel and logs the
// push result.
//
// **只应由投递协程调用**（enqueuePush 里那一条）。安全扫描、API 拨测、SNMP、
// 内容审计、检查项、Agent 升级、Prometheus 规则等十余处此前各自直接调它，于是每一处
// 都会被一条发不出去的渠道按住自己的循环——检查项停跑、拨测停跑，症状各不相同，
// 根因是同一个。它们现在统一走 enqueuePush。
func (n *Notifier) pushChannels(cfg ServerConfig, a Alert, firing bool) {
	// 告警治理：仅对「触发」通知做静默/抑制；「恢复」通知一律照发，避免规则造成"永远告警"错觉。
	//
	// 注意这里的 ActiveAlerts() 读的是**投递时刻**的活跃集合（本函数跑在投递协程上，
	// 见 enqueuePush）。渠道正常时与评估时刻相差毫秒级；渠道堵住时读到的是更新的状态，
	// 对"有更高级别告警时抑制低级别"这条语义只会更准，不会更差。
	if firing {
		if ok, rule := govSilenced(cfg.Governance, a, time.Now()); ok {
			n.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: Tz("notify.notification"), Host: a.Hostname, Message: "静默规则「" + rule + "」已抑制通知：" + a.Message})
			return
		}
		if ok, rule := govInhibited(cfg.Governance, a, n.ActiveAlerts()); ok {
			n.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: Tz("notify.notification"), Host: a.Hostname, Message: "抑制规则「" + rule + "」已抑制通知：" + a.Message})
			return
		}
	}
	// 通知路由：命中路由则仅发其指定渠道；无任何路由命中=默认全部启用渠道（向后兼容）。
	routeSel, routed := govRouteChannels(cfg.Governance, a)
	send := func(ch string) bool { return !routed || routeSel[ch] }

	// noteChannelFailure：告警发不出去是**所有自身故障里后果最重**的一种——它让全部
	// 告警同时形同虚设，而且没有任何人会来告诉你，因为本该来告诉你的正是坏掉的那条
	// 通道。此前它只落一条活动记录，混在成千上万条里，没人会去翻。
	//
	// 不在这里做「连续几次」的判断：那由自身故障归口按指纹统一决定（同一渠道同一错误
	// 连续 3 次开事件），各处自己拍阈值最后一定是有的太吵、有的永远沉默。
	noteChannelFailure := func(channel string, err error) {
		reportFault("notify", channel+"_send_failed", "warning", a.HostID,
			"告警通知渠道「"+channel+"」发送失败："+err.Error()+
				"；该渠道当前发不出告警，此期间的告警不会有人收到", "")
	}

	text := formatAlert(a, firing)
	smsText := formatAlertSMS(a, firing)
	var sent []string
	if send("feishu") && cfg.Feishu.Enabled && cfg.Feishu.Webhook != "" {
		if err := n.sendFeishu(cfg.Feishu, text); err != nil {
			slog.Error(Tz("notify.feishu_failed"), "err", err)
			noteChannelFailure("feishu", err)
			n.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: Tz("notify.notification"), Host: a.Hostname, Message: Tz("log.feishu_failed", err.Error())})
		} else {
			sent = append(sent, Tz("notify.feishu"))
		}
	}
	if send("dingtalk") && cfg.Dingtalk.Enabled && cfg.Dingtalk.Webhook != "" {
		if err := n.sendDingtalk(cfg.Dingtalk, text); err != nil {
			slog.Error(Tz("notify.dingtalk_failed"), "err", err)
			noteChannelFailure("dingtalk", err)
			n.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: Tz("notify.notification"), Host: a.Hostname, Message: Tz("log.dingtalk_failed", err.Error())})
		} else {
			sent = append(sent, Tz("notify.dingtalk"))
		}
	}
	// Email alert notification — sent to the operator's bound email if SMTP is configured
	if send("email") && cfg.SMTP.Enabled && cfg.SMTP.Host != "" {
		html := alertEmailHTML(a, firing)
		okAny := false
		for _, to := range n.cfg.AlertEmails() {
			if err := sendEmail(cfg.SMTP, to, Tz("notify.alert_subject", a.Hostname), html); err != nil {
				slog.Error(Tz("notify.email_failed"), "err", err)
				noteChannelFailure("email", err)
				n.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: Tz("notify.notification"), Host: a.Hostname, Message: Tz("log.email_failed", err.Error())})
			} else {
				okAny = true
			}
		}
		if okAny {
			sent = append(sent, Tz("notify.email"))
		}
	}
	// Custom webhook
	if send("webhook") && cfg.CustomWebhook.Enabled && cfg.CustomWebhook.URL != "" {
		if err := sendCustomWebhook(cfg.CustomWebhook, text, a, firing); err != nil {
			slog.Error(Tz("notify.custom_webhook_failed"), "err", err)
			noteChannelFailure("webhook", err)
			n.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: Tz("notify.notification"), Host: a.Hostname, Message: Tz("log.custom_webhook_failed", err.Error())})
		} else {
			sent = append(sent, Tz("notify.custom_webhook"))
		}
	}
	// SMS notification — use compact SMS text so host/IP/detail are not truncated mid-message.
	if send("sms") && cfg.SMS.Enabled && cfg.SMS.AccessKey != "" {
		if err := n.sendSMS(cfg.SMS, smsText); err != nil {
			slog.Error("sms send failed", "err", err)
			n.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: Tz("notify.notification"), Host: a.Hostname, Message: "短信发送失败: " + err.Error()})
		} else {
			sent = append(sent, "短信")
		}
	}
	// Voice call notification — same compact body as SMS (TTS templates also prefer one line).
	if send("voicecall") && cfg.VoiceCall.Enabled && cfg.VoiceCall.AccessKey != "" {
		if err := n.sendVoiceCall(cfg.VoiceCall, smsText); err != nil {
			slog.Error("voice call send failed", "err", err)
			n.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: Tz("notify.notification"), Host: a.Hostname, Message: "电话通知失败: " + err.Error()})
		} else {
			sent = append(sent, "电话")
		}
	}
	if len(sent) > 0 {
		n.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: Tz("notify.notification"), Host: a.Hostname, Message: Tz("log.pushed", strings.Join(sent, "/"), a.Message)})
	}
}

func alertTypeLabel(typ string) string {
	typeMap := map[string]string{
		"cpu": Tz("notify.type_cpu"), "memory": Tz("notify.type_memory"), "disk": Tz("notify.type_disk"), "diskio": Tz("notify.type_diskio"),
		"iops": Tz("notify.type_iops"), "offline": Tz("notify.type_offline"),
		"load": Tz("notify.type_load"), "gpu": Tz("notify.type_gpu"), "proc": Tz("notify.type_proc"), "check": Tz("notify.type_check"),
		"conn": Tz("notify.type_conn"), "hardware": Tz("notify.type_hardware"),
		"api": Tz("notify.type_api"), "task": Tz("notify.type_task"), "forward": Tz("notify.type_forward"), "hyperv": Tz("notify.type_hyperv"),
		"snmp": Tz("notify.type_snmp"), "trap": Tz("notify.type_trap"), "netflow": Tz("notify.type_netflow"),
		"content_audit": Tz("notify.type_content_audit"),
		"promrule":      Tz("notify.type_promrule"),
	}
	if label := typeMap[typ]; label != "" {
		return label
	}
	return typ
}

func formatAlert(a Alert, firing bool) string {
	status := Tz("notify.fire")
	if !firing {
		status = Tz("notify.recover")
	}
	lv := Tz("notify.warn")
	if a.Level == "critical" {
		lv = Tz("notify.critical")
	}
	typeLabel := alertTypeLabel(a.Type)
	host := a.Hostname
	if host == "" {
		host = a.HostID
	}
	ipLine := ""
	if a.IP != "" {
		ipLine = fmt.Sprintf("\n%s: %s", Tz("notify.ip"), a.IP)
	}
	// 分组紧跟在主机后面：值班的人先要知道"这是哪一摊的机器"，再看是哪一台。
	groupLine := ""
	if g := strings.TrimSpace(a.Group); g != "" {
		groupLine = fmt.Sprintf("\n%s: %s", Tz("notify.group"), g)
	}
	return fmt.Sprintf("%s\n%s: %s%s%s\n%s: %s\n%s: %s\n%s: %s\n%s: %s",
		Tz("notify.title", status), Tz("notify.host"), host, groupLine, ipLine,
		Tz("notify.level"), lv, Tz("notify.type"), typeLabel,
		Tz("notify.detail"), a.Message, Tz("notify.time"), time.Unix(a.Timestamp, 0).Format("2006-01-02 15:04:05"))
}

// formatAlertSMS builds a single-line alert for SMS/TTS templates.
// Priority: brand + status, host, IP, type, full anomaly detail, time — so truncation
// (if any provider limit remains) still keeps machine identity and the exception body.
func formatAlertSMS(a Alert, firing bool) string {
	status := Tz("notify.sms_fire")
	if !firing {
		status = Tz("notify.sms_recover")
	}
	lv := Tz("notify.warn")
	if a.Level == "critical" {
		lv = Tz("notify.critical")
	}
	host := strings.TrimSpace(a.Hostname)
	if host == "" {
		host = strings.TrimSpace(a.HostID)
	}
	typeLabel := alertTypeLabel(a.Type)
	detail := strings.TrimSpace(a.Message)
	ts := time.Unix(a.Timestamp, 0).Format("2006-01-02 15:04:05")
	// 标签与值之间用冒号分隔，避免英文 locale 下出现 Hostweb01 / TypeCPU 粘连。
	joinKV := func(k, v string) string {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			return v
		}
		if v == "" {
			return k
		}
		return k + ":" + v
	}
	var parts []string
	parts = append(parts, "AIOps"+status)
	parts = append(parts, lv)
	if host != "" {
		parts = append(parts, joinKV(Tz("notify.host"), host))
	}
	if g := strings.TrimSpace(a.Group); g != "" {
		parts = append(parts, joinKV(Tz("notify.group"), g))
	}
	if ip := strings.TrimSpace(a.IP); ip != "" {
		parts = append(parts, joinKV("IP", ip))
	}
	if typeLabel != "" {
		parts = append(parts, joinKV(Tz("notify.type"), typeLabel))
	}
	if detail != "" {
		parts = append(parts, joinKV(Tz("notify.detail"), detail))
	}
	parts = append(parts, joinKV(Tz("notify.time"), ts))
	return strings.Join(parts, " ")
}

// SendTest pushes a one-off test message on the enabled channels of the given
// config and returns human-readable errors (empty on full success).
func (n *Notifier) SendTest(cfg ServerConfig) []string {
	msg := Tz("notify.test_msg", time.Now().Format("2006-01-02 15:04:05"))
	var errs []string
	if cfg.Feishu.Enabled && cfg.Feishu.Webhook != "" {
		if err := n.sendFeishu(cfg.Feishu, msg); err != nil {
			errs = append(errs, Tz("notify.feishu")+": "+err.Error())
		}
	}
	if cfg.Dingtalk.Enabled && cfg.Dingtalk.Webhook != "" {
		if err := n.sendDingtalk(cfg.Dingtalk, msg); err != nil {
			errs = append(errs, Tz("notify.dingtalk")+": "+err.Error())
		}
	}
	if cfg.SMTP.Enabled && cfg.SMTP.Host != "" {
		emails := n.cfg.AlertEmails()
		if len(emails) == 0 {
			errs = append(errs, Tz("notify.email")+": "+Tz("notify.no_email"))
		} else {
			html := `<div style="font-family:sans-serif;padding:20px"><h2>AIOps</h2><p>` + Tz("notify.test_email_body") + `</p><p>` + Tz("notify.time") + ": " + time.Now().Format("2006-01-02 15:04:05") + `</p></div>`
			for _, to := range emails {
				if err := sendEmail(cfg.SMTP, to, Tz("notify.test_email_subject"), html); err != nil {
					errs = append(errs, Tz("notify.email")+": "+err.Error())
					break
				}
			}
		}
	}
	if cfg.CustomWebhook.Enabled && cfg.CustomWebhook.URL != "" {
		if err := sendCustomWebhook(cfg.CustomWebhook, msg, Alert{}, false); err != nil {
			errs = append(errs, Tz("notify.custom_webhook")+": "+err.Error())
		}
	}
	if cfg.SMS.Enabled && cfg.SMS.AccessKey != "" {
		if err := n.sendSMS(cfg.SMS, Tz("notify.test_msg_sms", time.Now().Format("2006-01-02 15:04:05"))); err != nil {
			errs = append(errs, "短信: "+err.Error())
		}
	}
	if cfg.VoiceCall.Enabled && cfg.VoiceCall.AccessKey != "" {
		if err := n.sendVoiceCall(cfg.VoiceCall, Tz("notify.test_msg_sms", time.Now().Format("2006-01-02 15:04:05"))); err != nil {
			errs = append(errs, "电话: "+err.Error())
		}
	}
	if !cfg.Feishu.Enabled && !cfg.Dingtalk.Enabled && !cfg.SMTP.Enabled && !cfg.CustomWebhook.Enabled && !cfg.SMS.Enabled && !cfg.VoiceCall.Enabled {
		errs = append(errs, Tz("notify.no_channel"))
	}
	return errs
}

// alertEmailHTML renders an alert notification as an HTML email body.
func alertEmailHTML(a Alert, firing bool) string {
	status := Tz("notify.email_alert_fired")
	headColor := "#e74c3c"
	lvlColor := "#f39c12"
	if a.Level == "critical" {
		lvlColor = "#e74c3c"
	}
	if !firing {
		status = Tz("notify.email_alert_recovered")
		headColor = "#27ae60"
		lvlColor = "#27ae60"
	}
	lv := Tz("notify.warn")
	if a.Level == "critical" {
		lv = Tz("notify.critical")
	}
	typeLabel := alertTypeLabel(a.Type)
	host := a.Hostname
	if host == "" {
		host = a.HostID
	}
	// 邮件是 HTML：主机名来自 Agent 上报、分组名来自用户输入，两者都可能带 < >，
	// 不转义会把整封邮件的表格撑坏（并给出一条注入面）。
	host = html.EscapeString(host)
	typeLabel = html.EscapeString(typeLabel)
	detail := html.EscapeString(a.Message)
	ipLine := ""
	if a.IP != "" {
		ipLine = `<tr><td style="color:#888;padding:4px 0">` + Tz("notify.ip") + `</td><td style="padding:4px 0">` + html.EscapeString(a.IP) + `</td></tr>`
	}
	groupLine := ""
	if g := strings.TrimSpace(a.Group); g != "" {
		groupLine = `<tr><td style="color:#888;padding:4px 0">` + Tz("notify.group") + `</td><td style="padding:4px 0">` + html.EscapeString(g) + `</td></tr>`
	}
	return fmt.Sprintf(`<div style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px">
  <div style="color:#888;font-size:12px;margin-bottom:4px">AIOps</div>
  <h2 style="color:%s">%s</h2>
  <table style="width:100%%;border-collapse:collapse">
    <tr><td style="color:#888;padding:4px 0;width:80px">%s</td><td style="padding:4px 0;font-weight:bold">%s</td></tr>
    %s
    %s
    <tr><td style="color:#888;padding:4px 0">%s</td><td style="padding:4px 0;color:%s">%s</td></tr>
    <tr><td style="color:#888;padding:4px 0">%s</td><td style="padding:4px 0">%s</td></tr>
    <tr><td style="color:#888;padding:4px 0">%s</td><td style="padding:4px 0;word-break:break-all">%s</td></tr>
    <tr><td style="color:#888;padding:4px 0">%s</td><td style="padding:4px 0">%s</td></tr>
  </table>
</div>`,
		headColor, status, Tz("notify.host"), host, groupLine, ipLine,
		Tz("notify.level"), lvlColor, lv,
		Tz("notify.type"), typeLabel,
		Tz("notify.detail"), detail,
		Tz("notify.time"), time.Unix(a.Timestamp, 0).Format("2006-01-02 15:04:05"))
}

func (n *Notifier) sendFeishu(c WebhookConfig, text string) error {
	body, _ := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	})
	return n.post(c.Webhook, body)
}

func (n *Notifier) sendDingtalk(c WebhookConfig, text string) error {
	webhook := c.Webhook
	if c.Secret != "" {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		sep := "?"
		if strings.Contains(webhook, "?") {
			sep = "&"
		}
		webhook = webhook + sep + "timestamp=" + ts + "&sign=" + dingSign(ts, c.Secret)
	}
	body, _ := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": text},
	})
	return n.post(webhook, body)
}

// dingSign implements DingTalk's HMAC-SHA256 signature: base64(hmac(secret,
// "timestamp\nsecret")), URL-encoded.
func dingSign(ts, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts + "\n" + secret))
	return url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil)))
}

func (n *Notifier) post(webhook string, body []byte) error {
	resp, err := n.httpc.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	// Feishu / DingTalk return HTTP 200 even on business errors (bad keyword,
	// sign mismatch, ...); the real status is in the body's code / errcode.
	var r struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	if json.Unmarshal(rb, &r) == nil && (r.Code != 0 || r.Errcode != 0) {
		code, msg := r.Code, r.Msg
		if code == 0 {
			code, msg = r.Errcode, r.Errmsg
		}
		return fmt.Errorf("API returned code=%d %s", code, msg)
	}
	return nil
}

// ----- cloud SMS / voice notification helpers -----

// aliyunEncode 按阿里云 API 签名 V1 规范做百分号编码。
// 规则：A-Z a-z 0-9 - _ . ~ 不编码；空格编码为 %20（非 +）；
// 其余全部编码为 %XX（大写十六进制）。这与 Go 标准库 url.QueryEscape
// 的关键区别在于空格 → %20，确保签名计算与阿里云服务端一致。
func aliyunEncode(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		case r == ' ':
			b.WriteString("%20")
		default:
			// 统一走 UTF-8 字节编码，避免 rune 直接转义
			for _, by := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", by)
			}
		}
	}
	return b.String()
}

// sha256Hex 返回字符串的 SHA-256 哈希（小写十六进制）。
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// aliyunSignV3 按阿里云 API 签名 V3（ACS3-HMAC-SHA256）计算签名。
// 规范：
//
//	canonicalRequest = HTTPMethod + "\n" + CanonicalURI + "\n" + CanonicalQueryString +
//	                   "\n" + CanonicalHeaders + "\n" + SignedHeaders + "\n" + HashedPayload
//	stringToSign = "ACS3-HMAC-SHA256\n" + SHA256(canonicalRequest)
//	signature = Hex(HMAC-SHA256(AccessKeySecret, stringToSign))
func aliyunSignV3(method, canonicalURI, queryString, payload string, headers map[string]string, signedHeaders []string, secret string) string {
	// 1) 构建规范化请求头（按 signedHeaders 顺序，全小写，值去首尾空白）
	sort.Strings(signedHeaders)
	var ch strings.Builder
	for _, h := range signedHeaders {
		ch.WriteString(h)
		ch.WriteByte(':')
		ch.WriteString(strings.TrimSpace(headers[h]))
		ch.WriteByte('\n')
	}
	sh := strings.Join(signedHeaders, ";")

	// 2) 哈希请求体
	hashedPayload := sha256Hex(payload)

	// 3) 规范化请求 → 哈希
	canonicalRequest := method + "\n" + canonicalURI + "\n" + queryString + "\n" +
		ch.String() + "\n" + sh + "\n" + hashedPayload
	hashedCanonicalRequest := sha256Hex(canonicalRequest)

	// 4) 待签字符串
	stringToSign := "ACS3-HMAC-SHA256\n" + hashedCanonicalRequest

	// 5) HMAC-SHA256(AccessKeySecret, stringToSign) → hex
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	return hex.EncodeToString(mac.Sum(nil))
}

// sendSMS sends an alert via cloud SMS API (Aliyun / Huawei / Tencent).
func (n *Notifier) sendSMS(cfg SMSConfig, text string) error {
	switch cfg.Provider {
	case "", "aliyun":
		return n.sendAliyunSMS(cfg, text)
	case "huawei":
		return n.sendHuaweiSMS(cfg, text)
	case "tencent":
		return n.sendTencentSMS(cfg, text)
	default:
		return fmt.Errorf("SMS provider %s not yet implemented", cfg.Provider)
	}
}

// sendAliyunSMS 使用阿里云 API 签名 V3（ACS3-HMAC-SHA256）发送短信。
// 参数通过 POST + Query String 传递，签名写入 Authorization 请求头。
func (n *Notifier) sendAliyunSMS(cfg SMSConfig, text string) error {
	phones := strings.Join(cfg.Phones, ",")
	if phones == "" {
		return fmt.Errorf("no phone numbers configured")
	}

	// 构建查询参数（按 key 排序 → 规范化查询字符串）。
	// TemplateParam 处理：
	//   - 为空 → 默认 {"message":"<告警内容>"}（仅适配变量名恰为 message 的模板）；
	//   - 含 ${...} 占位符（如 ${MESSAGE}）→ 整体替换为实际告警内容（JSON 转义），
	//     从而适配任意变量名的模板：填 {"MESSAGE":"${MESSAGE}"} 即动态注入告警内容；
	//   - 纯静态 JSON（无 ${...}）→ 原样发送（固定文案）。
	// 先清洗告警文本为短信可接受形态（去 emoji/换行/特殊符号、截断长度），否则阿里云会报
	// isv.UNSUPPORTED_SMS_CONTENT（如测试文案里的 ✅ 表情、换行、【】）。
	safe := smsSafeVar(text)
	jsonEsc := func(s string) string { b, _ := json.Marshal(s); return string(b[1 : len(b)-1]) }
	templateParam := cfg.TemplateParam
	if templateParam == "" {
		templateParam = `{"message":"` + jsonEsc(safe) + `"}`
	} else if strings.Contains(templateParam, "${") {
		templateParam = notifyTplVarRe.ReplaceAllStringFunc(templateParam, func(string) string { return jsonEsc(safe) })
	}
	params := map[string]string{
		"PhoneNumbers":  phones,
		"SignName":      cfg.SignName,
		"TemplateCode":  cfg.TemplateCode,
		"TemplateParam": templateParam,
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		qs = append(qs, aliyunEncode(k)+"="+aliyunEncode(params[k]))
	}
	queryString := strings.Join(qs, "&")

	// 构建 V3 签名所需请求头
	now := time.Now().UTC()
	xAcsDate := now.Format("2006-01-02T15:04:05Z")
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	xAcsNonce := hex.EncodeToString(nonce)
	host := "dysmsapi.aliyuncs.com"

	headers := map[string]string{
		"host":                  host,
		"x-acs-action":          "SendSms",
		"x-acs-version":         "2017-05-25",
		"x-acs-signature-nonce": xAcsNonce,
		"x-acs-date":            xAcsDate,
		"x-acs-content-sha256":  sha256Hex(""),
	}
	signedHeaders := []string{"host", "x-acs-action", "x-acs-content-sha256", "x-acs-date", "x-acs-signature-nonce", "x-acs-version"}

	// 去首尾空白，防止粘贴凭证时带入空格/换行导致签名不匹配
	signature := aliyunSignV3("POST", "/", queryString, "", headers, signedHeaders, strings.TrimSpace(cfg.SecretKey))

	// 构建 Authorization 请求头
	auth := fmt.Sprintf("ACS3-HMAC-SHA256 Credential=%s,SignedHeaders=%s,Signature=%s",
		strings.TrimSpace(cfg.AccessKey), strings.Join(signedHeaders, ";"), signature)

	req, _ := http.NewRequest(http.MethodPost, "https://"+host+"/?"+queryString, nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("x-acs-action", "SendSms")
	req.Header.Set("x-acs-version", "2017-05-25")
	req.Header.Set("x-acs-signature-nonce", xAcsNonce)
	req.Header.Set("x-acs-date", xAcsDate)
	req.Header.Set("x-acs-content-sha256", sha256Hex(""))
	req.Header.Set("Host", host)

	resp, err := n.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var r struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if json.Unmarshal(rb, &r) == nil && r.Code != "OK" {
		return fmt.Errorf("SMS API: %s (%s)", r.Message, r.Code)
	}
	return nil
}

// sendVoiceCall sends an alert via cloud voice call (TTS) API.
func (n *Notifier) sendVoiceCall(cfg VoiceCallConfig, text string) error {
	switch cfg.Provider {
	case "", "aliyun":
		return n.sendAliyunVoiceCall(cfg, text)
	case "huawei":
		return n.sendHuaweiVoiceCall(cfg, text)
	case "tencent":
		return n.sendTencentVoiceCall(cfg, text)
	default:
		return fmt.Errorf("voice call provider %s not yet implemented", cfg.Provider)
	}
}

// sendAliyunVoiceCall 使用阿里云 API 签名 V3（ACS3-HMAC-SHA256）发送语音通知。
func (n *Notifier) sendAliyunVoiceCall(cfg VoiceCallConfig, text string) error {
	phones := strings.Join(cfg.CalledNumbers, ",")
	if phones == "" {
		return fmt.Errorf("no called numbers configured")
	}
	// Build TTS params — 与短信一致：清洗告警文本，空模板默认 {"message":...}；含 ${...} 占位符
	// 则整体替换为告警内容（JSON 转义），从而适配任意变量名的 TTS 模板。
	safe := smsSafeVarN(text, voiceSafeVarMax)
	jsonEsc := func(s string) string { b, _ := json.Marshal(s); return string(b[1 : len(b)-1]) }
	tsParam := cfg.TTSParam
	if tsParam == "" {
		tsParam = `{"message":"` + jsonEsc(safe) + `"}`
	} else if strings.Contains(tsParam, "${") {
		tsParam = notifyTplVarRe.ReplaceAllStringFunc(tsParam, func(string) string { return jsonEsc(safe) })
	}
	calledNumber := cfg.CalledNumbers[0] // SingleCallByTts only supports one callee per call

	// 构建查询参数（按 key 排序 → 规范化查询字符串）
	params := map[string]string{
		"CalledNumber": calledNumber,
		"TtsCode":      cfg.TTSCode,
		"TtsParam":     tsParam,
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		qs = append(qs, aliyunEncode(k)+"="+aliyunEncode(params[k]))
	}
	queryString := strings.Join(qs, "&")

	// 构建 V3 签名所需请求头
	now := time.Now().UTC()
	xAcsDate := now.Format("2006-01-02T15:04:05Z")
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	xAcsNonce := hex.EncodeToString(nonce)
	host := "dyvmsapi.aliyuncs.com"

	headers := map[string]string{
		"host":                  host,
		"x-acs-action":          "SingleCallByTts",
		"x-acs-version":         "2017-05-25",
		"x-acs-signature-nonce": xAcsNonce,
		"x-acs-date":            xAcsDate,
		"x-acs-content-sha256":  sha256Hex(""),
	}
	signedHeaders := []string{"host", "x-acs-action", "x-acs-content-sha256", "x-acs-date", "x-acs-signature-nonce", "x-acs-version"}

	// 去首尾空白，防止粘贴凭证时带入空格/换行导致签名不匹配
	signature := aliyunSignV3("POST", "/", queryString, "", headers, signedHeaders, strings.TrimSpace(cfg.SecretKey))

	auth := fmt.Sprintf("ACS3-HMAC-SHA256 Credential=%s,SignedHeaders=%s,Signature=%s",
		strings.TrimSpace(cfg.AccessKey), strings.Join(signedHeaders, ";"), signature)

	req, _ := http.NewRequest(http.MethodPost, "https://"+host+"/?"+queryString, nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("x-acs-action", "SingleCallByTts")
	req.Header.Set("x-acs-version", "2017-05-25")
	req.Header.Set("x-acs-signature-nonce", xAcsNonce)
	req.Header.Set("x-acs-date", xAcsDate)
	req.Header.Set("x-acs-content-sha256", sha256Hex(""))
	req.Header.Set("Host", host)

	resp, err := n.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var r struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if json.Unmarshal(rb, &r) == nil && r.Code != "OK" {
		return fmt.Errorf("VoiceCall API: %s (%s)", r.Message, r.Code)
	}
	return nil
}

// ---------- 华为云短信（X-WSSE 鉴权，API v2）----------

// sendHuaweiSMS 使用华为云 SMS API v2 + X-WSSE 鉴权发送短信。
// 文档：https://support.huaweicloud.com/api-msgsms/sms_05_0001.html
func (n *Notifier) sendHuaweiSMS(cfg SMSConfig, text string) error {
	phones := strings.Join(cfg.Phones, ",")
	if phones == "" {
		return fmt.Errorf("no phone numbers configured")
	}
	projectID := strings.TrimSpace(cfg.AppID)
	if projectID == "" {
		return fmt.Errorf("Huawei Cloud SMS requires project_id (AppID)")
	}
	// 华为云短信必须携带「通道号 from」，缺失会被服务端拒绝（此前硬编码为空导致必失败）。
	from := strings.TrimSpace(cfg.Sender)
	if from == "" {
		return fmt.Errorf("华为云短信需配置通道号（Sender/from）")
	}

	// 构建模板参数：优先用用户自定义 JSON 数组，否则用清洗后的告警正文（保证主机/IP/详情完整且可过审）。
	safe := smsSafeVar(text)
	var templateParas []string
	tp := strings.TrimSpace(cfg.TemplateParam)
	if tp != "" {
		if err := json.Unmarshal([]byte(tp), &templateParas); err != nil {
			templateParas = []string{safe}
		}
	} else {
		templateParas = []string{safe}
	}

	// 国际号码格式：不加前缀的默认 +86
	var toList []string
	for _, p := range strings.Split(phones, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "+") {
			p = "+86" + p
		}
		toList = append(toList, p)
	}

	// 华为云 SMS 区域端点（默认 cn-north-4）
	endpoint := "https://smsapi.cn-north-4.myhuaweicloud.com:443"
	url := fmt.Sprintf("%s/v2/%s/sms/batch-send-sms", endpoint, projectID)

	// X-WSSE 鉴权 — request header only; never log xWsse / AccessKey / digest.
	nonceBytes := make([]byte, 16)
	_, _ = rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	created := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	h := sha256.New()
	h.Write([]byte(nonce + created + cfg.SecretKey))
	passwordDigest := base64.StdEncoding.EncodeToString(h.Sum(nil))
	xWsse := fmt.Sprintf(`UsernameToken Username="%s", PasswordDigest="%s", Nonce="%s", Created="%s"`,
		cfg.AccessKey, passwordDigest, nonce, created)

	body := map[string]any{
		"from":          from,
		"to":            strings.Join(toList, ","),
		"templateId":    cfg.TemplateCode,
		"templateParas": templateParas,
		"signature":     cfg.SignName,
	}
	bodyBytes, _ := json.Marshal(body)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Authorization", `WSSE realm="SDP", profile="UsernameToken", type="Appkey"`)
	req.Header.Set("X-WSSE", xWsse)

	resp, err := n.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var r struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	}
	if json.Unmarshal(rb, &r) == nil && r.Code != "000000" {
		return fmt.Errorf("Huawei SMS API: %s (%s)", r.Description, r.Code)
	}
	return nil
}

// ---------- 腾讯云短信（TC3-HMAC-SHA256 签名，API 2021-01-11）----------

// sendTencentSMS 使用腾讯云 SMS API 2021-01-11 + TC3-HMAC-SHA256 签名发送短信。
func (n *Notifier) sendTencentSMS(cfg SMSConfig, text string) error {
	phones := strings.Join(cfg.Phones, ",")
	if phones == "" {
		return fmt.Errorf("no phone numbers configured")
	}
	sdkAppID := strings.TrimSpace(cfg.AppID)
	if sdkAppID == "" {
		return fmt.Errorf("Tencent Cloud SMS requires SmsSdkAppId (AppID)")
	}

	// 构建模板参数：优先用用户自定义 JSON 数组，否则用清洗后的告警正文（保证主机/IP/详情完整且可过审）。
	safe := smsSafeVar(text)
	var templateParamSet []string
	tp := strings.TrimSpace(cfg.TemplateParam)
	if tp != "" {
		if err := json.Unmarshal([]byte(tp), &templateParamSet); err != nil {
			templateParamSet = []string{safe}
		}
	} else {
		templateParamSet = []string{safe}
	}

	// 国际号码格式：腾讯云要求 +86 前缀
	var phoneSet []string
	for _, p := range strings.Split(phones, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "+") {
			p = "+86" + p
		}
		phoneSet = append(phoneSet, p)
	}

	host := "sms.tencentcloudapi.com"
	service := "sms"
	action := "SendSms"
	version := "2021-01-11"

	payload := map[string]any{
		"PhoneNumberSet":   phoneSet,
		"SmsSdkAppId":      sdkAppID,
		"SignName":         cfg.SignName,
		"TemplateId":       cfg.TemplateCode,
		"TemplateParamSet": templateParamSet,
	}
	payloadBytes, _ := json.Marshal(payload)
	payloadStr := string(payloadBytes)

	timestamp := time.Now().Unix()
	auth, err := tencentSignV3(cfg.AccessKey, cfg.SecretKey, host, service, action, version, payloadStr, timestamp)
	if err != nil {
		return err
	}

	req, _ := http.NewRequest(http.MethodPost, "https://"+host, bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	tcRegion := strings.TrimSpace(cfg.Region)
	if tcRegion == "" {
		tcRegion = "ap-guangzhou" // 腾讯云短信/语音必需地域参数，缺失会被拒；默认广州
	}
	req.Header.Set("X-TC-Region", tcRegion) // 此前遗漏，导致腾讯云短信/语音必失败
	req.Header.Set("Authorization", auth)

	resp, err := n.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var r struct {
		Response struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if json.Unmarshal(rb, &r) == nil && r.Response.Error != nil {
		return fmt.Errorf("Tencent SMS API: %s (%s)", r.Response.Error.Message, r.Response.Error.Code)
	}
	return nil
}

// ---------- 华为云语音通知（X-WSSE 鉴权）----------

// sendHuaweiVoiceCall 发送华为云语音通知（TTS）。
func (n *Notifier) sendHuaweiVoiceCall(cfg VoiceCallConfig, text string) error {
	if len(cfg.CalledNumbers) == 0 {
		return fmt.Errorf("no called numbers configured")
	}
	projectID := strings.TrimSpace(cfg.AppID)
	if projectID == "" {
		return fmt.Errorf("Huawei Cloud Voice Call requires project_id (AppID)")
	}
	// 华为云语音通知必须携带主叫号码 displayNbr（购买的固话/号码），缺失会被拒。
	displayNbr := strings.TrimSpace(cfg.DisplayNbr)
	if displayNbr == "" {
		return fmt.Errorf("华为云语音需配置主叫号码（displayNbr）")
	}

	// 被叫号码
	called := cfg.CalledNumbers[0]
	if !strings.HasPrefix(called, "+") {
		called = "+86" + called
	}

	// 模板参数
	var templateParas []string
	tp := strings.TrimSpace(cfg.TTSParam)
	if tp != "" {
		if err := json.Unmarshal([]byte(tp), &templateParas); err != nil {
			templateParas = []string{text}
		}
	} else {
		templateParas = []string{text}
	}

	endpoint := "https://rtc-api.myhuaweicloud.com:443"
	url := fmt.Sprintf("%s/v2/%s/voice/tts", endpoint, projectID)

	// X-WSSE 鉴权（同短信）— request header only; never log credentials.
	nonceBytes := make([]byte, 16)
	_, _ = rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	created := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	h := sha256.New()
	h.Write([]byte(nonce + created + cfg.SecretKey))
	passwordDigest := base64.StdEncoding.EncodeToString(h.Sum(nil))
	xWsse := fmt.Sprintf(`UsernameToken Username="%s", PasswordDigest="%s", Nonce="%s", Created="%s"`,
		cfg.AccessKey, passwordDigest, nonce, created)

	body := map[string]any{
		"displayNbr":    displayNbr,
		"called":        called,
		"templateId":    cfg.TTSCode,
		"templateParas": templateParas,
		"playTimes":     1,
	}
	bodyBytes, _ := json.Marshal(body)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Authorization", `WSSE realm="SDP", profile="UsernameToken", type="Appkey"`)
	req.Header.Set("X-WSSE", xWsse)

	resp, err := n.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var r struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	}
	if json.Unmarshal(rb, &r) == nil && r.Code != "000000" {
		return fmt.Errorf("Huawei VoiceCall API: %s (%s)", r.Description, r.Code)
	}
	return nil
}

// ---------- 腾讯云语音通知（TC3-HMAC-SHA256 签名，API 2020-02-24）----------

// sendTencentVoiceCall 发送腾讯云语音通知（TTS）。
func (n *Notifier) sendTencentVoiceCall(cfg VoiceCallConfig, text string) error {
	if len(cfg.CalledNumbers) == 0 {
		return fmt.Errorf("no called numbers configured")
	}
	voiceAppID := strings.TrimSpace(cfg.AppID)
	if voiceAppID == "" {
		return fmt.Errorf("Tencent Cloud Voice Call requires VoiceSdkAppId (AppID)")
	}

	called := cfg.CalledNumbers[0]
	if !strings.HasPrefix(called, "+") {
		called = "+86" + called
	}

	var templateParamSet []string
	tp := strings.TrimSpace(cfg.TTSParam)
	if tp != "" {
		if err := json.Unmarshal([]byte(tp), &templateParamSet); err != nil {
			templateParamSet = []string{text}
		}
	} else {
		templateParamSet = []string{text}
	}

	host := "vms.tencentcloudapi.com"
	service := "vms"
	action := "SendTts"
	version := "2020-02-24"

	payload := map[string]any{
		"TemplateId":       cfg.TTSCode,
		"CalledNumber":     called,
		"VoiceSdkAppid":    voiceAppID,
		"TemplateParamSet": templateParamSet,
		"PlayTimes":        1,
	}
	payloadBytes, _ := json.Marshal(payload)
	payloadStr := string(payloadBytes)

	timestamp := time.Now().Unix()
	auth, err := tencentSignV3(cfg.AccessKey, cfg.SecretKey, host, service, action, version, payloadStr, timestamp)
	if err != nil {
		return err
	}

	req, _ := http.NewRequest(http.MethodPost, "https://"+host, bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	tcRegion := strings.TrimSpace(cfg.Region)
	if tcRegion == "" {
		tcRegion = "ap-guangzhou" // 腾讯云短信/语音必需地域参数，缺失会被拒；默认广州
	}
	req.Header.Set("X-TC-Region", tcRegion) // 此前遗漏，导致腾讯云短信/语音必失败
	req.Header.Set("Authorization", auth)

	resp, err := n.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var r struct {
		Response struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if json.Unmarshal(rb, &r) == nil && r.Response.Error != nil {
		return fmt.Errorf("Tencent VoiceCall API: %s (%s)", r.Response.Error.Message, r.Response.Error.Code)
	}
	return nil
}

// tencentSignV3 计算腾讯云 API TC3-HMAC-SHA256 签名。
func tencentSignV3(secretID, secretKey, host, service, action, version, payload string, timestamp int64) (string, error) {
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")

	// 1) 规范化请求
	canonicalHeaders := fmt.Sprintf("content-type:application/json\nhost:%s\n", host)
	signedHeaders := "content-type;host"
	hashedPayload := sha256Hex(payload)
	canonicalRequest := fmt.Sprintf("POST\n/\n\n%s\n%s\n%s", canonicalHeaders, signedHeaders, hashedPayload)

	// 2) 待签字符串
	credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, service)
	hashedCanonical := sha256Hex(canonicalRequest)
	stringToSign := fmt.Sprintf("TC3-HMAC-SHA256\n%d\n%s\n%s", timestamp, credentialScope, hashedCanonical)

	// 3) 派生签名密钥
	secretDate := hmacSHA256Bytes([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256Bytes(secretDate, service)
	secretSigning := hmacSHA256Bytes(secretService, "tc3_request")

	// 4) 签名
	signature := hex.EncodeToString(hmacSHA256Bytes(secretSigning, stringToSign))

	auth := fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		secretID, credentialScope, signedHeaders, signature)
	return auth, nil
}

// hmacSHA256Bytes 返回 HMAC-SHA256 的原始字节。
func hmacSHA256Bytes(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// sendCustomWebhook sends an alert to a user-defined HTTP(S) endpoint.
func sendCustomWebhook(cfg CustomWebhookConfig, text string, a Alert, firing bool) error {
	method := cfg.Method
	if method == "" {
		method = "POST"
	}
	contentType := cfg.ContentType
	if contentType == "" {
		contentType = "application/json"
	}

	// Build body: use template if provided, otherwise default JSON
	var body []byte
	if cfg.BodyTemplate != "" {
		tmpl, err := webhookTemplate(cfg.BodyTemplate)
		if err != nil {
			return fmt.Errorf("template parse error: %w", err)
		}
		data := map[string]any{
			"Level":     a.Level,
			"Type":      a.Type,
			"Hostname":  a.Hostname,
			"HostID":    a.HostID,
			"IP":        a.IP,
			"Message":   a.Message,
			"Value":     a.Value,
			"Timestamp": a.Timestamp,
			"Firing":    firing,
			"Text":      text,
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("template exec error: %w", err)
		}
		body = buf.Bytes()
	} else {
		body, _ = json.Marshal(map[string]any{
			"text":      text,
			"level":     a.Level,
			"type":      a.Type,
			"hostname":  a.Hostname,
			"message":   a.Message,
			"value":     a.Value,
			"timestamp": a.Timestamp,
			"firing":    firing,
		})
	}

	var req *http.Request
	var err error
	if method == "GET" {
		req, err = http.NewRequest("GET", cfg.URL, nil)
	} else {
		req, err = http.NewRequest(method, cfg.URL, bytes.NewReader(body))
	}
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)

	// Parse optional custom headers (JSON key-value)
	if cfg.Headers != "" {
		var hdrs map[string]string
		if json.Unmarshal([]byte(cfg.Headers), &hdrs) == nil {
			for k, v := range hdrs {
				req.Header.Set(k, v)
			}
		}
	}

	client := newGuardedHTTPClient(8 * time.Second) // SSRF：自定义 webhook URL 用户可配，拦元数据/链路本地
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

// notifyTplVarRe 匹配短信/语音模板参数里的 ${var} 占位。原来在每次发送时
// regexp.MustCompile 一遍——告警风暴里就是每条通知编译一次正则。
var notifyTplVarRe = regexp.MustCompile(`\$\{[^}]*\}`)

// webhookTemplateCache 按模板原文缓存已解析的 text/template：同一条 webhook 配置在一次
// 告警风暴里会被成百上千次地用到，而解析结果只取决于模板文本本身。
var webhookTemplateCache sync.Map // template text -> *template.Template

func webhookTemplate(text string) (*template.Template, error) {
	if t, ok := webhookTemplateCache.Load(text); ok {
		return t.(*template.Template), nil
	}
	t, err := template.New("webhook").Parse(text)
	if err != nil {
		return nil, err
	}
	webhookTemplateCache.Store(text, t)
	return t, nil
}
