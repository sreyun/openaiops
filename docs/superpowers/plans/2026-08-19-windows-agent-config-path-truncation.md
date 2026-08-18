# 一个空格：升级成功，主机却永远离线

现场那台机器的服务 ImagePath 是这样的：

```
"C:\Program Files\AIOps Agent\aiops-agent.exe" --service --config "C:\Program"
```

`--config` 指向 `C:\Program`。**一次成功的换版把整台机器打成了哑巴。**

## 一、这条路径是怎么断的

升级助手（两条路径都有）用这一句把 Agent 拉回来：

```powershell
Start-Process -FilePath $Exe -ArgumentList @('--install-service','--config',$Cfg) ...
```

`Start-Process -ArgumentList` 把数组元素用**单个空格拼接，不加任何引号**。于是默认安装
路径在第一个空格处断开：

```
--install-service --config C:\Program Files\AIOps Agent\config.yaml
                          └── Agent 只收到这一段 ┘  └─ 两个游离的位置参数 ─┘
```

Agent 的 `--install-service` 分支拿到 `cfgPath = C:\Program`，`filepath.Abs` 之后**原样
写进服务 ImagePath**（`service_windows.go:108` 那一句反倒是加了引号的——它忠实地把一个
已经残缺的值括了起来）。

同一段脚本里 `wscript.exe` 的两处调用是**手工加了引号**的（`'"'+$vbs+'"'`），说明写的人
知道这个坑，只是没把它用到 `$Cfg` 上。

## 二、为什么表现是「服务正常但主机离线，重启无效」

ImagePath 被写坏之后，此后每一次启动：

1. `--config C:\Program` → 文件不存在；
2. `main.go` 的加载分支**不报错、不退出**，只写一条 WARN，然后带着默认的
   `localhost:8529` 继续跑；
3. 于是：服务状态 Running、进程活着、二进制是最新的、重启一百次都一样，而控制台上这台
   主机永远离线——它一直在向本机的 8529 上报。

而唯一的线索——那条 WARN——跟着 `cfgPath` 一起落进了 `C:\Windows\System32\agent.log`：
`startServiceFileLog` 用的是同一个 `cfgPath` 推出来的目录，`filepath.Abs("config.yaml")`
在 SCM 的工作目录下就是 System32。`config.example.yaml`、`agent_state.json` 也在那里。
**证据一直在写，只是写在了没人会去看的地方。**

这就是「升级不成功却把 Agent 弄离线」最恶劣的一种：升级其实**成功**了。

## 三、修复：写、读、判三侧各一道

### 3.1 写（根因）

四处 `Start-Process -ArgumentList` 全部给 `$Cfg` 加引号：

| 位置 | 用途 |
|---|---|
| `cmd/agent/module_agent_update_scripts.go` ×2 | 模块助手：`--install-service` 与用户态兜底 |
| `cmd/server/agent_update_script.go` ×2 | legacy 救援脚本：同上 |

两侧各一条测试守着（`TestWindowsHelperQuotesTheConfigPathInStartProcess` /
`TestLegacyHelperQuotesTheConfigPathInStartProcess`）：逐行扫生成的脚本，凡是
`-ArgumentList` 里出现 `'--config'` 的行，必须带 `('"'+$Cfg+'"')`。

### 3.2 读（救存量）

助手侧补了引号，但**已经被写坏 ImagePath 的主机不会自己好**：它们连不上服务端，收不到
任何修复。所以读这一侧也要能自愈：

- `agentConfigCandidates()`：配置查找顺序改为「先工作目录、再**可执行文件所在目录**」。
  服务无论被怎样注册（绝对 / 相对 / 根本没有 `--config`），都能找回装在自己旁边的配置。
- 显式 `--config` 指到不存在的文件时，退回二进制旁边那份，并留下 WARN 写明 given/used
  与修复命令。只在旁边确实有配置时才退——静默替换配置比读不到配置更危险。
- `agentLogBaseDir()`：一个字节都没读到配置时，运行日志锚到 exe 目录而不是工作目录，
  不再落进 System32。`configBaseDir` / `resolveConfigRelativePaths` 的语义**没有动**——
  身份文件的锚定规则改不得，那是「同一台机器在多个 host 之间横跳」那一轮的成果。

`resolveConfigRelativePaths` 早就为 `state_file` / `plugins_dir` 做过同一件事（那一次的
现场是「同一台机器在多个 host 之间反复横跳」）。配置文件自身一直没有，这次补上。

### 3.3 判（不再当哑巴）

`--service` 模式下，配置文件读不到且没有 `AIOPS_SERVER` / `--server` → **直接 Fatalf**，
不再带着 localhost 默认值继续跑。

一个「看起来健康的哑巴」比一个起不来的服务坏得多：后者 SCM 会记录、会按恢复策略重试、
人一眼就能看见。手工前台运行不受此限（第一次跑还没配置是正常的）。

这段检查**放在 `startServiceFileLog` 之后**——Windows 服务的 stderr 没有任何去处，先炸
再写日志等于什么都没说。

顺带：日志里的配置路径一律改成绝对路径，并带上 `cwd=`。原来打印的是
`path=config.yaml`，看不出它解析到了哪里，而「解析到了哪里」正是全部答案。

## 四、存量主机怎么修

这些主机是离线的，收不到升级。必须到机器上执行一次（管理员 PowerShell）：

```powershell
& "C:\Program Files\AIOps Agent\aiops-agent.exe" --install-service --config "C:\Program Files\AIOps Agent\config.yaml"
```

判断一台机器是不是中了这一枪，两条命令：

```powershell
(Get-CimInstance Win32_Service -Filter "Name='AiopsMonitorAgent'").PathName
Test-Path C:\Windows\System32\agent.log, C:\Windows\System32\config.example.yaml
```

`--config` 后面不是完整路径，或者 System32 下有 `agent.log`，就是它。

换上带本次修复的二进制之后，即便 ImagePath 仍是坏的，Agent 也会自己认回旁边的配置并
重新上线——但 ImagePath 该修还是要修，否则下一次 `--install-service` 又会把坏值传下去。

## 五、这次的通用教训

与 `2026-08-18-windows-agent-update-version-probe.md` 是同一族，但方向相反：

> 那一次是**把「无法判定」当成了「判定为失败」**，挡死了本该成功的升级；
> 这一次是**把「无法工作」当成了「可以继续」**，放行了一台已经报不上去的 Agent。

两条合起来是同一句话：**判据取不到值时，不要沉默地选一个默认答案。** 一端沉默地选了
「失败」，另一端沉默地选了「localhost」，代价都是几百台机器和几个人天。

第三条老规矩这次又被验证了一遍：**证据在抵达人眼之前不得被丢弃或改道。** 那条 WARN
一直在写，只是被 `cfgPath` 带去了 System32。
