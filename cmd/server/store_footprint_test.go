package main

import (
	"testing"
	"unsafe"

	"aiops-monitor/shared"
)

// 内存态历史环的容量预算。
//
// 这不是"跑通就行"的测试：三层环的深度直接决定进程能装下多少台主机，而超了之后的
// 表现是 OOM 或长时间 GC 停顿——没有任何一条日志会说"是历史环占满了"。这里把这个
// 换算固化下来，改动默认值时能立刻看到对机群规模的影响。
func TestHistoryRingFootprint(t *testing.T) {
	const sampleSize = int(unsafe.Sizeof(shared.Sample{}))
	points := histRawMax + hist1mMax + hist5mMax
	perHostMB := float64(sampleSize*points) / 1024 / 1024
	t.Logf("shared.Sample=%d B，三层共 %d 点 → 每台主机 ≈ %.2f MB", sampleSize, points, perHostMB)
	for _, hosts := range []int{100, 500, 1000} {
		t.Logf("  %4d 台 ≈ %.2f GB（不含 raw 层的 Disks/Conns/GPUs 切片与 map 开销）",
			hosts, perHostMB*float64(hosts)/1024)
	}
	// 默认配置下每台主机不该超过 8 MB——超了说明有人给某一层加了深度却没算过账。
	if perHostMB > 8 {
		t.Fatalf("每台主机内存预算 %.2f MB 超出 8 MB：500 台会吃掉 %.1f GB", perHostMB, perHostMB*500/1024)
	}
}

// 环深度必须可按机群规模下调——500 台以上时这是唯一能救内存的旋钮。
func TestHistoryRingDepthsAreTunable(t *testing.T) {
	t.Setenv("AIOPS_HIST_5M_MAX", "2016")
	if got := envIntDefault("AIOPS_HIST_5M_MAX", hist5mMaxDefault); got != 2016 {
		t.Fatalf("AIOPS_HIST_5M_MAX 未生效: %d", got)
	}
	// 非法值必须回落默认，不能把环长设成 0 让历史直接消失。
	t.Setenv("AIOPS_HIST_5M_MAX", "0")
	if got := envIntDefault("AIOPS_HIST_5M_MAX", hist5mMaxDefault); got != hist5mMaxDefault {
		t.Fatalf("非法值应回落默认，得到 %d", got)
	}
	t.Setenv("AIOPS_HIST_5M_MAX", "not-a-number")
	if got := envIntDefault("AIOPS_HIST_5M_MAX", hist5mMaxDefault); got != hist5mMaxDefault {
		t.Fatalf("非数字应回落默认，得到 %d", got)
	}
}
