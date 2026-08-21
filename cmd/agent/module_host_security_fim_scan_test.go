package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fimTestOpts points the walker at a throwaway tree with its baseline stored in
// a separate state dir (so the baseline file is never itself under scan).
func fimTestOpts(t *testing.T, root string) fimOptions {
	t.Helper()
	state := t.TempDir()
	setFIMStateDir(filepath.Join(state, "agent_state.json"))
	t.Cleanup(func() { setFIMStateDir("") })
	return fimOptions{
		Scope: "full", Roots: []string{root},
		MaxFiles: 10000, MaxChanges: 100, Budget: 30 * time.Second,
		ContentDiff: true,
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func changeByPath(changes []hostSecFileChange) map[string]hostSecFileChange {
	m := map[string]hostSecFileChange{}
	for _, c := range changes {
		m[c.Path] = c
	}
	return m
}

func TestCollectFIMChangesBaselineThenDelta(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), "same\n")
	writeFile(t, filepath.Join(root, "gone.txt"), "bye\n")
	writeFile(t, filepath.Join(root, "sub", "edit.txt"), "v1\n")
	opts := fimTestOpts(t, root)

	changes, stats := collectFIMChanges(opts)
	if !stats.Baseline {
		t.Fatalf("first run must establish baseline: %+v", stats)
	}
	if len(changes) != 0 {
		t.Fatalf("baseline must not emit changes: %+v", changes)
	}
	if stats.Files < 3 {
		t.Fatalf("expected at least 3 files walked, got %d", stats.Files)
	}

	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "sub", "edit.txt"), "v2-longer\n")
	writeFile(t, filepath.Join(root, "sub", "new.txt"), "hello\n")

	changes, stats = collectFIMChanges(opts)
	if stats.Baseline {
		t.Fatal("second run must not re-baseline")
	}
	byPath := changeByPath(changes)
	norm := func(p ...string) string { return fimNormPath(filepath.Join(append([]string{root}, p...)...)) }

	if got := byPath[norm("gone.txt")]; got.Change != "removed" {
		t.Fatalf("delete not detected: %+v", got)
	}
	if got := byPath[norm("sub", "new.txt")]; got.Change != "added" {
		t.Fatalf("add not detected: %+v", got)
	}
	edit := byPath[norm("sub", "edit.txt")]
	if edit.Change != "modified" {
		t.Fatalf("modify not detected: %+v", edit)
	}
	if edit.Diff != "" {
		t.Fatalf("non-whitelisted file must never carry content: %q", edit.Diff)
	}
	if edit.NewSHA != "" {
		t.Fatalf("non-whitelisted file must not be hashed: %+v", edit)
	}
	if _, ok := byPath[norm("keep.txt")]; ok {
		t.Fatal("unchanged file should not be reported")
	}
	if stats.Added != 1 || stats.Removed != 1 || stats.Modified != 1 {
		t.Fatalf("stats mismatch: %+v", stats)
	}

	// A third run with no filesystem activity must be silent.
	changes, _ = collectFIMChanges(opts)
	if len(changes) != 0 {
		t.Fatalf("idempotent run should be quiet: %+v", changes)
	}
}

func TestCollectFIMChangesContentDiffOnlyForWhitelist(t *testing.T) {
	root := t.TempDir()
	audited := filepath.Join(root, "app.conf")
	plain := filepath.Join(root, "data.bin")
	writeFile(t, audited, "listen 80\n")
	writeFile(t, plain, "aaaa\n")

	opts := fimTestOpts(t, root)
	opts.ContentPaths = []string{fimNormPath(audited)}
	if _, stats := collectFIMChanges(opts); !stats.Baseline {
		t.Fatal("expected baseline")
	}

	writeFile(t, audited, "listen 8080\n")
	writeFile(t, plain, "bbbb\n")
	// 非白名单文件走「仅元数据」检查（size/mtime/mode），而 "aaaa\n" 与 "bbbb\n"
	// **尺寸相同**，于是能否发现改动完全取决于 mtime 是否变了。文件系统的 mtime 粒度
	// 可以粗到 1~2 秒（overlayfs、部分 CI 卷），两次写落在同一个刻度里就得到完全相同的
	// 元数据 —— 这条用例因此会随机失败，而产品行为是对的：同尺寸同 mtime 的改动本来就
	// 不可能被仅看元数据的检查发现。显式把 mtime 推开，让断言只考察它真正想考察的东西。
	bumped := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(plain, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	changes, _ := collectFIMChanges(opts)
	byPath := changeByPath(changes)

	a := byPath[fimNormPath(audited)]
	if a.Reason != "content" || a.NewSHA == "" {
		t.Fatalf("whitelisted file should be content-hashed: %+v", a)
	}
	if !strings.Contains(a.Diff, "+listen 8080") {
		t.Fatalf("whitelisted file should carry a diff: %q", a.Diff)
	}
	p := byPath[fimNormPath(plain)]
	if p.Change != "modified" {
		t.Fatalf("plain file change missed: %+v", p)
	}
	if p.Diff != "" || p.NewSHA != "" {
		t.Fatalf("plain file must stay metadata-only: %+v", p)
	}
}

func TestCollectFIMChangesRespectsExcludes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x\n")
	writeFile(t, filepath.Join(root, "skipme", "a.txt"), "x\n")
	writeFile(t, filepath.Join(root, "watched.txt"), "x\n")

	opts := fimTestOpts(t, root)
	opts.Excludes = []string{fimNormPath(filepath.Join(root, "skipme"))}
	if _, stats := collectFIMChanges(opts); stats.Files != 1 {
		t.Fatalf("excludes not applied, walked %d files", stats.Files)
	}
}

