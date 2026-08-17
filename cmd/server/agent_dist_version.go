package main

import (
	"bytes"
	"io"
	"os"
	"sync"
)

// dist/ 里的产物版本号从来没有被验证过：listAgentDistManifest 直接把服务端自己的
// appVersion 当成每个产物的版本报出去，agentDistResolveForHost 也只判断"文件在不在"。
//
// 这留下了一个**永动机式**的故障：只要 dist 里的 agent 二进制比服务端旧一格（镜像里
// 烘进去的旧产物、手工拷贝时漏掉一个平台、win2012 那条独立构建线没跟上），每一次升级
// 都会「成功」——助手下载、校验、换版、重启全都对——而主机上报的版本纹丝不动。服务端
// 看到的是 pending_verify 超时，于是 5 分钟后走 legacy 救援，再失败，再软重试……整支
// 机队从此无限循环，每台机器每半小时被停一次服务换一次二进制，而屏幕上写的是
// 「重启已排程但版本没跟上」——一个永远查不到头的说法，因为故障根本不在主机那一侧。
//
// 检测方式：用 `-X main.appVersion=<ver>` 注入的版本号会以原始字节留在二进制的只读数据段
// 里（`-s -w` 只剥符号表，不动字符串常量）。产物里找不到目标版本串，就说明它不是这一版
// 构建出来的。这是"缺席才报警"的判据：找到不代表一定是对的构建，但找不到几乎一定是错的。
type distVersionKey struct {
	path string
	size int64
	mod  int64
	ver  string
}

var (
	distVerMu    sync.Mutex
	distVerCache = map[distVersionKey]bool{}
)

// resetAgentDistVersionCache drops every memoised answer. Tests replace an
// artifact in place within the same clock tick, which the (size,mtime) key
// cannot see — see the caveat on agentDistCarriesVersion.
func resetAgentDistVersionCache() {
	distVerMu.Lock()
	distVerCache = map[distVersionKey]bool{}
	distVerMu.Unlock()
}

// agentDistCarriesVersion reports whether the binary at path contains ver as a
// literal string. Cached per (path,size,mtime,ver) — a fleet scan asks this for
// every host, and the file is tens of megabytes.
//
// 缓存键的边界条件说清楚：**同样大小 + 同一个 mtime 刻度内被替换**的文件，缓存看不见
// 变化。内核的时间戳粒度是毫秒级，而真实部署里换产物必然带来不同的大小或至少几毫秒的
// 时间差，所以现网够用；但测试里连写两次几乎必然落在同一刻度，需要 resetAgentDistVersionCache。
func agentDistCarriesVersion(path, ver string) bool {
	if path == "" || ver == "" {
		return true // nothing to check against; never block on ignorance
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return true
	}
	key := distVersionKey{path: path, size: fi.Size(), mod: fi.ModTime().UnixNano(), ver: ver}
	distVerMu.Lock()
	if hit, ok := distVerCache[key]; ok {
		distVerMu.Unlock()
		return hit
	}
	distVerMu.Unlock()

	found := fileContainsBytes(path, []byte(ver))
	distVerMu.Lock()
	if len(distVerCache) > 64 {
		distVerCache = map[distVersionKey]bool{}
	}
	distVerCache[key] = found
	distVerMu.Unlock()
	return found
}

// fileContainsBytes streams the file in windows that overlap by len(needle)-1,
// so a match straddling two reads is still found. Returns true on read errors:
// this feeds a gate, and "could not read it" must never be reported as "it is
// the wrong build".
func fileContainsBytes(path string, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()

	const chunk = 1 << 20
	overlap := len(needle) - 1
	buf := make([]byte, chunk+overlap)
	carried := 0
	for {
		n, err := f.Read(buf[carried:])
		if n > 0 {
			if bytes.Contains(buf[:carried+n], needle) {
				return true
			}
			if carried+n > overlap {
				copy(buf, buf[carried+n-overlap:carried+n])
				carried = overlap
			} else {
				carried += n
			}
		}
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
	}
}

// agentDistVersionMismatch returns the artifact name when the dist binary this
// host would receive does not carry ver, and "" when it does (or when there is
// nothing to check).
func (s *Server) agentDistVersionMismatch(h *Host, ver string) string {
	if s == nil || s.distDir == "" || !isComparableAgentVer(ver) {
		return ""
	}
	name, ok := s.agentDistResolveForHost(h)
	if !ok || name == "" {
		return ""
	}
	if agentDistCarriesVersion(s.distDir+string(os.PathSeparator)+name, ver) {
		return ""
	}
	return name
}
