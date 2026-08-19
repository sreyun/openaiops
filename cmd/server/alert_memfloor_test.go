package main

import (
	"testing"
	"time"

	"aiops-monitor/shared"
)

const gib = uint64(1) << 30

func memHost(total, used uint64) *Host {
	return &Host{
		ID: "h1", Hostname: "big-mem-01", IP: "10.0.0.1", LastSeen: time.Now().Unix(),
		Latest: &shared.Sample{Metrics: shared.Metrics{
			MemTotal: total, MemUsed: used,
			MemPercent: float64(used) / float64(total) * 100,
		}},
	}
}

func hasMemAlert(alerts []Alert) bool {
	for _, a := range alerts {
		if a.Type == "memory" {
			return true
		}
	}
	return false
}

// 同一个百分比在不同规格的机器上不是同一件事：512G 到 92% 还剩 40G，1G 的小机器到 92%
// 只剩 80M。附加条件就是用来把前者从值班电话里摘出去的。
func TestMemFreeFloorGatesMemoryAlert(t *testing.T) {
	th := Thresholds{MemWarn: 85, MemCrit: 95, OfflineAfter: time.Hour}

	big := memHost(512*gib, 472*gib) // 92.2%，剩余 40G
	if !hasMemAlert(Evaluate([]*Host{big}, th)) {
		t.Fatal("没配地板时必须照旧告警（不能改变现有行为）")
	}

	th.MemFreeFloorGB = 10
	if hasMemAlert(Evaluate([]*Host{big}, th)) {
		t.Error("剩余 40G ≥ 地板 10G，不该告警")
	}

	small := memHost(2*gib, 1900*gib/1000) // 92.8%，剩余约 150M
	if !hasMemAlert(Evaluate([]*Host{small}, th)) {
		t.Error("剩余不足 10G 的机器必须照常告警")
	}
}

func TestMemFreeAboveFloorEdgeCases(t *testing.T) {
	cases := []struct {
		name              string
		total, used       uint64
		floor             float64
		wantAboveTheFloor bool
	}{
		{"未启用", 512 * gib, 500 * gib, 0, false},
		{"负数当未启用", 512 * gib, 500 * gib, -5, false},
		{"刚好等于地板算充裕", 20 * gib, 10 * gib, 10, true},
		{"差一点点不算", 20 * gib, 10*gib + 1, 10, false},
		{"没采到内存总量时不吞告警", 0, 0, 10, false},
		{"used 超过 total 的脏数据不吞告警", 8 * gib, 9 * gib, 10, false},
	}
	for _, c := range cases {
		if got := memFreeAboveFloor(c.total, c.used, c.floor); got != c.wantAboveTheFloor {
			t.Errorf("%s: memFreeAboveFloor(%d,%d,%v) = %v, want %v",
				c.name, c.total, c.used, c.floor, got, c.wantAboveTheFloor)
		}
	}
}

// 配错单位（把 MB 当 GB 填）会把内存告警整条静音，且界面上看起来"已生效"——
// 这种状态最难排查，配置校验必须直接拒绝。
func TestValidateRejectsAbsurdMemFreeFloor(t *testing.T) {
	cfg := ServerConfig{Thresholds: ThresholdConfig{
		CPUWarn: 80, CPUCrit: 90, MemWarn: 85, MemCrit: 95, DiskWarn: 80, DiskCrit: 90,
		OfflineAfterSec: 60, MemFreeFloorGB: 65536,
	}}
	if err := cfg.Validate(); err == nil {
		t.Error("65536 GB 的地板应被拒绝")
	}
	cfg.Thresholds.MemFreeFloorGB = -1
	if err := cfg.Validate(); err == nil {
		t.Error("负数地板应被拒绝")
	}
	cfg.Thresholds.MemFreeFloorGB = 10
	if err := cfg.Validate(); err != nil {
		t.Errorf("合理取值被拒: %v", err)
	}
}

// 阈值要能存得住、读得回：漏了任一方向的映射，用户改完保存、刷新页面就变回 0。
func TestMemFreeFloorRoundTripsThroughConfig(t *testing.T) {
	in := Thresholds{MemWarn: 85, MemCrit: 95, MemFreeFloorGB: 12.5, OfflineAfter: time.Minute}
	back := thresholdConfigFromThresholds(in).toThresholds()
	if back.MemFreeFloorGB != 12.5 {
		t.Fatalf("往返丢失: %v", back.MemFreeFloorGB)
	}
}