func TestFimPartialWalkDoesNotFakeDeletes(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		writeFile(t, filepath.Join(root, n), "x\n")
	}
	opts := fimTestOpts(t, root)
	if _, stats := collectFIMChanges(opts); !stats.Baseline {
		t.Fatal("expected baseline")
	}
	// Cutting the walk short must not turn unvisited files into deletions.
	opts.MaxFiles = 2
	changes, stats := collectFIMChanges(opts)
	if !stats.LimitHit {
		t.Fatalf("expected limit hit: %+v", stats)
	}
	for _, c := range changes {
		if c.Change == "removed" {
			t.Fatalf("truncated walk must not report deletes: %+v", c)
		}
	}
}

func TestFimContentAllowedDenylistWinsOverWhitelist(t *testing.T) {
	pats := []string{"/etc/*", "/etc/ssl/private/*"}
	if !fimContentAllowed("/etc/hosts", pats) {
		t.Fatal("plain config should be auditable")
	}
	for _, p := range []string{
		"/etc/shadow", "/etc/ssl/private/site.key", "/root/.ssh/id_rsa",
		"/etc/app.pem", "/etc/secret-token", "/home/u/.env",
	} {
		if fimContentAllowed(p, pats) {
			t.Fatalf("secret must never be content-audited: %s", p)
		}
	}
}

func TestFimBaselineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.gz")
	in := map[string]fimEntry{
		"/etc/hosts": {Size: 12, Mtime: 100, Mode: "0644", SHA: "abc"},
		"/usr/bin/x": {Size: 99, Mtime: 200, Mode: "0755"},
	}
	if err := fimSaveBaseline(p, in); err != nil {
		t.Fatal(err)
	}
	out, ok := fimLoadBaseline(p)
	if !ok || len(out) != 2 {
		t.Fatalf("load failed ok=%v n=%d", ok, len(out))
	}
	if out["/etc/hosts"] != in["/etc/hosts"] || out["/usr/bin/x"] != in["/usr/bin/x"] {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if _, ok := fimLoadBaseline(filepath.Join(dir, "missing.gz")); ok {
		t.Fatal("missing baseline must report not-found")
	}
}

func TestFimChangeReasonPrefersContent(t *testing.T) {
	old := fimEntry{Size: 1, Mtime: 1, Mode: "0644", SHA: "a"}
	// Hashed files: mtime churn without content change is not a modification.
	if r := fimChangeReason(old, fimEntry{Size: 2, Mtime: 9, Mode: "0644", SHA: "a"}); r != "" {
		t.Fatalf("same hash should be quiet, got %q", r)
	}
	if r := fimChangeReason(old, fimEntry{Size: 1, Mtime: 1, Mode: "0644", SHA: "b"}); r != "content" {
		t.Fatalf("hash change should be content, got %q", r)
	}
	if r := fimChangeReason(old, fimEntry{Size: 1, Mtime: 1, Mode: "0777", SHA: "a"}); r != "mode" {
		t.Fatalf("mode change should be reported, got %q", r)
	}
	// Unhashed files fall back to metadata.
	bare := fimEntry{Size: 1, Mtime: 1, Mode: "0644"}
	if r := fimChangeReason(bare, fimEntry{Size: 5, Mtime: 1, Mode: "0644"}); r != "size" {
		t.Fatalf("size change, got %q", r)
	}
	if r := fimChangeReason(bare, fimEntry{Size: 1, Mtime: 7, Mode: "0644"}); r != "mtime" {
		t.Fatalf("mtime change, got %q", r)
	}
}

