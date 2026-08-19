package main

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestParseWinDiskKVBlocks(t *testing.T) {
	raw := `
Caption=C:
FreeSpace=1000000000
Size=2000000000
UsedPercent=50

Caption=D:
FreeSpace=500
Size=1000
`
	rows := parseWinDiskKVBlocks(raw)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].ID != "C:" || rows[0].Size != 2000000000 || rows[0].Free != 1000000000 {
		t.Fatalf("row0: %+v", rows[0])
	}
	if rows[1].ID != "D:" || rows[1].Size != 1000 {
		t.Fatalf("row1: %+v", rows[1])
	}
}

func TestParseWinDiskKVBlocksDeviceID(t *testing.T) {
	raw := "DeviceID=E:\nFreeSpace=10\nSize=20\n"
	rows := parseWinDiskKVBlocks(raw)
	if len(rows) != 1 || rows[0].ID != "E:" || rows[0].Size != 20 {
		t.Fatalf("got %+v", rows)
	}
}

func TestWinLooksEmptyOrFailed(t *testing.T) {
	if !winLooksEmptyOrFailed("") {
		t.Fatal("empty should fail")
	}
	if !winLooksEmptyOrFailed(`'wmic' 不是内部或外部命令，也不是可运行的程序或批处理文件。`) {
		t.Fatal("chinese wmic missing should fail")
	}
	if !winLooksEmptyOrFailed("wmic is not recognized as an internal or external command") {
		t.Fatal("english wmic missing should fail")
	}
	if winLooksEmptyOrFailed("Caption=C:\nFreeSpace=1\nSize=2\n") {
		t.Fatal("valid disk output should pass")
	}
}

func TestFormatWinDiskUsageHuman(t *testing.T) {
	s := formatWinDiskUsageHuman([]winDiskRow{{ID: "C:", Free: 25, Size: 100}})
	if !strings.Contains(s, "Caption=C:") || !strings.Contains(s, "UsedPercent=75.0") {
		t.Fatalf("bad format: %q", s)
	}
}

func TestWinCIMDiskUsageLive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	out, exit := winCIMDiskUsageText(context.Background())
	if exit != 0 {
		t.Fatalf("disk_usage exit=%d out=%s", exit, out)
	}
	if !strings.Contains(string(out), "Caption=") && !strings.Contains(string(out), "DeviceID=") {
		t.Fatalf("expected disk captions, got: %s", out)
	}
	rows := winCollectDiskRows()
	if len(rows) == 0 {
		t.Fatal("winCollectDiskRows returned no disks")
	}
}
