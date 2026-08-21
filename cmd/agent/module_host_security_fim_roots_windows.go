//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Windows 的扫描根：**每一个本地盘**，不是只有 C:。
//
// 修的是"只扫了 C 盘，D/E 盘完全没进来"。原来是 A..Z 逐个 os.Stat：这既会漏（盘符被
// 挂成卷装载点时 Stat 不一定给出想要的结果），也会把**网络驱动器**一起拖进来——
// 映射的 Z: 盘走一遍又慢又不是这台机器的状态。
//
// 现在按 Win32 的正规做法来：GetLogicalDriveStringsW 拿到盘符列表，GetDriveTypeW 分类，
// 只留下本地盘（固定盘 + 可移动盘），网络盘 / 光驱 / 内存盘一律排除。
const (
	fimDriveRemovable = 2 // DRIVE_REMOVABLE：U 盘、SD 卡，属于"这台机器上的内容"
	fimDriveFixed     = 3 // DRIVE_FIXED：本地硬盘
)

// fimLocalDriveRoots 返回所有本地盘根（"C:\\"、"D:\\"…）。
func fimLocalDriveRoots() []string {
	buf := make([]uint16, 512)
	r, _, _ := procGetLogicalDrives.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	if r == 0 || int(r) > len(buf) {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < int(r); i++ {
		if buf[i] != 0 {
			continue
		}
		if i > start {
			root := buf[start : i+1] // 含结尾 NUL，Win32 调用要的就是这个
			dt, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(&root[0])))
			if dt == fimDriveFixed || dt == fimDriveRemovable {
				out = append(out, syscall.UTF16ToString(buf[start:i]))
			}
		}
		start = i + 1
	}
	return out
}
