package main

import (
	"os"
	"path/filepath"
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
