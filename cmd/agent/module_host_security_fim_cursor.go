package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 增量覆盖：让"整盘扫描"在有预算上限的前提下**最终覆盖到每一个角落**。
//
// 修的是现场那句"Windows 里用户新增的文件、目录没被识别到"。原因不在比对逻辑，而在
// 覆盖面：一次扫描有 15 万文件 / 90 秒的硬上限，而 Windows 的 C:\ 动辄上百万文件。
// filepath.WalkDir 按字典序遍历，于是每一轮都在**同一个位置**被截断——
// `C:\Program Files` 之后就没了，`C:\Users\...\Desktop` 永远轮不到。
// 用户在桌面上新建的文件和目录，扫描器从来没有走到过，自然"识别不到"。
//
// 三件事一起解决：
//
//  1. **断点续扫**：截断时把停在哪儿记下来，下一轮从那里接着走，走完一圈再从头开始。
//     跳过已扫区域是整棵子树跳过（目录名字典序比较），不是逐个文件空转。
//  2. **要害目录优先**：先扫用户数据与配置（Windows 的 Users/ProgramData/Program Files、
//     Linux 的 /etc /root /home /opt /usr/local、macOS 的 /Users /Applications /Library
//     的两个 LaunchAgents/Daemons），再扫整盘。第一轮就能覆盖到人真正会改动的地方。
//  3. **目录本身也进基线**：原来只记录普通文件，所以"新建了一个目录"这件事在比对里
//     根本不存在。现在目录也记，新增/删除目录都能报出来。
//
// 与之配套的判定规则见 fimRegionKnown：只有**之前完整枚举过**的目录里出现的新条目才算
// "新增"，否则那只是"第一次走到这片区域"，报出来全是噪音。

// fimScanStateFile 与基线同目录，记录每个根的续扫游标。
const fimScanStateFile = "fim_cursor.json"

// fimRootState 是一个扫描根的进度。
type fimRootState struct {
	// Next 是下一轮从哪个路径继续（归一化路径，空 = 从头开始）。
	Next string `json:"next,omitempty"`
	// Cycles 是这个根被完整走完的次数，仅用于排障与 UI 上的覆盖度说明。
	Cycles int `json:"cycles,omitempty"`
}

type fimScanState struct {
	Roots map[string]fimRootState `json:"roots,omitempty"`
}

func fimScanStatePath() string { return filepath.Join(fimDataDir(), fimScanStateFile) }

func fimLoadScanState() fimScanState {
	var st fimScanState
	raw, err := os.ReadFile(fimScanStatePath())
	if err != nil {
		return fimScanState{Roots: map[string]fimRootState{}}
	}
	if json.Unmarshal(raw, &st) != nil || st.Roots == nil {
		return fimScanState{Roots: map[string]fimRootState{}}
	}
	return st
}

func fimSaveScanState(st fimScanState) {
	if err := os.MkdirAll(fimDataDir(), 0o750); err != nil {
		return
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := fimScanStatePath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, fimScanStatePath())
}

// fimPriorityRoots 是"人会动、且安全上最该看"的目录，排在整盘之前先扫。
//
// 整盘扫描在大机器上一轮走不完，谁先谁后就决定了用户能不能及时看到变更。
// 桌面上新建一个文件属于最高频的场景，所以用户目录必须排在最前面。
func fimPriorityRoots() []string {
	switch runtime.GOOS {
	case "windows":
		sd := os.Getenv("SystemDrive")
		if sd == "" {
			sd = "C:"
		}
		out := []string{sd + `\Users`}
		if pd := os.Getenv("ProgramData"); pd != "" {
			out = append(out, pd)
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			out = append(out, pf)
		}
		if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
			out = append(out, pf86)
		}
		if win := os.Getenv("WINDIR"); win != "" {
			// System32 是落后门最常见的位置之一，但整个 Windows 目录太大，
			// 只把最要害的两个子目录提前。
			out = append(out, filepath.Join(win, "System32", "drivers", "etc"),
				filepath.Join(win, "System32", "Tasks"))
		}
		return out
	case "darwin":
		return []string{
			"/etc", "/Users", "/Applications",
			"/Library/LaunchAgents", "/Library/LaunchDaemons",
			"/usr/local/bin", "/opt",
		}
	default:
		return []string{
			"/etc", "/root", "/home", "/opt", "/srv",
			"/usr/local", "/usr/bin", "/usr/sbin", "/bin", "/sbin",
			"/var/www", "/var/spool/cron",
		}
	}
}