// 点名的根目录不受自动排除项影响。
//
// 这条是真踩出来的：`/tmp` 在很多机器上是独立的 tmpfs 挂载（容器镜像、这台构建机都是），
// 而 fimRemoteMountExcludes() 会把所有 tmpfs 挂载点排掉。于是 FIM 的四个测试在这类机器上
// 一起报 "walked 0 files"——测试本身没错，是被挂载类型挡掉了。产品侧的隐患更值得修：
// 运维在配置里点名一个目录（比如挂在 ramdisk 上的应用目录），扫描器一个文件都不走，
// 界面上只有一句"0 个文件"，没有任何线索指向"因为它是 tmpfs"。
func TestFimExplicitRootOverridesAutoExcludes(t *testing.T) {
	// /var/log 在 fimDefaultExcludes() 里；点名它就该扫。
	e := newFIMExcluder(nil, []string{"/var/log/myapp"})
	if e.skipPath("/var/log/myapp/app.log") {
		t.Error("点名 /var/log/myapp 之后，它下面的文件不该被默认排除项挡掉")
	}
	// 没点名的兄弟目录仍然排除。
	if !e.skipPath("/var/log/other/sys.log") {
		t.Error("/var/log 的其余部分仍应被默认排除项挡住")
	}
	// 运维自己写的排除项永远生效——那是他明确要排除的。
	e = newFIMExcluder([]string{"/var/log/myapp/noisy"}, []string{"/var/log/myapp"})
	if !e.skipPath("/var/log/myapp/noisy/x.log") {
		t.Error("显式 Excludes 必须压过点名根目录")
	}
	// root 是 "/" 等于整盘扫，不该把 /proc、/sys 一起拖进来。
	e = newFIMExcluder(nil, []string{"/"})
	if !e.skipPath("/proc/1/maps") {
		t.Error("整盘扫时默认排除项必须仍然生效")
	}
}

// —— 增量覆盖：这一组守的是"Windows 里新增的文件和目录识别不到"那类问题 ——
//
// 根因不在比对逻辑，而在覆盖面：一次扫描有文件数/时间上限，而 WalkDir 按字典序走，
// 于是每一轮都在同一个位置被截断，靠后的目录（`C:\Users\...\Desktop` 就在其中）
// 永远轮不到。下面分别钉住：目录本身要能报、断点要能续、没走到的地方不能瞎报。

func TestCollectFIMDetectsNewDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "seed.txt"), "x\n")
	opts := fimTestOpts(t, root)
	if _, stats := collectFIMChanges(opts); !stats.Baseline {
		t.Fatal("首轮应当建立基线")
	}

	// 用户在桌面上"新建文件夹"，里面还放了个文件。
	if err := os.MkdirAll(filepath.Join(root, "新建文件夹"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "新建文件夹", "note.txt"), "hi\n")

	changes, _ := collectFIMChanges(opts)
	byPath := changeByPath(changes)
	dir := fimNormPath(filepath.Join(root, "新建文件夹"))
	file := fimNormPath(filepath.Join(root, "新建文件夹", "note.txt"))
	if c, ok := byPath[dir]; !ok || c.Change != "added" {
		t.Fatalf("新建的目录没有被报出来：%+v", changes)
	}
	if c, ok := byPath[file]; !ok || c.Change != "added" {
		t.Fatalf("新目录里的文件没有被报出来：%+v", changes)
	}
}

func TestCollectFIMDetectsRemovedDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sub", "a.txt"), "x\n")
	opts := fimTestOpts(t, root)
	collectFIMChanges(opts)

	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	changes, _ := collectFIMChanges(opts)
	byPath := changeByPath(changes)
	if c, ok := byPath[fimNormPath(filepath.Join(root, "sub"))]; !ok || c.Change != "removed" {
		t.Fatalf("被删除的目录没有被报出来：%+v", changes)
	}
	if c, ok := byPath[fimNormPath(filepath.Join(root, "sub", "a.txt"))]; !ok || c.Change != "removed" {
		t.Fatalf("被删除目录里的文件没有被报出来：%+v", changes)
	}
}

