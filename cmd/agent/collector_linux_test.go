//go:build linux

package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestParseMemInfoScannerFallsBackWithoutMemAvailable(t *testing.T) {
	src := `MemTotal:        8000000 kB
MemFree:         1000000 kB
Buffers:          500000 kB
Cached:          2000000 kB
SwapTotal:       1000000 kB
SwapFree:         900000 kB
`
	mi := parseMemInfoScanner(bufio.NewScanner(strings.NewReader(src)))
	if mi.memTotal != 8000000*1024 {
		t.Fatalf("memTotal=%d", mi.memTotal)
	}
	wantAvail := uint64((1000000 + 500000 + 2000000) * 1024)
	if mi.memAvail != wantAvail {
		t.Fatalf("memAvail fallback=%d want %d", mi.memAvail, wantAvail)
	}
	usedPct := float64(mi.memTotal-mi.memAvail) / float64(mi.memTotal) * 100
	if usedPct > 60 {
		t.Fatalf("usage looked like ~100%% without MemAvailable: %.1f%%", usedPct)
	}
}

func TestParseMemInfoScannerPrefersMemAvailable(t *testing.T) {
	src := `MemTotal:        8000000 kB
MemFree:         1000000 kB
MemAvailable:    4500000 kB
Buffers:          500000 kB
Cached:          2000000 kB
`
	mi := parseMemInfoScanner(bufio.NewScanner(strings.NewReader(src)))
	if mi.memAvail != 4500000*1024 {
		t.Fatalf("memAvail=%d", mi.memAvail)
	}
}

func TestIncludeLinuxMount(t *testing.T) {
	cases := []struct {
		dev, mount, fs string
		want           bool
	}{
		{"/dev/sda1", "/", "ext4", true},
		{"/dev/sda1", "/boot", "ext4", false},
		{"overlay", "/", "overlay", true},
		{"overlay", "/var/lib/docker", "overlay", false},
		{"server:/export", "/mnt/nfs", "nfs4", true},
		{"tank/data", "/data", "zfs", true},
		{"tmpfs", "/run", "tmpfs", false},
		{"proc", "/proc", "proc", false},

		// 容器/Pod 的内部挂载不该出现在"主机的盘"里：一台跑几十个 Pod 的节点会因此多出
		// 几十上百条，而它们多半只是节点根盘的绑定挂载——同一块盘被数很多遍。
		{"/dev/sda1", "/var/lib/kubelet/pods/8f3c/volumes/kubernetes.io~empty-dir/tmp", "ext4", false},
		{"/dev/sda1", "/var/lib/docker/containers/abc123/mounts/shm", "ext4", false},
		{"nfs:/pv", "/var/lib/kubelet/pods/8f3c/volumes/kubernetes.io~nfs/pv-1", "nfs4", false},
		{"/dev/sda1", "/run/k3s/containerd/io.containerd.grpc.v1.cri/sandboxes/x/shm", "ext4", false},
		{"/dev/sda1", "/var/lib/containerd/tmpmounts/x", "ext4", false},
		// 但运行时数据目录本身要留着：给它挂独立盘是常见做法，写满了整个节点的容器全挂。
		{"/dev/sdb1", "/var/lib/docker", "ext4", true},
		{"/dev/sdb1", "/var/lib/kubelet", "xfs", true},
		// 名字只是前缀相同、并不在目录内部的，不能误伤。
		{"/dev/sdc1", "/var/lib/dockerdata", "ext4", true},
	}
	for _, c := range cases {
		got := includeLinuxMount(c.dev, c.mount, c.fs)
		if got != c.want {
			t.Errorf("include(%s,%s,%s)=%v want %v", c.dev, c.mount, c.fs, got, c.want)
		}
	}
}

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/my\040disk`); got != "/mnt/my disk" {
		t.Fatalf("got %q", got)
	}
}

func TestIsLinuxDiskPartition(t *testing.T) {
	parts := []string{"sda1", "vda2", "nvme0n1p3", "mmcblk0p1", "xvda1"}
	for _, d := range parts {
		if !isLinuxDiskPartition(d) {
			t.Errorf("%s should be partition", d)
		}
	}
	wholes := []string{"sda", "vda", "nvme0n1", "mmcblk0", "xvda"}
	for _, d := range wholes {
		if isLinuxDiskPartition(d) {
			t.Errorf("%s should be whole disk", d)
		}
	}
}
