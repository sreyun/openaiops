package main

import (
	"strings"
	"time"
)

// Windows 升级的「换版成功」与「服务起来了」是两件事，本文件负责的就是它们的差集。
//
// 助手在换完二进制之后调用 Restart-Agent。它有三种结果：
//
//	service   Windows 服务真的 Running —— 唯一的成功
//	usermode  二进制在跑，但只是一个游离进程：这次登录会话结束、或者机器一重启，
//	          它就没了，而且没有任何东西会把它拉回来
//	failed    什么都没起来 —— 助手自己回滚
//
// 服务端原来只看一件事：主机上报的版本号有没有追上目标。而 usermode 下版本号**会**追上
// ——那个游离进程照样上报——于是操作台判 success 并变绿。真正的后果要等到几小时甚至
// 一次重启之后才出现：主机悄无声息地掉线，而任务表上写着「升级成功」。这正是助手已经
// 把 `degraded ... reason=service-not-running` 写进 result 文件、却没人读的那一段。
//
// 所以版本号追上之后还要再问一句：**助手自己是怎么说的？**

// utf8BOM：Windows PowerShell 5.1 的 `Set-Content -Encoding UTF8` 是「UTF-8 带 BOM」，
// 助手的 result 文件正是这么写出来的。不剥掉，前缀判定在任何一台真机上都不会命中。
const utf8BOM = "\uFEFF"

// windowsUpdateResultCommand 只取两条升级路径 result 文件的最后一行。
//
// 与 windowsUpdateEvidenceCommand 的区别是**代价**：那条是写失败判决前的一次性取证，
// 拉回目录清单和两个文件各 25 行；这条要在**每一台 Windows 主机升级成功时**都跑一次，
// 所以只读最后一行，且不碰日志文件。
//
// 同样必须走 -EncodedCommand：现网老 Agent 把 exec 命令交给 cmd.exe 时会弄坏 `\"`
// 转义（见 cmd/agent 的 useRawCmdLine），base64 正文里没有引号，是唯一能原样送达的形式。
//
// 两组文件名都要读：module 路径写 aiops-agent-update.result，legacy 救援路径写
// aiops-agent-legacy-update.result；救援跑过的主机，最终判决在后者里。
func windowsUpdateResultCommand() string {
	ps := `$ErrorActionPreference='SilentlyContinue'
foreach($d in @("$env:ProgramData\aiops-agent-update","$env:TEMP\aiops-agent-update")){
foreach($f in @('aiops-agent-update.result','aiops-agent-legacy-update.result')){$q=Join-Path $d $f
if(Test-Path -LiteralPath $q){$l=Get-Content -LiteralPath $q -Tail 1
if($l){Write-Output ($f+" >> "+$l)}}}}`
	return `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe ` +
		`-NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ` + psEncodedCommand(ps)
}

// degradedSwapVerdict 从 result 文件的输出里找出「这一轮」的 degraded 判决，找不到返回 ""。
//
// 只认版本号对得上的那一条。result 文件是**累积**的：上一次升级留下的 degraded 还躺在
// 里面，拿它给这一次定罪，等于让一台已经修好的主机永远判不了成功。版本号是这里唯一
// 可靠的一致性凭证——助手写 version= 用的就是探针从新二进制读回来的那个串，与主机
// 随后上报的 agent_version 同源。
func degradedSwapVerdict(out, ver string) string {
	ver = strings.TrimPrefix(strings.TrimSpace(ver), "v")
	if ver == "" {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), utf8BOM))
		// "aiops-agent-update.result >> degraded …"：去掉取证命令加的文件名前缀，
		// 剥完还要再剥一次 BOM——BOM 在文件正文的开头，也就是前缀的**后面**。
		if i := strings.Index(line, " >> "); i >= 0 {
			line = strings.TrimSpace(strings.TrimPrefix(line[i+len(" >> "):], utf8BOM))
		}
		if !strings.HasPrefix(line, "degraded ") {
			continue
		}
		if resultLineVersion(line) != ver {
			continue
		}
		return line
	}
	return ""
}