// fimExistingRoots 过滤掉不存在的路径，并去掉重复项（保持顺序）。
func fimExistingRoots(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range list {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key := fimMatchKey(fimNormPath(p))
		if seen[key] {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// fimScanRoots 组装本次要走的根：要害目录在前，整盘在后。
// 显式配置了 fim_roots 时完全听配置的，不再自作主张加东西。
func fimScanRoots(configured []string) []string {
	if len(configured) > 0 {
		return fimExistingRoots(configured)
	}
	return fimExistingRoots(append(fimPriorityRoots(), fimDefaultRoots()...))
}

// fimParentDir 取归一化路径的父目录（"/" 或 "C:" 到顶）。
func fimParentDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	if i == 0 {
		if p == "/" {
			return ""
		}
		return "/"
	}
	return p[:i]
}

// fimRegionKnown 回答"这个新条目所在的区域，我们以前完整枚举过吗"。
//
// 这是增量覆盖下判定"新增"的关键：第一次走到某片区域时，那里的每一个文件对基线来说
// 都是"没见过"，但它们并不是新增的——只是我们以前没走到。只有当它的某一级祖先目录
// **在上一份基线里存在**（意味着那次扫描完整枚举过它）时，才谈得上"新增"。
//
// 反过来，用户在已知目录里新建的目录/文件会一路向上撞到那个已知祖先，照常报出来。
func fimRegionKnown(p string, prevDirs map[string]bool) bool {
	for d := fimParentDir(p); d != ""; d = fimParentDir(d) {
		if prevDirs[fimMatchKey(d)] {
			return true
		}
	}
	return false
}

// fimResumeScope 记录本轮某个扫描根实际使用的续扫游标（开走时的 Next）。
// 删除判定必须拿它把"字典序上还没重走的前缀"排除掉，见 fimRegionVisited。
type fimResumeScope struct {
	Root   string
	Cursor string
}

// fimUnderRoot 判断路径是否落在该扫描根之下（含根自身）。
func fimUnderRoot(p, root string) bool {
	pk, rk := fimMatchKey(p), fimMatchKey(root)
	if pk == rk {
		return true
	}
	if rk == "/" {
		return strings.HasPrefix(pk, "/")
	}
	return strings.HasPrefix(pk, rk+"/")
}

// fimBeforeResume 判断 p 是否仍处在该根续扫游标之前——本轮被 fimSubtreeBefore /
// fimPathBefore 跳过、根本没重新枚举的那一段。
func fimBeforeResume(p, root, cursor string) bool {
	if cursor == "" || !fimUnderRoot(p, root) {
		return false
	}
	return fimMatchKey(p) < fimMatchKey(cursor)
}

// fimRegionVisited 回答"这一条基线记录所在的位置，本轮真的走到了吗"。
//
// 删除判定必须以此为前提。直接看"父目录在不在 visitedDirs"是不够的：目录被整个删掉时，
// 父目录本身也不在了，里面的文件就永远报不出删除。所以沿祖先向上找，
// 找到任何一个**本轮完整枚举过**的目录就算走到了（那次枚举没列出这一支，说明确实没了）。
//
// blocked 是本轮读不进去的目录（权限不足等）：它们下面的东西"看不见"，不是"没了"，
// 沿途撞上就停。被截断的停点祖先同样不在 visited 里（调用方在扫描收尾时剔除），
// 所以"还没轮到扫"的区域不会被误判成删除。
//
// resumes 更关键：续扫时 WalkDir 仍会把游标的祖先目录标成 visited（否则进不去
// 游标所在的那棵子树），但游标之前的兄弟子树是整棵 SkipDir 掉的，里面的基线条目
// 不在 cur 里。若只看 visited，那一整段前缀会被当成"已枚举却不见了"——误报删除，
// 并从基线里抹掉；下一轮再走到时又会当成新增，安全基线在续扫循环里被反复摧毁。
// 字典序上仍排在本轮游标之前的路径，一律视为"本轮没走到"。
func fimRegionVisited(p string, visited, blocked map[string]bool, resumes []fimResumeScope) bool {
	for _, s := range resumes {
		if fimBeforeResume(p, s.Root, s.Cursor) {
			return false
		}
	}
	for d := fimParentDir(p); d != ""; d = fimParentDir(d) {
		k := fimMatchKey(d)
		if blocked[k] {
			return false
		}
		if visited[k] {
			return true
		}
	}
	return false
}

// fimMinVolumeQuota 是每个卷每轮至少要走的文件数。
//
// 多盘机器上必须给每个盘留份额：C 盘上百万文件，如果不设下限，按"总额/卷数"分下来
// D/E 盘可能只分到几千个，覆盖一圈遥遥无期。同时它也是"总额很小时别把某个盘饿死"的兜底。
const fimMinVolumeQuota = 20000

// fimGroupRootsByVolume 把扫描根分成可以**并发**走的若干组。
//
// 规则很简单：**互相嵌套的根必须同组**（顺序走），**互不相干的根各成一组**（并发走）。
//
//   - Windows：C:\Users、C:\ProgramData、C:\ 都在 C 盘这棵树上 → 一组；D:\ 自成一组。
//     于是多盘机器上每个盘每一轮都能分到份额——"只扫了 C 盘"的第二层原因就在这里。
//   - 类 Unix：默认根（/etc、/home…、/）全都挂在 / 下面 → 一组。它们相互嵌套，必须顺序
//     走，否则"同一棵子树不重复走"的去重会失效、同一片被扫两遍。
//   - 显式配置了互不相干的根（fim_roots=/data,/srv）：各成一组，并发走，纯赚。
//
// 判据用路径包含关系而不是盘符，所以三个平台一套逻辑，也不必去猜挂载点。
func fimGroupRootsByVolume(roots []string) [][]string {
	if len(roots) == 0 {
		return [][]string{{}}
	}
	// 并查集：把有包含关系的根并到一起。根的数量是个位数，O(n²) 完全够用。
	parent := make([]int, len(roots))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	keys := make([]string, len(roots))
	for i, r := range roots {
		keys[i] = fimMatchKey(fimNormPath(r))
	}
	nested := func(a, b string) bool {
		return a == b || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
	}
	for i := range roots {
		for j := i + 1; j < len(roots); j++ {
			if nested(keys[i], keys[j]) || nested(keys[j], keys[i]) {
				union(i, j)
			}
		}
	}
	order := []int{}
	groups := map[int][]string{}
	for i, r := range roots {
		g := find(i)
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], r)
	}
	out := make([][]string, 0, len(order))
	for _, g := range order {
		out = append(out, groups[g])
	}
	return out
}
