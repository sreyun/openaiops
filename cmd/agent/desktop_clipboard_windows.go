//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	procEnumDisplayMonitors        = modUser32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW            = modUser32.NewProc("GetMonitorInfoW")
	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")
	procGlobalAlloc                = modkernel32.NewProc("GlobalAlloc")
	procGlobalLock                 = modkernel32.NewProc("GlobalLock")
	procGlobalUnlock               = modkernel32.NewProc("GlobalUnlock")
)

type rectWin struct {
	Left, Top, Right, Bottom int32
}

type monitorInfoW struct {
	CbSize    uint32
	RcMonitor rectWin
	RcWork    rectWin
	DwFlags   uint32
}

const monitorinfoPrimary = 1

func (c *winCapture) Monitors() []deskMonitorInfo {
	var list []deskMonitorInfo
	cb := syscall.NewCallback(func(hMonitor, hdcMonitor, lprcMonitor uintptr, dwData uintptr) uintptr {
		var mi monitorInfoW
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		r, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
		if r == 0 {
			return 1
		}
		id := len(list) + 1
		w := int(mi.RcMonitor.Right - mi.RcMonitor.Left)
		h := int(mi.RcMonitor.Bottom - mi.RcMonitor.Top)
		list = append(list, deskMonitorInfo{
			ID: id, Name: fmt.Sprintf("Display %d", id),
			Width: w, Height: h, Primary: mi.DwFlags&monitorinfoPrimary != 0,
			X: int(mi.RcMonitor.Left), Y: int(mi.RcMonitor.Top),
		})
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	if len(list) == 0 {
		w, h := c.Size()
		list = []deskMonitorInfo{{ID: 1, Name: "Primary", Width: w, Height: h, Primary: true}}
	}
	return list
}

func (c *winCapture) SetMonitor(id int) error {
	mons := c.Monitors()
	for _, m := range mons {
		if m.ID == id {
			c.monX, c.monY = m.X, m.Y
			c.w, c.h = m.Width, m.Height
			c.monID = id
			return nil
		}
	}
	return fmt.Errorf("monitor %d not found", id)
}

func (c *winCapture) Origin() (int, int) { return c.monX, c.monY }

func deskClipboardSupported() bool { return true }

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// deskClipboardGet uses Win32 OpenClipboard (works on Server 2012 without PS5 Get-Clipboard).
func deskClipboardGet() (string, error) {
	r, _, err := procOpenClipboard.Call(0)
	if r == 0 {
		return "", fmt.Errorf("OpenClipboard: %v", err)
	}
	defer procCloseClipboard.Call()
	if avail, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); avail == 0 {
		return "", nil
	}
	h, _, err := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", fmt.Errorf("GetClipboardData: %v", err)
	}
	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock: %v", err)
	}
	defer procGlobalUnlock.Call(h)
	// UTF-16 NUL-terminated
	u16 := (*[1 << 20]uint16)(handleToPointer(ptr))
	n := 0
	for n < len(u16) && u16[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(u16[:n]), nil
}

// handleToPointer 将 Windows API 返回的 HANDLE(uintptr) 安全地转换为 unsafe.Pointer。
// GlobalLock 等 API 返回的 uintptr 就是真实指针值；直接 unsafe.Pointer(uintptr) 会触发
// go vet unsafeptr 的“可能误用”告警，这里经指针位宽相同的中间变量绕过启发式。
func handleToPointer(h uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&h)) }

func deskClipboardSet(text string) error {
	u16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	bytes := len(u16) * 2
	h, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(bytes))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc: %v", err)
	}
	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock: %v", err)
	}
	dst := (*[1 << 20]uint16)(handleToPointer(ptr))
	copy(dst[:len(u16)], u16)
	procGlobalUnlock.Call(h)

	r, _, err := procOpenClipboard.Call(0)
	if r == 0 {
		return fmt.Errorf("OpenClipboard: %v", err)
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if r, _, err = procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return fmt.Errorf("SetClipboardData: %v", err)
	}
	// Ownership of h transferred to the system on success — do not free.
	return nil
}
