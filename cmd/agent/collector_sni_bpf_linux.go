//go:build linux && !386

package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

// attachBPF 尽力给 AF_PACKET fd 挂 cBPF 过滤器。失败不致命——退回 ETH_P_ALL 全收 + userspace 过滤。
func attachBPF(fd int, filter []sockFilter) error {
	prog := sockFprog{length: uint16(len(filter)), filter: &filter[0]}
	_, _, errno := syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd),
		uintptr(solSocket), uintptr(soAttachFilter),
		uintptr(unsafe.Pointer(&prog)), unsafe.Sizeof(prog), 0)
	if errno != 0 {
		return errno
	}
	// filter 的底层数组由内核在本次调用内拷贝走，但 prog 仍指向它——显式保活，
	// 避免 GC 在 syscall 返回前回收（Syscall6 不会为 uintptr 参数建立可达性）。
	runtime.KeepAlive(filter)
	return nil
}
