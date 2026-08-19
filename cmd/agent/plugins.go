package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// pluginOutput is the JSON contract every plugin prints to stdout. All fields
// are optional:
//   - base:    base system metrics (used only when the native collector is
//     unsupported, e.g. on Windows/macOS via psutil)
//   - metrics: custom named gauges (mysql.connections, nginx.rps, ...)
//   - events:  discrete findings from the Python/AI/automation layer
type pluginOutput struct {
	Base    *shared.Metrics    `json:"base"`
	Metrics map[string]float64 `json:"metrics"`
	Events  []shared.Event     `json:"events"`
}

// pluginResult is the merged output of all plugins in one run.
type pluginResult struct {
	base   *shared.Metrics
	custom map[string]float64
	events []shared.Event
}

// PluginRunner discovers and executes plugins from a directory. Plugins run as
// isolated subprocesses so a crashing / hanging plugin can never take down the
// agent core — it is logged and skipped.
//
// Optimization: plugin discovery is done once at startup (cached). Python
// plugin execution uses deferred start — first RunAll starts immediately,
// but subsequent runs are spaced by the configured interval, preventing
// rapid-fire subprocess creation if the report interval is shorter than
// the plugin interval.
type PluginRunner struct {
	dir       string
	python    string
	timeout   time.Duration
	filesOnce sync.Once
	files     []string // cached plugin file list
}

func NewPluginRunner(dir, python string, timeout time.Duration) *PluginRunner {
	return &PluginRunner{dir: dir, python: python, timeout: timeout}
}

// discover returns the plugin files to run, skipping the SDK helper, dotfiles
// and underscore-prefixed files. Cached after first call — plugin list doesn't
// change while the agent is running.
// v5.4.1: only allow known safe extensions (.py, .sh); reject extensionless
// files and potential binary/native executables (.exe, .bin, etc.).
func (p *PluginRunner) discover() []string {
	p.filesOnce.Do(func() {
		entries, err := os.ReadDir(p.dir)
		if err != nil {
			return
		}
		var out []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			if name == "plugin_sdk.py" || name == "requirements.txt" {
				continue
			}
			// v5.4.1: whitelist approach — only allow known safe extensions
			ext := strings.ToLower(filepath.Ext(name))
			if ext == "" {
				// Extensionless files are dangerous (could be native binaries)
				continue
			}
			switch ext {
			case ".py", ".sh":
				// allowed
			default:
				// reject: .json, .txt, .md, .yaml, .yml, .conf, .ini, .cfg, .log,
				//         .exe, .bin, .bat, .cmd, .ps1, .dll, .so, .dylib, etc.
				continue
			}
			out = append(out, filepath.Join(p.dir, name))
		}
		sort.Strings(out)
		p.files = out
	})
	return p.files
}

// RunAll executes every plugin concurrently and merges their outputs.
// Each plugin gets its own goroutine with a bounded timeout — a hung plugin
// can block its own goroutine but never the rest.
func (p *PluginRunner) RunAll(logf func(string, ...any)) pluginResult {
	files := p.discover()
	agg := pluginResult{custom: map[string]float64{}}
	if len(files) == 0 {
		return agg
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	// Cap concurrent plugin goroutines: spawning 50+ Python processes
	// simultaneously would spike CPU/memory. A semaphore bounds this.
	sem := make(chan struct{}, 4) // max 4 concurrent plugin subprocesses
	for _, f := range files {
		wg.Add(1)
		go func(file string) {
			// panic 兜底：pluginLoop 的 recover 只护自身栈，护不到这些子协程；一个插件
			// 输出触发的 panic(如损坏的 map)会打崩整个 agent。defer LIFO 保证 wg.Done/
			// 释放槽位仍先执行，wg.Wait() 不会因 panic 卡死。
			defer func() {
				if r := recover(); r != nil {
					logf("插件 %s panic 已恢复: %v", pluginName(file), r)
				}
			}()
			sem <- struct{}{}        // acquire slot
			defer func() { <-sem }() // release slot
			defer wg.Done()
			name := pluginName(file)
			out, err := p.runOne(file)
			if err != nil {
				logf("插件 %s 执行失败: %v", name, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if out.Base != nil {
				agg.base = out.Base
			}
			for k, v := range out.Metrics {
				agg.custom[k] = v
			}
			for _, ev := range out.Events {
				if ev.Source == "" {
					ev.Source = name
				}
				agg.events = append(agg.events, ev)
			}
		}(f)
	}
	wg.Wait()
	return agg
}

func (p *PluginRunner) runOne(file string) (pluginOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	var cmd *exec.Cmd
	if strings.HasSuffix(file, ".py") {
		cmd = exec.CommandContext(ctx, p.python, file)
	} else {
		cmd = exec.CommandContext(ctx, file)
	}
	stdout, err := cmd.Output()
	if err != nil {
		return pluginOutput{}, err
	}
	stdout = []byte(strings.TrimSpace(string(stdout)))
	if len(stdout) == 0 {
		return pluginOutput{}, nil
	}
	var out pluginOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		return pluginOutput{}, err
	}
	return out, nil
}

func pluginName(file string) string {
	base := filepath.Base(file)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