// 截断之后必须从断点继续，而不是每一轮都重扫同一段前缀——那正是靠后的目录
// "永远扫不到"的原因。
func TestCollectFIMResumesAfterTruncation(t *testing.T) {
	root := t.TempDir()
	// 三个按字典序排开的目录，每个若干文件。
	for _, d := range []string{"a_dir", "b_dir", "c_dir"} {
		for i := 0; i < 4; i++ {
			writeFile(t, filepath.Join(root, d, string(rune('a'+i))+".txt"), "x\n")
		}
	}
	opts := fimTestOpts(t, root)
	opts.MaxFiles = 5 // 强制截断

	first, stats1 := collectFIMChanges(opts)
	if !stats1.Baseline {
		t.Fatal("首轮应当建立基线")
	}
	if len(first) != 0 {
		t.Fatalf("基线轮不该报变更：%+v", first)
	}
	if !stats1.LimitHit || stats1.ResumeFrom == "" {
		t.Fatalf("应当因为文件数上限被截断并留下续扫点：%+v", stats1)
	}

	seen := map[string]bool{}
	resume := stats1.ResumeFrom
	// 再扫几轮，覆盖面应当推进到最后一个目录。
	for i := 0; i < 6; i++ {
		_, st := collectFIMChanges(opts)
		for _, r := range st.Roots {
			_ = r
		}
		if st.ResumeFrom != "" {
			if st.ResumeFrom == resume {
				t.Fatalf("续扫点没有推进，还停在 %q", resume)
			}
			resume = st.ResumeFrom
		}
		seen[st.ResumeFrom] = true
		if st.ResumeFrom == "" {
			break // 走完一圈
		}
	}
	// 最终基线里必须出现最后一个目录的文件，否则"靠后的目录永远扫不到"依旧成立。
	base, ok := fimLoadBaseline(fimBaselinePath())
	if !ok {
		t.Fatal("基线读不出来")
	}
	want := fimNormPath(filepath.Join(root, "c_dir", "d.txt"))
	if _, ok := base[want]; !ok {
		t.Fatalf("续扫没有覆盖到最后一个目录：基线里没有 %s", want)
	}
}

// 第一次走到某片区域时，那里的文件对基线来说都是"没见过"——但它们不是新增。
// 报出来会把真正的变更淹掉，所以必须先默默建基线。
func TestCollectFIMDoesNotReportFirstVisitAsAdded(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a_dir", "z_dir"} {
		for i := 0; i < 4; i++ {
			writeFile(t, filepath.Join(root, d, string(rune('a'+i))+".txt"), "x\n")
		}
	}
	opts := fimTestOpts(t, root)
	opts.MaxFiles = 5

	collectFIMChanges(opts) // 基线轮：只覆盖前半段
	changes, stats := collectFIMChanges(opts)
	for _, c := range changes {
		if c.Change == "added" && strings.Contains(c.Path, "z_dir") {
			t.Fatalf("第一次走到 z_dir 就报新增，是噪音：%+v（stats=%+v）", c, stats)
		}
	}
}

// 没走到的目录里，基线中的文件不能被报成删除——那是"看不见"，不是"没了"。
func TestCollectFIMDoesNotReportUnvisitedAsRemoved(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a_dir", "z_dir"} {
		for i := 0; i < 4; i++ {
			writeFile(t, filepath.Join(root, d, string(rune('a'+i))+".txt"), "x\n")
		}
	}
	opts := fimTestOpts(t, root)
	// 先完整建一次基线
	collectFIMChanges(opts)
	// 再把上限压到只够扫前半段
	opts.MaxFiles = 5
	changes, _ := collectFIMChanges(opts)
	for _, c := range changes {
		if c.Change == "removed" && strings.Contains(c.Path, "z_dir") {
			t.Fatalf("没走到的目录被误报成删除：%+v", c)
		}
	}
}

func TestFimSubtreeBeforeSkipsOnlyFinishedTrees(t *testing.T) {
	cursor := "/data/b_dir/c.txt"
	if !fimSubtreeBefore("/data/a_dir", cursor) {
		t.Error("排在游标之前的整棵子树应当跳过")
	}
	if fimSubtreeBefore("/data/b_dir", cursor) {
		t.Error("游标就在这棵子树里，不能跳")
	}
	if fimSubtreeBefore("/data/z_dir", cursor) {
		t.Error("排在游标之后的子树还没扫，不能跳")
	}
	if fimSubtreeBefore("/data", cursor) {
		t.Error("祖先目录不能跳")
	}
}

