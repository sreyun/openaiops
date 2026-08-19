package main

import (
	"strings"
	"testing"
)

// 敏感路径闸门原来挂在模块名上（只查 file_head / file_stat / copy），于是
// java_exception_scan 的 args.path——Agent 拿到它就直接读那个文件——整个绕过了闸门。
// 闸门必须挂在**参数**上：下一个带路径参数的模块默认被覆盖，而不是等作者想起来补一行。
func TestModulePathArgsAreGatedNotJustFileHead(t *testing.T) {
	denied := []struct {
		mod, key, val string
	}{
		{"java_exception_scan", "path", "/etc/shadow"},
		{"java_exception_scan", "path", "/opt/aiops-agent/config.yaml"},
		{"java_exception_scan", "path", "/proc/self/environ"},
		{"file_head", "path", "/etc/./shadow"},
		{"file_stat", "path", "/root/.ssh/id_rsa"},
		{"copy", "dest", "/etc/sudoers"},
		{"container_compose", "file", "/home/app/.ssh/config"},
	}
	for _, c := range denied {
		st := PlaybookStep{Module: c.mod, Args: map[string]string{c.key: c.val}}
		if c.mod == "copy" {
			st.Args["name"] = "x"
		}
		if c.mod == "file_stat" || c.mod == "file_head" {
			st.Args["path"] = c.val
		}
		err := validatePlaybookModule(st)
		if err == nil {
			t.Errorf("%s %s=%s 未被拦截", c.mod, c.key, c.val)
			continue
		}
		if !strings.Contains(err.Error(), "敏感路径") {
			t.Errorf("%s %s=%s 的报错要说清是敏感路径：%v", c.mod, c.key, c.val, err)
		}
	}

	// 正常路径不能被误伤，否则这道闸会被当成"模块坏了"绕过去。
	allowed := []PlaybookStep{
		{Module: "java_exception_scan", Args: map[string]string{"path": "/var/log/app/app.log"}},
		{Module: "file_head", Args: map[string]string{"path": "/var/log/messages"}},
		{Module: "copy", Args: map[string]string{"dest": "/opt/app/app.conf"}},
	}
	for _, st := range allowed {
		if err := validatePlaybookModule(st); err != nil {
			t.Errorf("%s 正常路径被误伤：%v", st.Module, err)
		}
	}
}
