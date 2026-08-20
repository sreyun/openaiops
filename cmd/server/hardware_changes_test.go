package main

import (
	"strings"
	"testing"

	"aiops-monitor/shared"
)

func snapWith(disks []shared.RedfishStorage, dimms []shared.MemoryDIMM, fw []shared.FirmwareInfo) shared.HardwareSnapshot {
	return shared.HardwareSnapshot{
		TargetName: "idrac-01",
		Storage:    disks,
		Memory:     shared.RedfishMemory{DIMMs: dimms},
		Firmware:   fw,
	}
}

// 换盘：槽位不变、序列号变 —— 必须识别成 replaced 一条，
// 而不是 removed+added 两条（那样读起来完全看不出是同一个位置换了件）。
func TestDiffHardwareDetectsDiskReplacement(t *testing.T) {
	prev := snapWith([]shared.RedfishStorage{
		{Name: "Disk 0", Location: "Bay 0", SerialNumber: "OLD123", Model: "ST4000", CapacityGB: 3726},
	}, nil, nil)
	cur := snapWith([]shared.RedfishStorage{
		{Name: "Disk 0", Location: "Bay 0", SerialNumber: "NEW456", Model: "ST4000", CapacityGB: 3726},
	}, nil, nil)

	got := diffHardware(prev, cur)
	if len(got) != 1 {
		t.Fatalf("变更数 = %d, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Action != "replaced" || c.Kind != "disk" || c.Component != "Bay 0" {
		t.Errorf("变更 = %+v, want disk/Bay 0/replaced", c)
	}
	if !contains(c.Old, "OLD123") || !contains(c.New, "NEW456") {
		t.Errorf("新旧值没记录序列号: old=%q new=%q", c.Old, c.New)
	}
}

func TestDiffHardwareAddRemove(t *testing.T) {
	prev := snapWith([]shared.RedfishStorage{
		{Name: "Disk 0", Location: "Bay 0", SerialNumber: "A"},
		{Name: "Disk 1", Location: "Bay 1", SerialNumber: "B"},
	}, nil, nil)
	cur := snapWith([]shared.RedfishStorage{
		{Name: "Disk 0", Location: "Bay 0", SerialNumber: "A"},
		{Name: "Disk 2", Location: "Bay 2", SerialNumber: "C"}, // 新插
	}, nil, nil)

	got := diffHardware(prev, cur)
	if len(got) != 2 {
		t.Fatalf("变更数 = %d, want 2（Bay1 拔掉 + Bay2 新插）: %+v", len(got), got)
	}
	actions := map[string]string{}
	for _, c := range got {
		actions[c.Component] = c.Action
	}
	if actions["Bay 1"] != "removed" || actions["Bay 2"] != "added" {
		t.Errorf("动作判定错误: %+v", actions)
	}
	// 没动过的 Bay 0 绝不能产生记录
	if _, ok := actions["Bay 0"]; ok {
		t.Error("未变化的部件不应产生变更记录")
	}
}

// 稳定的机器每 30~60s 采一次，必须**一条都不写**，否则表会被重复数据淹没。
func TestDiffHardwareStableProducesNothing(t *testing.T) {
	s := snapWith(
		[]shared.RedfishStorage{{Name: "Disk 0", Location: "Bay 0", SerialNumber: "A", Model: "ST4000"}},
		[]shared.MemoryDIMM{{Name: "DIMM A1", Slot: "A1", SerialNumber: "M1", CapacityGB: 32}},
		[]shared.FirmwareInfo{{Name: "BIOS", Version: "2.19.1"}},
	)
	if got := diffHardware(s, s); len(got) != 0 {
		t.Errorf("无变化时产生了 %d 条记录: %+v", len(got), got)
	}
}

func TestDiffHardwareFirmwareUpgrade(t *testing.T) {
	prev := snapWith(nil, nil, []shared.FirmwareInfo{{Name: "BIOS", Version: "2.14.0"}})
	cur := snapWith(nil, nil, []shared.FirmwareInfo{{Name: "BIOS", Version: "2.19.1"}})
	got := diffHardware(prev, cur)
	if len(got) != 1 || got[0].Action != "changed" || got[0].Kind != "firmware" {
		t.Fatalf("固件升级未被记录为 changed: %+v", got)
	}
	if got[0].Old != "2.14.0" || got[0].New != "2.19.1" {
		t.Errorf("固件新旧版本错误: %+v", got[0])
	}
}

// 没有稳定槽位标识的部件不能瞎记：否则每轮名字一变就是一条假"换件"。
func TestDiffHardwareIgnoresUnidentifiableParts(t *testing.T) {
	prev := snapWith([]shared.RedfishStorage{{Name: "", Location: "", SerialNumber: "X"}}, nil, nil)
	cur := snapWith([]shared.RedfishStorage{{Name: "", Location: "", SerialNumber: "Y"}}, nil, nil)
	if got := diffHardware(prev, cur); len(got) != 0 {
		t.Errorf("无法定位的部件不应产生变更: %+v", got)
	}
}

// 内存换条同样按槽位判定
func TestDiffHardwareDIMMReplacement(t *testing.T) {
	prev := snapWith(nil, []shared.MemoryDIMM{{Slot: "A1", SerialNumber: "M-OLD", CapacityGB: 32}}, nil)
	cur := snapWith(nil, []shared.MemoryDIMM{{Slot: "A1", SerialNumber: "M-NEW", CapacityGB: 32}}, nil)
	got := diffHardware(prev, cur)
	if len(got) != 1 || got[0].Kind != "dimm" || got[0].Action != "replaced" || got[0].Component != "A1" {
		t.Fatalf("内存换条未识别: %+v", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