func TestFimRegionKnownWalksAncestors(t *testing.T) {
	known := map[string]bool{fimMatchKey("/home/u/Desktop"): true}
	if !fimRegionKnown("/home/u/Desktop/new.txt", known) {
		t.Error("已知目录里的新文件应当算新增")
	}
	if !fimRegionKnown("/home/u/Desktop/新建文件夹/a.txt", known) {
		t.Error("已知目录下新建子目录里的文件也应当算新增")
	}
	if fimRegionKnown("/var/log/never/walked.txt", known) {
		t.Error("从没走到过的区域不该算新增")
	}
}

func TestFimPriorityRootsAreRealDirectories(t *testing.T) {
	// 只要求"存在的才留下"这条成立；具体路径随平台不同。
	roots := fimExistingRoots(append(fimPriorityRoots(), t.TempDir(), "/definitely/not/here"))
	for _, r := range roots {
		fi, err := os.Stat(r)
		if err != nil || !fi.IsDir() {
			t.Fatalf("扫描根不存在或不是目录：%s", r)
		}
	}
	if len(roots) == 0 {
		t.Fatal("至少应当保留刚建出来的临时目录")
	}
}

// —— 多卷覆盖：这一组守的是"Windows 上只扫了 C 盘，D/E 盘完全没进来" ——

func TestFimGroupRootsByVolume(t *testing.T) {
	// 相互嵌套的根必须同组顺序走：并发会重复扫同一片，
	// 还会让"同一棵子树不重复走"的去重失效。
	got := fimGroupRootsByVolume([]string{"/etc", "/home", "/"})
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("嵌套的根应当归成一组：%+v", got)
	}
	if got[0][0] != "/etc" {
		t.Fatalf("组内顺序要保持（要害目录在前）：%+v", got[0])
	}

	// 互不相干的根各成一组，可以并发——Windows 上的 C:\ 与 D:\ 就是这种关系。
	got = fimGroupRootsByVolume([]string{"/data", "/srv", "/data/app"})
	if len(got) != 2 {
		t.Fatalf("互不相干的根应当分成两组：%+v", got)
	}
	if len(got[0]) != 2 || got[0][0] != "/data" || got[0][1] != "/data/app" {
		t.Fatalf("/data 与 /data/app 有包含关系，必须同组：%+v", got)
	}

	// 前缀相同但并不嵌套（/data 与 /database）不能被误并到一起。
	got = fimGroupRootsByVolume([]string{"/data", "/database"})
	if len(got) != 2 {
		t.Fatalf("/data 与 /database 只是前缀像，不该同组：%+v", got)
	}
}

// 每个卷每一轮都必须分到自己的份额，否则大盘一个人就把预算吃光，
// 其余的盘永远排不上队——那正是"只扫了 C 盘"的第二层原因。
func TestCollectFIMCoversEveryRootInOneScan(t *testing.T) {
	volA := t.TempDir()
	volB := t.TempDir()
	for i := 0; i < 30; i++ {
		writeFile(t, filepath.Join(volA, "big", string(rune('a'+i%26))+strconv.Itoa(i)+".txt"), "x\n")
	}
	writeFile(t, filepath.Join(volB, "only-here.txt"), "y\n")

	opts := fimTestOpts(t, volA)
	opts.Roots = []string{volA, volB}
	opts.MaxFiles = 10 // 逼出截断：第一个根远远走不完

	collectFIMChanges(opts) // 基线轮
	base, ok := fimLoadBaseline(fimBaselinePath())
	if !ok {
		t.Fatal("基线读不出来")
	}
	want := fimNormPath(filepath.Join(volB, "only-here.txt"))
	if _, ok := base[want]; !ok {
		t.Fatalf("第二个根在第一轮就应当被覆盖到（各卷有独立份额），基线里没有 %s", want)
	}
}

// 每卷的保底份额不能凌驾于操作员设的总上限之上，否则"调小上限"就不起作用了。
func TestFimVolumeQuotaRespectsSmallLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		writeFile(t, filepath.Join(root, "f"+strconv.Itoa(i)+".txt"), "x\n")
	}
	opts := fimTestOpts(t, root)
	opts.MaxFiles = 6
	_, stats := collectFIMChanges(opts)
	if stats.Files > 8 { // 6 + 少量目录条目的容差
		t.Fatalf("上限设成 6 却走了 %d 个文件——保底份额压过了操作员的设定", stats.Files)
	}
	if !stats.LimitHit {
		t.Fatalf("应当因为上限被截断：%+v", stats)
	}
}
