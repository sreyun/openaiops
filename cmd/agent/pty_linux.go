//go:build linux

package main

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// Linux-specific pty open: unlock the slave (TIOCSPTLCK=0) and read its number
// (TIOCGPTN) to form /dev/pts/N.
const (
	tiocSPTLCK  = 0x40045431 // TIOCSPTLCK
	tiocGPTN    = 0x80045430 // TIOCGPTN
	ptyWinszReq = 0x5414     // TIOCSWINSZ
)

func ptyOpen() (*os.File, string, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, "", err
	}
	var unlock int32 = 0
	if e := ioctl(m.Fd(), tiocSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		m.Close()
		return nil, "", e
	}
	var n uint32
	if e := ioctl(m.Fd(), tiocGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		m.Close()
		return nil, "", e
	}
	return m, "/dev/pts/" + strconv.Itoa(int(n)), nil
}