// resultLineVersion 取出 result 行里的 version= 字段（不带前导 v），没有则返回 ""。
func resultLineVersion(line string) string {
	at := strings.Index(line, "version=")
	if at < 0 {
		return ""
	}
	v := line[at+len("version="):]
	if sp := strings.IndexAny(v, " \t"); sp >= 0 {
		v = v[:sp]
	}
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// windowsSwapDegraded 问主机一句「助手自己怎么说」，返回那条 degraded 判决原文。
//
// 全部是尽力而为：非 Windows、主机已离线、exec 失败、文件没有——一律返回 ""，也就是
// 按原来的成功处理。这里绝不能把「问不出来」升级成「判失败」——那正是版本探针踩过的
// 坑（见 2026-08-18-windows-agent-update-version-probe.md）：不确定不等于失败。
func (s *Server) windowsSwapDegraded(h *Host, ver string) string {
	if s == nil || h == nil {
		return ""
	}
	if goos, _ := hostGOOSArch(h); goos != "windows" {
		return ""
	}
	// 离线主机答不了，exec 会白等一个 execPickupTimeout。而且离线本身已经是更重的信号，
	// 会由掉线告警负责，不需要在这里再等一遍。
	if cur := s.hostByID(h.ID); cur != nil && s.cfg != nil {
		offlineSec := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
		if cur.LastSeen <= 0 || time.Now().Unix()-cur.LastSeen > offlineSec {
			return ""
		}
	}
	out, _, err := s.execCommandOnHost(h, windowsUpdateResultCommand(), 30)
	if err != nil {
		return ""
	}
	return degradedSwapVerdict(sanitizePowerShellOutput(out), ver)
}

// markHostUpdateDegraded 写下「二进制换上去了，但 Windows 服务没起来」这条判决。
//
// 记成 failed 而不是新造一个状态，有三个理由：
//  1. 它**就是**一次没做成的升级——那台机器会在下一次重启后彻底掉线；
//  2. 操作台上「回滚失败主机」按钮筛的正是 status==="failed"，而回滚恰好是这里唯一
//     正确的补救；红色计数、告警抬头也都是现成的；
//  3. 不会因此陷入重试循环：自动扫描按「版本号还落后」挑主机，而这台已经不落后了。
//
// 与失败计数的关系也正因为第 3 条：它一次就到头，不会累成连续同因失败，所以
// noteAgentUpdateFailure 里那条「连续 N 次」的抬头永远不会为它触发。它必须自己抬头。
func (s *Server) markHostUpdateDegraded(job *agentUpdateJob, hostID, ver, verdict string) {
	if s == nil || s.agentUpdates == nil || job == nil {
		return
	}
	msg := "binary swapped to " + ver + " but the Windows service is NOT running — " +
		"this host stays up only until the process or its logon session ends, and nothing " +
		"restarts it on reboot. Helper verdict: " + verdict
	hostLabel := hostID
	fromVer := ""
	s.agentUpdates.mu.Lock()
	if j := s.agentUpdates.jobs[job.ID]; j != nil {
		for _, hr := range j.Hosts {
			if hr != nil && hr.HostID == hostID && (hr.Status == "pending_verify" || hr.Status == "success") {
				hr.Status = "failed"
				hr.Message = truncateRun(msg, 1200)
				hr.Updated = time.Now().Unix()
				fromVer = hr.FromVer
				break
			}
		}
	}
	s.agentUpdates.mu.Unlock()

	if s.store != nil {
		if h, ok := s.store.GetHost(hostID); ok && h != nil && h.Hostname != "" {
			hostLabel = h.Hostname
		}
	}
	text := "主机「" + hostLabel + "」升级后二进制已是 " + ver +
		"，但 Windows 服务没有运行：当前进程或登录会话一结束就会掉线，重启后不会自动拉起。" +
		"请到该主机上确认 aiops-agent 服务，或在任务里对它执行回滚。助手判决原文：" + verdict
	if s.store != nil {
		s.store.AddLog(LogEntry{
			Kind: KindSystem, Level: "warning", Actor: Tz("agent_update.actor"),
			Host: hostLabel, Message: text,
		})
	}
	if s.notifier != nil {
		s.notifier.enqueuePush(s.cfg.Get(), Alert{
			HostID:    hostID,
			Hostname:  hostLabel,
			Level:     "warning",
			Type:      "agent_update",
			Scope:     "service_not_running",
			Message:   text,
			Timestamp: time.Now().Unix(),
		}, true)
	}
	// 升不上去是平台自己没做成的事，进自身故障归口（开事件 / AI 诊断 / 可转工单）。
	s.reportPlatformFault("agent_update", "service_not_running", "warning", hostID, text, verdict)
	// 失败计数照记：单看一台主机它到不了阈值，但同一批次里多台一起 degraded 时，
	// 这份记录是唯一能把它们串起来的东西。
	s.noteAgentUpdateFailure(hostID, fromVer, msg)
}
