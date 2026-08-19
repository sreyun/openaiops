package main

import (
	"strings"
	"testing"
)

// 告警只写主机名和 IP 时，值班的人还得回面板查"这台机器是哪一摊的"。层级本来就是用来
// 区分同名节点的（两个机房各有一个「数据库」），所以带的必须是完整路径而不是一级分类。
func TestDecorateAlertGroupsUsesFullPath(t *testing.T) {
	cs := testConfigStore(t)
	idc, err := cs.addHostFolder("", "IDC机房")
	if err != nil {
		t.Fatal(err)
	}
	east, err := cs.addHostFolder(idc.ID, "华东")
	if err != nil {
		t.Fatal(err)
	}
	db, err := cs.addHostFolder(east.ID, "数据库")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.assignHostFolder("h1", db.ID); err != nil {
		t.Fatal(err)
	}

	alerts := []Alert{
		{HostID: "h1", Hostname: "mysql-01", Type: "diskio"},
		{HostID: "h2", Hostname: "未分组机器", Type: "cpu"},
		{HostID: "h1", Hostname: "mysql-01", Type: "memory", Group: "别覆盖我"},
		{Type: "snmp"}, // 非主机告警：没有 HostID，不该被塞分组
	}
	out := cs.decorateAlertGroups(alerts)

	if got, want := out[0].Group, "IDC机房 / 华东 / 数据库"; got != want {
		t.Errorf("完整路径 = %q, want %q", got, want)
	}
	if out[1].Group != "" {
		t.Errorf("未分组主机不该有分组: %q", out[1].Group)
	}
	if out[2].Group != "别覆盖我" {
		t.Errorf("已有分组被覆盖: %q", out[2].Group)
	}
	if out[3].Group != "" {
		t.Errorf("无 HostID 的告警被塞了分组: %q", out[3].Group)
	}
}

// 分组变动不能改变告警身份，否则把一台机器挪个分组，所有在途告警会"恢复"再"触发"一遍。
func TestAlertKeyIgnoresGroup(t *testing.T) {
	a := Alert{HostID: "h1", Type: "cpu", Scope: ""}
	b := a
	b.Group = "IDC机房 / 华东"
	if alertKey(a) != alertKey(b) {
		t.Fatalf("分组进了去重键：%q vs %q", alertKey(a), alertKey(b))
	}
}

func TestFormatAlertCarriesGroup(t *testing.T) {
	a := Alert{HostID: "h1", Hostname: "mysql-01", IP: "10.34.45.119", Level: "critical",
		Type: "diskio", Message: "磁盘 IO 负载过高", Group: "IDC机房 / 华东 / 数据库"}
	got := formatAlert(a, true)
	if !strings.Contains(got, "IDC机房 / 华东 / 数据库") {
		t.Errorf("通知文案缺少分组路径:\n%s", got)
	}
	// 分组要排在主机之后、IP 之前：先答"哪一摊"，再答"哪台"。
	iHost := strings.Index(got, "mysql-01")
	iGroup := strings.Index(got, "IDC机房")
	iIP := strings.Index(got, "10.34.45.119")
	if !(iHost < iGroup && iGroup < iIP) {
		t.Errorf("字段顺序不对（主机→分组→IP）:\n%s", got)
	}

	a.Group = ""
	if strings.Contains(formatAlert(a, true), "分组") {
		t.Error("没有分组时不该留空行/空字段")
	}
}

func TestFormatAlertSMSCarriesGroup(t *testing.T) {
	a := Alert{HostID: "h1", Hostname: "mysql-01", IP: "10.34.45.119", Level: "warning",
		Type: "memory", Message: "内存使用率 91%", Group: "IDC机房 / 华东"}
	got := formatAlertSMS(a, true)
	if !strings.Contains(got, "IDC机房 / 华东") {
		t.Errorf("短信缺少分组:\n%s", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("短信必须是单行:\n%s", got)
	}
}

// 邮件是 HTML：分组名与主机名都可能带 < >（分组名只过滤了斜杠和换行）。
func TestFormatAlertEmailEscapes(t *testing.T) {
	a := Alert{HostID: "h1", Hostname: `<img src=x onerror=alert(1)>`, IP: "10.0.0.1",
		Level: "critical", Type: "cpu", Message: `<script>bad()</script>`,
		Group: `<b>组</b>`}
	got := alertEmailHTML(a, true)
	if strings.Contains(got, "<img src=x") || strings.Contains(got, "<script>") || strings.Contains(got, "<b>组</b>") {
		t.Errorf("邮件正文未转义:\n%s", got)
	}
	if !strings.Contains(got, "&lt;b&gt;组&lt;/b&gt;") {
		t.Errorf("分组应以转义形式出现:\n%s", got)
	}
}
