//go:build linux && 386

package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

// i386 没有独立的 setsockopt(2) 号：所有 socket 调用都经 socketcall(2) 多路复用，
// 参数打包成一个 uintptr 数组按指针传入。SYS_SETSOCKOPT 在 linux/386 上未定义，
// 直接照抄 64 位写法会编译失败（这正是 386 产物此前缺席的原因）。
const socketcallSetsockopt = 14

func attachBPF(fd int, filter []sockFilter) error {
	prog := sockFprog{length: uint16(len(filter)), filter: &filter[0]}
	args := [5]uintptr{
		uintptr(fd), uintptr(solSocket), uintptr(soAttachFilter),
		uintptr(unsafe.Pointer(&prog)), unsafe.Sizeof(prog),
	}
	_, _, errno := syscall.Syscall(syscall.SYS_SOCKETCALL,
		socketcallSetsockopt, uintptr(unsafe.Pointer(&args)), 0)
	if errno != 0 {
		return errno
	}
	runtime.KeepAlive(filter)
	runtime.KeepAlive(&prog)
	return nil
}
