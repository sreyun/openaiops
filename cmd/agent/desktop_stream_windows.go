//go:build windows

package main

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Windows GDI screen capture + SendInput / SetCursorPos keyboard/mouse.
//
// When run as the SYSTEM desktop worker (deskFollowSecureDesktop), the capture
// and input threads follow the *input desktop* via OpenInputDesktop +
// SetThreadDesktop. This is what lets the operator see and control the lock
// screen / logon screen (Winsta0\Winlogon secure desktop) and UAC prompts —
// impossible from an ordinary user-session process.

var (
	modUser32                  = syscall.NewLazyDLL("user32.dll")
	modGdi32                   = syscall.NewLazyDLL("gdi32.dll")
	procGetSystemMetrics       = modUser32.NewProc("GetSystemMetrics")
	procGetDC                  = modUser32.NewProc("GetDC")
	procGetWindowDC            = modUser32.NewProc("GetWindowDC")
	procGetDesktopWindow       = modUser32.NewProc("GetDesktopWindow")
	procReleaseDC              = modUser32.NewProc("ReleaseDC")
	procCreateCompatibleDC     = modGdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = modGdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection       = modGdi32.NewProc("CreateDIBSection")
	procSelectObject           = modGdi32.NewProc("SelectObject")
	procBitBlt                 = modGdi32.NewProc("BitBlt")
	procStretchBlt             = modGdi32.NewProc("StretchBlt")
	procDeleteObject           = modGdi32.NewProc("DeleteObject")
	procDeleteDC               = modGdi32.NewProc("DeleteDC")
	procGetDIBits              = modGdi32.NewProc("GetDIBits")
	procSendInput              = modUser32.NewProc("SendInput")
	procSetCursorPos           = modUser32.NewProc("SetCursorPos")
	procMouseEvent             = modUser32.NewProc("mouse_event")
	procKeybdEvent             = modUser32.NewProc("keybd_event")
	procMapVirtualKeyW         = modUser32.NewProc("MapVirtualKeyW")

	procOpenInputDesktop          = modUser32.NewProc("OpenInputDesktop")
	procOpenDesktopW              = modUser32.NewProc("OpenDesktopW")
	procSetThreadDesktop          = modUser32.NewProc("SetThreadDesktop")
	procGetThreadDesktop          = modUser32.NewProc("GetThreadDesktop")
	procCloseDesktop              = modUser32.NewProc("CloseDesktop")
	procGetUserObjectInformationW = modUser32.NewProc("GetUserObjectInformationW")
	procSetProcessDPIAware        = modUser32.NewProc("SetProcessDPIAware")
	procEnumWindows               = modUser32.NewProc("EnumWindows")
	procEnumDesktopWindows        = modUser32.NewProc("EnumDesktopWindows")
	procIsWindowVisible           = modUser32.NewProc("IsWindowVisible")
	procGetWindowRect             = modUser32.NewProc("GetWindowRect")
	procGetWindowTextW            = modUser32.NewProc("GetWindowTextW")
	procGetClassNameW             = modUser32.NewProc("GetClassNameW")
	procPrintWindow               = modUser32.NewProc("PrintWindow")
	procGetWindowThreadProcessId  = modUser32.NewProc("GetWindowThreadProcessId")

	modKernel32Desk                = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentProcessId        = modKernel32Desk.NewProc("GetCurrentProcessId")
	procProcessIdToSessionId       = modKernel32Desk.NewProc("ProcessIdToSessionId")
	procGetLastError               = modKernel32Desk.NewProc("GetLastError")
	procOpenProcess                = modKernel32Desk.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = modKernel32Desk.NewProc("QueryFullProcessImageNameW")
	procCloseHandleDesk            = modKernel32Desk.NewProc("CloseHandle")
	procGetCurrentThreadId         = modKernel32Desk.NewProc("GetCurrentThreadId")
)

const (
	uoiName = 2 // UOI_NAME

	desktopReadObjects     = 0x0001
	desktopCreateWindow    = 0x0002
	desktopCreateMenu      = 0x0004
	desktopHookControl     = 0x0008
	desktopJournalRecord   = 0x0010
	desktopJournalPlayback = 0x0020 // REQUIRED for SendInput to inject into this desktop
	desktopEnumerate       = 0x0040
	desktopWriteObjects    = 0x0080
	desktopSwitchDesktop   = 0x0100

	deskDesiredAccess = 0x10000000 // GENERIC_ALL
	// Capture needs READOBJECTS; SendInput needs JOURNALPLAYBACK. The previous
	// mask (0x181 = READ|WRITE|SWITCH) let OpenInputDesktop succeed without
	// JOURNALPLAYBACK, so JPEG capture worked while mouse/keyboard silently died
	// in the LocalSystem desktop worker.
	deskOperationalAccess = desktopReadObjects | desktopCreateWindow | desktopCreateMenu |
		desktopHookControl | desktopJournalPlayback | desktopEnumerate |
		desktopWriteObjects | desktopSwitchDesktop // 0x01EF
	maximumAllowedAccess = 0x02000000
)

// deskFollowSecureDesktop enables input-desktop following (worker mode only, so
// the widely-deployed foreground mode keeps its exact current behaviour).
// deskWorkerMode softens the initial capture probe (the first frame may land
// before we attach to the input desktop).
var (
	deskFollowSecureDesktop bool
	deskWorkerMode          bool
)

var deskDPIOnce sync.Once

func win32LastError() uint32 {
	r, _, _ := procGetLastError.Call()
	return uint32(r)
}

func currentSessionID() uint32 {
	pid, _, _ := procGetCurrentProcessId.Call()
	var sid uint32
	r, _, _ := procProcessIdToSessionId.Call(pid, uintptr(unsafe.Pointer(&sid)))
	if r == 0 {
		return 0xFFFFFFFF
	}
	return sid
}

func setDeskDPIAware() {
	deskDPIOnce.Do(func() {
		// Become DPI-aware so GetSystemMetrics / BitBlt see real (unscaled) pixels.
		_, _, _ = procSetProcessDPIAware.Call()
	})
}

func desktopNameOf(h uintptr) string {
	var buf [256]uint16
	var needed uint32
	r, _, _ := procGetUserObjectInformationW.Call(
		h, uintptr(uoiName),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2),
		uintptr(unsafe.Pointer(&needed)),
	)
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

func openInputDesktop() (uintptr, error) {
	var lastErr uint32
	// Prefer the operational mask (includes JOURNALPLAYBACK). Fall back to
	// GENERIC_ALL / MAXIMUM_ALLOWED on hardened ACLs that reject the explicit set.
	for _, access := range []uintptr{deskOperationalAccess, deskDesiredAccess, maximumAllowedAccess} {
		h, _, callErr := procOpenInputDesktop.Call(0, 0, access)
		if h != 0 {
			if deskWorkerMode {
				slog.Info("OpenInputDesktop ok",
					"access", fmt.Sprintf("0x%X", access),
					"desktop", desktopNameOf(h),
					"session", currentSessionID())
			}
			return h, nil
		}
		if errno, ok := callErr.(syscall.Errno); ok {
			lastErr = uint32(errno)
		} else {
			lastErr = win32LastError()
		}
	}
	// Lock / logoff often lands on Winlogon; OpenInputDesktop can fail briefly
	// while the secure desktop is switching — try it by name as a fallback.
	if h, err := openNamedDesktop("Winlogon"); err == nil && h != 0 {
		if deskWorkerMode {
			slog.Info("OpenDesktop(Winlogon) fallback ok", "session", currentSessionID())
		}
		return h, nil
	}
	return 0, fmt.Errorf("OpenInputDesktop failed: win32=%d", lastErr)
}

func openNamedDesktop(name string) (uintptr, error) {
	ptr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	var lastErr uint32
	for _, access := range []uintptr{deskOperationalAccess, deskDesiredAccess, maximumAllowedAccess} {
		h, _, callErr := procOpenDesktopW.Call(uintptr(unsafe.Pointer(ptr)), 0, 0, access)
		if h != 0 {
			return h, nil
		}
		if errno, ok := callErr.(syscall.Errno); ok {
			lastErr = uint32(errno)
		} else {
			lastErr = win32LastError()
		}
	}
	return 0, fmt.Errorf("OpenDesktop(%s) failed: win32=%d", name, lastErr)
}

func threadDesktopName() string {
	h, _, _ := procGetThreadDesktop.Call(uintptr(currentThreadID()))
	if h == 0 {
		return ""
	}
	// Do not CloseDesktop — GetThreadDesktop returns a borrow, not an owned handle.
	return desktopNameOf(h)
}

func currentThreadID() uint32 {
	r, _, _ := procGetCurrentThreadId.Call()
	return uint32(r)
}

// runDesktopWorker is the Windows entry point for the secure-desktop worker
// process (spawned by the service into the active console session). It runs ONLY
// the remote-desktop channel, with capture/input following the input desktop.
func runDesktopWorker(agent *Agent) error {
	deskWorkerMode = true
	deskFollowSecureDesktop = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancel()
	}()
	agent.RunDesktopOnly(ctx)
	return nil
}

const (
	smCXScreen             = 0
	smCYScreen             = 1
	smCXVirtualScreen      = 78
	smCYVirtualScreen      = 79
	smXVVirtualScreen      = 76
	smYVVirtualScreen      = 77
	srcCopy                = 0x00CC0020
	captureBLT             = 0x40000000 // include layered windows
	biRGB                  = 0
	dibRGBColors           = 0
	inputMouse             = 0
	inputKeyboard          = 1
	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfAbsolute    = 0x8000
	mouseeventfVirtualDesk = 0x4000
	keyeventfKeyUp         = 0x0002
	keyeventfExtendedKey   = 0x0001
	keyeventfUnicode       = 0x0004
	mapVKToVSC             = 0 // MAPVK_VK_TO_VSC
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type winCapture struct {
	w, h        int
	monX, monY  int
	monID       int
	curDesk     uintptr // currently attached input desktop (worker mode)
	curDeskName string
	locked      bool // this goroutine's OS thread is locked for SetThreadDesktop
}

// ensureInputDesktop attaches the calling (capture) thread to the desktop that
// currently receives input. It re-attaches whenever the input desktop switches
// (Default ↔ Winlogon ↔ Screen-saver), which is what makes lock/logon screens
// visible. No-op unless running as the SYSTEM worker.
func (c *winCapture) ensureInputDesktop() error {
	if !deskFollowSecureDesktop {
		return nil
	}
	if !c.locked {
		runtime.LockOSThread()
		c.locked = true
	}
	h, err := openInputDesktop()
	if err != nil {
		if deskWorkerMode {
			slog.Warn("无法打开输入桌面", "err", err)
		}
		return err
	}
	name := desktopNameOf(h)
	// Skip SetThreadDesktop only when we are already on the same desktop
	// (verify via GetThreadDesktop — name cache alone can lie after session switch).
	if c.curDesk != 0 && name == c.curDeskName && threadDesktopName() == name {
		_, _, _ = procCloseDesktop.Call(h)
		return nil
	}
	if r, _, _ := procSetThreadDesktop.Call(h); r == 0 {
		err := fmt.Errorf("SetThreadDesktop(%q) failed: win32=%d", name, win32LastError())
		_, _, _ = procCloseDesktop.Call(h)
		if deskWorkerMode {
			slog.Warn("无法附着输入桌面", "desktop", name, "err", err)
		}
		return err
	}
	old := c.curDesk
	c.curDesk = h
	c.curDeskName = name
	if old != 0 {
		_, _, _ = procCloseDesktop.Call(old)
	}
	slog.Info("桌面 worker 已附着输入桌面", "desktop", name, "session", currentSessionID())
	return nil
}

// CurrentDesktop exposes the attached input-desktop name for session meta.
func (c *winCapture) CurrentDesktop() string {
	if c == nil {
		return ""
	}
	return c.curDeskName
}

func openDeskCapture() (deskCapture, error) {
	setDeskDPIAware()
	if sid := currentSessionID(); sid == 0 {
		return nil, fmt.Errorf("screen capture unavailable in Session 0 (win32 session=0); install the Agent as a Windows service: aiops-agent --install-service")
	}
	w, _, _ := procGetSystemMetrics.Call(smCXScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("cannot read screen size (no interactive desktop? run Agent in a logged-on user session, not Session 0)")
	}
	c := &winCapture{w: int(w), h: int(h), monID: 1}
	mons := c.Monitors()
	for _, m := range mons {
		if m.Primary {
			_ = c.SetMonitor(m.ID)
			break
		}
	}
	// Probe raw GDI capture (no desktop-follow: that must happen on the capture
	// goroutine). In worker mode the first frame may precede desktop attach, so a
	// probe failure is only a warning; otherwise fail fast with a clear message.
	if _, err := c.captureGDI(); err != nil {
		if deskWorkerMode {
			slog.Warn("初始抓屏失败，进入输入桌面后将重试", "err", err)
		} else {
			return nil, fmt.Errorf("screen capture failed: %w (Agent must run in an interactive user session with an unlocked desktop; 若需锁屏/登录界面请以服务方式安装: aiops-agent --install-service)", err)
		}
	}
	return c, nil
}

func (c *winCapture) Size() (int, int) { return c.w, c.h }
func (c *winCapture) Close() error {
	if c.curDesk != 0 {
		_, _, _ = procCloseDesktop.Call(c.curDesk)
		c.curDesk = 0
	}
	return nil
}

func (c *winCapture) Capture() (image.Image, error) {
	if err := c.ensureInputDesktop(); err != nil {
		if deskWorkerMode {
			// Do not BitBlt on the wrong desktop — surface attach failure instead.
			return nil, err
		}
	}
	// RDP / DPI / monitor hotplug can change geometry between frames; stale
	// buffers against a resized desktop make BitBlt fail.
	c.refreshGeometry()
	img, err := c.captureGDI()
	if err != nil {
		// One hard retry: re-attach input desktop and re-query geometry (session
		// switches / RDP resize race with the first attempt).
		if attachErr := c.ensureInputDesktop(); attachErr != nil && deskWorkerMode {
			return nil, attachErr
		}
		c.refreshGeometry()
		img, err = c.captureGDI()
	}
	return img, err
}

// refreshGeometry re-reads the selected monitor's virtual-screen rect.
func (c *winCapture) refreshGeometry() {
	mons := c.Monitors()
	if len(mons) == 0 {
		return
	}
	for _, m := range mons {
		if m.ID == c.monID || (c.monID == 0 && m.Primary) {
			if m.Width > 0 && m.Height > 0 {
				c.monX, c.monY = m.X, m.Y
				c.w, c.h = m.Width, m.Height
				c.monID = m.ID
			}
			return
		}
	}
	for _, m := range mons {
		if m.Primary && m.Width > 0 && m.Height > 0 {
			c.monX, c.monY = m.X, m.Y
			c.w, c.h = m.Width, m.Height
			c.monID = m.ID
			return
		}
	}
	m := mons[0]
	if m.Width > 0 && m.Height > 0 {
		c.monX, c.monY = m.X, m.Y
		c.w, c.h = m.Width, m.Height
		c.monID = m.ID
	}
}

type bltSrc struct {
	hdc     uintptr
	x, y    int
	w, h    int
	release func()
}

func (c *winCapture) onSecureDesktop() bool {
	n := strings.ToLower(c.curDeskName)
	return n == "winlogon" || strings.HasPrefix(n, "winlogon") || n == "screensaver"
}

func (c *winCapture) openScreenDCs() []bltSrc {
	var out []bltSrc
	appendScreenDC := func() {
		if hdc, _, _ := procGetDC.Call(0); hdc != 0 {
			out = append(out, bltSrc{
				hdc: hdc, x: c.monX, y: c.monY, w: c.w, h: c.h,
				release: func() { _, _, _ = procReleaseDC.Call(0, hdc) },
			})
			vx, _, _ := procGetSystemMetrics.Call(smXVVirtualScreen)
			vy, _, _ := procGetSystemMetrics.Call(smYVVirtualScreen)
			vw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
			vh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
			if int(vw) > 0 && int(vh) > 0 && (int(vx) != c.monX || int(vy) != c.monY || int(vw) != c.w || int(vh) != c.h) {
				out = append(out, bltSrc{
					hdc: hdc, x: int(vx), y: int(vy), w: int(vw), h: int(vh),
					release: nil,
				})
			}
			sw, _, _ := procGetSystemMetrics.Call(smCXScreen)
			sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
			if int(sw) > 0 && int(sh) > 0 {
				out = append(out, bltSrc{
					hdc: hdc, x: 0, y: 0, w: int(sw), h: int(sh),
					release: nil,
				})
			}
		}
	}
	appendWindowDC := func() {
		desk, _, _ := procGetDesktopWindow.Call()
		if desk == 0 {
			return
		}
		if hdc, _, _ := procGetWindowDC.Call(desk); hdc != 0 {
			d := desk
			out = append(out, bltSrc{
				hdc: hdc, x: c.monX, y: c.monY, w: c.w, h: c.h,
				release: func() { _, _, _ = procReleaseDC.Call(d, hdc) },
			})
		}
	}
	// On Winlogon/secure desktop, GetDC(NULL) usually beats GetWindowDC(desktop)
	// (LogonUI is often not in the desktop-window GDI surface).
	if c.onSecureDesktop() {
		appendScreenDC()
		appendWindowDC()
	} else {
		appendWindowDC()
		appendScreenDC()
	}
	return out
}

func (c *winCapture) captureGDI() (image.Image, error) {
	if c.w < 1 || c.h < 1 {
		return nil, fmt.Errorf("invalid capture size %dx%d", c.w, c.h)
	}
	if c.w > 7680 || c.h > 4320 {
		return nil, fmt.Errorf("capture size too large: %dx%d", c.w, c.h)
	}

	srcs := c.openScreenDCs()
	if len(srcs) == 0 {
		return nil, fmt.Errorf("GetDC/GetWindowDC failed (no interactive desktop?)")
	}
	defer func() {
		seen := map[uintptr]bool{}
		for _, s := range srcs {
			if s.release != nil && !seen[s.hdc] {
				seen[s.hdc] = true
				s.release()
			}
		}
	}()

	var lastErr error
	var bestImg image.Image
	var bestScore int
	var bestSrc bltSrc
	for _, src := range srcs {
		if src.w < 1 || src.h < 1 {
			continue
		}
		img, err := bltToImage(src.hdc, src.x, src.y, src.w, src.h)
		if err != nil {
			lastErr = err
			continue
		}
		score := deskContentScore(img)
		// Skip pure flat fills unless we have nothing better (Winlogon dark UI
		// still scores above pure #000 / solid blue).
		if score < 8 && isLikelyUniform(img, false) && !c.onSecureDesktop() {
			if bestImg == nil || score > bestScore {
				bestImg, bestScore, bestSrc = img, score, src
			}
			continue
		}
		if score > bestScore {
			bestImg, bestScore, bestSrc = img, score, src
		}
		// Good enough — stop early on interactive Default desktop.
		if score >= 80 && !c.onSecureDesktop() {
			break
		}
	}

	// Prefer PrintWindow of LogonUI / credential UI when on the secure desktop —
	// classic BitBlt often returns near-black wallpaper while CAD chrome is DWM.
	if c.onSecureDesktop() {
		if img, err := captureSecureDesktopWindows(c.curDesk, c.w, c.h); err == nil && img != nil {
			pwScore := deskContentScore(img)
			if pwScore > bestScore || (pwScore >= 12 && bestScore < 25) {
				return img, nil
			}
		}
	}

	if bestImg != nil {
		if bestSrc.w > 0 && bestSrc.h > 0 {
			c.w, c.h = bestSrc.w, bestSrc.h
			c.monX, c.monY = bestSrc.x, bestSrc.y
		}
		return bestImg, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("BitBlt failed: all capture rects exhausted")
	}
	return nil, lastErr
}

// bltToImage copies a screen rectangle into an RGBA image.
// Primary path: CreateCompatibleBitmap (device-dependent) — BitBlt into a DDB is
// far more reliable than into a top-down DIB section on Server 2012 / some RDP
// mirror drivers (those return BitBlt=0 even when CreateDIBSection succeeded,
// which previously short-circuited before any fallback).
func bltToImage(srcDC uintptr, srcX, srcY, w, h int) (image.Image, error) {
	memDC, _, _ := procCreateCompatibleDC.Call(srcDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	var lastErr error
	// --- Path A: device-dependent bitmap + GetDIBits (deselect first) ---
	if img, err := bltViaDDB(memDC, srcDC, srcX, srcY, w, h); err == nil {
		return img, nil
	} else {
		lastErr = err
	}

	// --- Path B: bottom-up DIB section (positive height; top-down often breaks BitBlt) ---
	if img, err := bltViaDIB(memDC, srcDC, srcX, srcY, w, h, false); err == nil {
		return img, nil
	} else {
		lastErr = err
	}

	// --- Path C: top-down DIB section ---
	if img, err := bltViaDIB(memDC, srcDC, srcX, srcY, w, h, true); err == nil {
		return img, nil
	} else {
		lastErr = err
	}

	return nil, lastErr
}

func tryBitBlt(memDC, srcDC uintptr, srcX, srcY, w, h int) error {
	var lastCode uint32
	for _, rop := range []uintptr{srcCopy | captureBLT, srcCopy} {
		ret, _, callErr := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), srcDC,
			uintptr(int32(srcX)), uintptr(int32(srcY)), rop)
		if ret != 0 {
			return nil
		}
		if errno, ok := callErr.(syscall.Errno); ok {
			lastCode = uint32(errno)
		} else {
			lastCode = win32LastError()
		}
		// Some RDP/mirror display drivers reject BitBlt but implement the same
		// SRCCOPY through StretchBlt. Use an equal-size stretch as a real second
		// capture backend before giving up.
		ret, _, callErr = procStretchBlt.Call(
			memDC, 0, 0, uintptr(w), uintptr(h),
			srcDC, uintptr(int32(srcX)), uintptr(int32(srcY)), uintptr(w), uintptr(h),
			rop,
		)
		if ret != 0 {
			return nil
		}
		if errno, ok := callErr.(syscall.Errno); ok {
			lastCode = uint32(errno)
		} else {
			lastCode = win32LastError()
		}
	}
	return fmt.Errorf("BitBlt/StretchBlt failed at %dx%d@%d,%d: win32=%d", w, h, srcX, srcY, lastCode)
}

type winRECT struct {
	Left, Top, Right, Bottom int32
}

const (
	processQueryLimitedInfo = 0x1000
	pwRenderFullContent     = 0x00000002
)

type enumWinCandidate struct {
	hwnd  uintptr
	score int
	w, h  int
	title string
	class string
	exe   string
}

var (
	enumWinMu   sync.Mutex
	enumWinList []enumWinCandidate
)

// captureSecureDesktopWindows tries PrintWindow on LogonUI / credential UI
// windows visible on the currently attached desktop (Winlogon).
func captureSecureDesktopWindows(deskHandle uintptr, targetW, targetH int) (image.Image, error) {
	enumWinMu.Lock()
	enumWinList = enumWinList[:0]
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		var rc winRECT
		if r, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r == 0 {
			return 1
		}
		ww := int(rc.Right - rc.Left)
		hh := int(rc.Bottom - rc.Top)
		if ww < 120 || hh < 80 {
			return 1
		}
		title := windowTextOf(hwnd)
		class := windowClassOf(hwnd)
		exe := windowExeOf(hwnd)
		lowTitle := strings.ToLower(title)
		lowClass := strings.ToLower(class)
		lowExe := strings.ToLower(exe)
		score := 0
		switch {
		case strings.Contains(lowExe, "logonui"):
			score += 100
		case strings.Contains(lowExe, "authhost"), strings.Contains(lowExe, "credential"):
			score += 80
		case strings.Contains(lowExe, "lockapp"), strings.Contains(lowExe, "lockapphost"):
			score += 70
		}
		if strings.Contains(lowClass, "credential") || strings.Contains(lowClass, "logon") {
			score += 50
		}
		if strings.Contains(lowTitle, "windows security") || strings.Contains(lowTitle, "windows 安全") ||
			strings.Contains(lowTitle, "sign in") || strings.Contains(lowTitle, "登录") {
			score += 40
		}
		if strings.Contains(lowClass, "immersive") || strings.Contains(lowClass, "applicationframe") {
			score += 15
		}
		// Prefer near-fullscreen windows on the secure desktop.
		if targetW > 0 && targetH > 0 {
			area := ww * hh
			screen := targetW * targetH
			if screen > 0 {
				pct := area * 100 / screen
				if pct >= 70 {
					score += 40
				} else if pct >= 35 {
					score += 25
				} else if pct >= 10 {
					score += 10
				}
			}
		}
		// On Winlogon, even unnamed large windows are worth trying (DWM chrome).
		if score == 0 && ww >= 400 && hh >= 300 {
			score = 5
		}
		if score > 0 {
			enumWinList = append(enumWinList, enumWinCandidate{
				hwnd: hwnd, score: score, w: ww, h: hh, title: title, class: class, exe: exe,
			})
		}
		return 1
	})
	// Prefer EnumDesktopWindows so we only see the attached Winlogon desktop.
	if deskHandle != 0 {
		_, _, _ = procEnumDesktopWindows.Call(deskHandle, cb, 0)
	}
	if len(enumWinList) == 0 {
		_, _, _ = procEnumWindows.Call(cb, 0)
	}
	cands := append([]enumWinCandidate(nil), enumWinList...)
	enumWinMu.Unlock()

	if len(cands) == 0 {
		return nil, fmt.Errorf("no secure-desktop UI windows")
	}
	// Highest score first; try several in case PrintWindow fails on the top hit.
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].score > cands[i].score {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	var lastErr error
	limit := len(cands)
	if limit > 6 {
		limit = 6
	}
	for _, cand := range cands[:limit] {
		img, err := printWindowToImage(cand.hwnd, cand.w, cand.h)
		if err != nil {
			lastErr = err
			continue
		}
		if deskContentScore(img) < 8 && isLikelyUniform(img, false) {
			lastErr = fmt.Errorf("PrintWindow returned flat frame")
			continue
		}
		slog.Info("Winlogon UI PrintWindow 成功",
			"exe", filepath.Base(cand.exe), "class", cand.class,
			"size", fmt.Sprintf("%dx%d", cand.w, cand.h), "score", cand.score)
		return img, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("PrintWindow exhausted candidates")
	}
	return nil, lastErr
}

func windowTextOf(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func windowClassOf(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func windowExeOf(hwnd uintptr) string {
	var pid uint32
	_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInfo, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandleDesk.Call(h)
	var buf [syscall.MAX_PATH]uint16
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}

func printWindowToImage(hwnd uintptr, w, h int) (image.Image, error) {
	if w < 1 || h < 1 {
		return nil, fmt.Errorf("invalid PrintWindow size")
	}
	if w > 7680 {
		w = 7680
	}
	if h > 4320 {
		h = 4320
	}
	hdcWin, _, _ := procGetWindowDC.Call(hwnd)
	if hdcWin == 0 {
		return nil, fmt.Errorf("GetWindowDC failed")
	}
	defer procReleaseDC.Call(hwnd, hdcWin)
	memDC, _, _ := procCreateCompatibleDC.Call(hdcWin)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)
	bmp, _, _ := procCreateCompatibleBitmap.Call(hdcWin, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(memDC, bmp)
	// PW_RENDERFULLCONTENT (0x2) captures DWM-composited content when available.
	r, _, _ := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent)
	if r == 0 {
		r, _, _ = procPrintWindow.Call(hwnd, memDC, 0)
	}
	if r == 0 {
		_, _, _ = procSelectObject.Call(memDC, old)
		return nil, fmt.Errorf("PrintWindow failed")
	}
	_, _, _ = procSelectObject.Call(memDC, old) // deselect before GetDIBits
	bi := bitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(w), Height: -int32(h),
		Planes: 1, BitCount: 32, Compression: biRGB,
	}
	buf := make([]byte, w*h*4)
	n, _, _ := procGetDIBits.Call(memDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if n == 0 {
		n, _, _ = procGetDIBits.Call(hdcWin, bmp, 0, uintptr(h),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	}
	if n == 0 {
		return nil, fmt.Errorf("GetDIBits after PrintWindow failed")
	}
	return bgraBufToRGBA(buf, w, h), nil
}

func bltViaDDB(memDC, srcDC uintptr, srcX, srcY, w, h int) (image.Image, error) {
	bmp, _, _ := procCreateCompatibleBitmap.Call(srcDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)

	old, _, _ := procSelectObject.Call(memDC, bmp)
	if err := tryBitBlt(memDC, srcDC, srcX, srcY, w, h); err != nil {
		_, _, _ = procSelectObject.Call(memDC, old)
		return nil, err
	}
	_, _, _ = procSelectObject.Call(memDC, old) // MUST deselect before GetDIBits

	bi := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h),
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	buf := make([]byte, w*h*4)
	n, _, _ := procGetDIBits.Call(srcDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if n == 0 {
		n, _, _ = procGetDIBits.Call(memDC, bmp, 0, uintptr(h),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	}
	if n == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}
	return bgraBufToRGBA(buf, w, h), nil
}

func bltViaDIB(memDC, srcDC uintptr, srcX, srcY, w, h int, topDown bool) (image.Image, error) {
	height := int32(h)
	if topDown {
		height = -height
	}
	bi := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      height,
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	var bits unsafe.Pointer
	bmp, _, _ := procCreateDIBSection.Call(srcDC, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(bmp)

	old, _, _ := procSelectObject.Call(memDC, bmp)
	if err := tryBitBlt(memDC, srcDC, srcX, srcY, w, h); err != nil {
		_, _, _ = procSelectObject.Call(memDC, old)
		return nil, err
	}
	_, _, _ = procSelectObject.Call(memDC, old)

	nPix := w * h
	src := unsafe.Slice((*byte)(bits), nPix*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if topDown {
		for i := 0; i < nPix; i++ {
			off := i * 4
			img.Pix[off+0] = src[off+2]
			img.Pix[off+1] = src[off+1]
			img.Pix[off+2] = src[off+0]
			img.Pix[off+3] = 255
		}
		return img, nil
	}
	// Bottom-up DIB: flip rows.
	stride := w * 4
	for y := 0; y < h; y++ {
		srcOff := (h - 1 - y) * stride
		dstOff := y * stride
		for x := 0; x < w; x++ {
			s := srcOff + x*4
			d := dstOff + x*4
			img.Pix[d+0] = src[s+2]
			img.Pix[d+1] = src[s+1]
			img.Pix[d+2] = src[s+0]
			img.Pix[d+3] = 255
		}
	}
	return img, nil
}

func bgraBufToRGBA(buf []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		off := i * 4
		img.Pix[off+0] = buf[off+2]
		img.Pix[off+1] = buf[off+1]
		img.Pix[off+2] = buf[off+0]
		img.Pix[off+3] = 255
	}
	return img
}

type winInput struct {
	curDesk     uintptr
	curDeskName string
	locked      bool
	monX, monY  int // current monitor origin in virtual-screen coords
	lastAX      int // last absolute cursor position (virtual-screen)
	lastAY      int
	failLogAt   time.Time // rate-limit SendInput failure logs
	probed      bool
	lastDeskChk time.Time // throttle OpenInputDesktop during fast typing
}

func (i *winInput) SetOrigin(x, y int) { i.monX, i.monY = x, y }

func openDeskInput() (deskInput, error) {
	i := &winInput{}
	// Attach + SendInput probe up front in worker mode so failures show in the
	// agent log before the operator clicks (capture can succeed while input cannot).
	if deskFollowSecureDesktop {
		_ = i.ensureInputDesktop()
	}
	return i, nil
}
func (i *winInput) Close() error {
	if i.curDesk != 0 {
		_, _, _ = procCloseDesktop.Call(i.curDesk)
		i.curDesk = 0
	}
	return nil
}

func (i *winInput) logSendFail(kind string, errno uint32, extra ...any) {
	now := time.Now()
	if !i.failLogAt.IsZero() && now.Sub(i.failLogAt) < 5*time.Second {
		return
	}
	i.failLogAt = now
	args := []any{"kind", kind, "win32", errno, "desktop", i.curDeskName, "session", currentSessionID()}
	args = append(args, extra...)
	slog.Warn("SendInput/键鼠注入失败", args...)
}

// ensureInputDesktop attaches the calling (input) thread to the current input
// desktop so SendInput/SetCursorPos reach the lock/logon/secure desktop.
//
// Fast typing on the lock screen used to call OpenInputDesktop on every key —
// that dwarfed the SendInput cost. We trust a recent attach briefly and only
// re-open when the input desktop name changes (lock ↔ logon ↔ user).
func (i *winInput) ensureInputDesktop() error {
	if !deskFollowSecureDesktop {
		return nil
	}
	if !i.locked {
		runtime.LockOSThread()
		i.locked = true
	}
	now := time.Now()
	// Trust a recent attach longer — OpenInputDesktop on every key made lock-screen
	// typing feel like ~1 char / 200–500ms even when SendInput itself was fine.
	if i.curDesk != 0 && i.curDeskName != "" && now.Sub(i.lastDeskChk) < 500*time.Millisecond {
		if threadDesktopName() == i.curDeskName {
			return nil
		}
	}
	h, err := openInputDesktop()
	if err != nil {
		if deskWorkerMode {
			slog.Warn("输入线程无法打开输入桌面", "err", err)
		}
		return err
	}
	name := desktopNameOf(h)
	i.lastDeskChk = now
	if i.curDesk != 0 && name == i.curDeskName && threadDesktopName() == name {
		_, _, _ = procCloseDesktop.Call(h)
		return nil
	}
	if r, _, _ := procSetThreadDesktop.Call(h); r == 0 {
		err := fmt.Errorf("SetThreadDesktop(%q) failed: win32=%d", name, win32LastError())
		if deskWorkerMode {
			slog.Warn("输入线程无法附着输入桌面", "desktop", name, "err", err)
		}
		_, _, _ = procCloseDesktop.Call(h)
		return err
	}
	old := i.curDesk
	i.curDesk = h
	i.curDeskName = name
	i.probed = false // re-probe after desktop switch
	if old != 0 {
		_, _, _ = procCloseDesktop.Call(old)
	}
	slog.Info("输入线程已附着输入桌面", "desktop", name, "session", currentSessionID())
	i.probeSendInputOnce()
	return nil
}

// probeSendInputOnce verifies JOURNALPLAYBACK actually works after attach.
func (i *winInput) probeSendInputOnce() {
	if i.probed || !deskWorkerMode {
		return
	}
	i.probed = true
	inp := winMouseInput{
		Type:  inputMouse,
		Flags: mouseeventfMove, // relative 0,0 — no visible motion
	}
	n, _, callErr := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), uintptr(unsafe.Sizeof(inp)))
	if n != 0 {
		slog.Info("SendInput 探测成功（键鼠注入可用）", "desktop", i.curDeskName)
		return
	}
	errno := win32LastError()
	if e, ok := callErr.(syscall.Errno); ok && e != 0 {
		errno = uint32(e)
	}
	slog.Warn("SendInput 探测失败：键鼠可能不可用",
		"win32", errno, "desktop", i.curDeskName, "session", currentSessionID(),
		"hint", "OpenInputDesktop 需含 DESKTOP_JOURNALPLAYBACK；检查 AppLocker/会话桌面 ACL")
}

// winMouseInput matches the Windows INPUT/MOUSEINPUT layout on amd64/arm64
// (4-byte type + 4-byte pad + MOUSEINPUT with 8-byte ExtraInfo alignment).
type winMouseInput struct {
	Type      uint32
	_         uint32
	Dx        int32
	Dy        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	_         uint32
	ExtraInfo uintptr
}

type winKeyInput struct {
	Type      uint32
	_         uint32
	Vk        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	_         uint32
	ExtraInfo uintptr
	_         [8]byte // pad to sizeof(INPUT) matching mouse variant (40 on amd64)
}

func (i *winInput) sendMouseAbsolute(ax, ay int, btnFlags, data uint32) bool {
	vx, _, _ := procGetSystemMetrics.Call(smXVVirtualScreen)
	vy, _, _ := procGetSystemMetrics.Call(smYVVirtualScreen)
	vw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	vh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	if int(vw) < 2 {
		vw = 2
	}
	if int(vh) < 2 {
		vh = 2
	}
	nx := int32((int64(ax-int(vx)) * 65535) / int64(int(vw)-1))
	ny := int32((int64(ay-int(vy)) * 65535) / int64(int(vh)-1))
	if nx < 0 {
		nx = 0
	}
	if ny < 0 {
		ny = 0
	}
	if nx > 65535 {
		nx = 65535
	}
	if ny > 65535 {
		ny = 65535
	}
	try := func(flags uint32) (bool, uint32) {
		inp := winMouseInput{
			Type:      inputMouse,
			Dx:        nx,
			Dy:        ny,
			MouseData: data,
			Flags:     flags,
		}
		n, _, callErr := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), uintptr(unsafe.Sizeof(inp)))
		if n != 0 {
			return true, 0
		}
		errno := win32LastError()
		if e, ok := callErr.(syscall.Errno); ok && e != 0 {
			errno = uint32(e)
		}
		return false, errno
	}
	// Prefer virtual-desktop absolute; fall back without VIRTUALDESK (some RDP /
	// hardened sessions accept only the primary-monitor absolute form).
	if ok, _ := try(mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk | btnFlags); ok {
		return true
	}
	if ok, errno := try(mouseeventfMove | mouseeventfAbsolute | btnFlags); ok {
		return true
	} else {
		i.logSendFail("mouse", errno, "ax", ax, "ay", ay)
		return false
	}
}

func (i *winInput) sendMouseButton(flags, data uint32) bool {
	inp := winMouseInput{
		Type:      inputMouse,
		MouseData: data,
		Flags:     flags,
	}
	n, _, callErr := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), uintptr(unsafe.Sizeof(inp)))
	if n != 0 {
		return true
	}
	errno := win32LastError()
	if e, ok := callErr.(syscall.Errno); ok && e != 0 {
		errno = uint32(e)
	}
	i.logSendFail("mouse_btn", errno, "flags", flags)
	return false
}

func (i *winInput) MouseMove(x, y int) error {
	if err := i.ensureInputDesktop(); err != nil {
		return err
	}
	ax, ay := i.monX+x, i.monY+y
	i.lastAX, i.lastAY = ax, ay
	if r, _, _ := procSetCursorPos.Call(uintptr(int32(ax)), uintptr(int32(ay))); r == 0 {
		i.logSendFail("SetCursorPos", win32LastError(), "ax", ax, "ay", ay)
	}
	_ = i.sendMouseAbsolute(ax, ay, 0, 0)
	return nil
}

func (i *winInput) MouseButton(button int, down bool) error {
	if err := i.ensureInputDesktop(); err != nil {
		return err
	}
	var flags uint32
	switch button {
	case 2:
		if down {
			flags = mouseeventfRightDown
		} else {
			flags = mouseeventfRightUp
		}
	case 3:
		if down {
			flags = mouseeventfMiddleDown
		} else {
			flags = mouseeventfMiddleUp
		}
	default:
		if down {
			flags = mouseeventfLeftDown
		} else {
			flags = mouseeventfLeftUp
		}
	}
	ax, ay := i.lastAX, i.lastAY
	if ax == 0 && ay == 0 {
		ax, ay = i.monX, i.monY
	}
	_, _, _ = procSetCursorPos.Call(uintptr(int32(ax)), uintptr(int32(ay)))
	// Combined move+button in one SendInput is more reliable than separate calls
	// under UIPI / RDP (button-only events often land at the wrong cursor).
	if i.sendMouseAbsolute(ax, ay, flags, 0) {
		return nil
	}
	if i.sendMouseButton(flags, 0) {
		return nil
	}
	_, _, _ = procMouseEvent.Call(uintptr(flags), 0, 0, 0, 0)
	return nil
}

func (i *winInput) MouseWheel(delta int) error {
	if err := i.ensureInputDesktop(); err != nil {
		return err
	}
	data := uint32(int32(delta) * 120)
	if !i.sendMouseButton(mouseeventfWheel, data) {
		_, _, _ = procMouseEvent.Call(mouseeventfWheel, 0, 0, uintptr(data), 0)
	}
	return nil
}

func (i *winInput) Key(vk int, down bool) error {
	if err := i.ensureInputDesktop(); err != nil {
		return err
	}
	var flags uint32
	if !down {
		flags = keyeventfKeyUp
	}
	if deskVKExtended(vk) {
		flags |= keyeventfExtendedKey
	}
	scan, _, _ := procMapVirtualKeyW.Call(uintptr(vk), mapVKToVSC)
	inp := winKeyInput{
		Type:  inputKeyboard,
		Vk:    uint16(vk),
		Scan:  uint16(scan),
		Flags: flags,
	}
	// cbSize must be sizeof(INPUT); use the mouse variant size (canonical INPUT).
	cb := unsafe.Sizeof(winMouseInput{})
	n, _, callErr := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), cb)
	if n == 0 {
		errno := win32LastError()
		if e, ok := callErr.(syscall.Errno); ok && e != 0 {
			errno = uint32(e)
		}
		i.logSendFail("key", errno, "vk", vk, "down", down)
		_, _, _ = procKeybdEvent.Call(uintptr(vk), scan, uintptr(flags), 0)
	}
	return nil
}

func deskGOOS() string { return "windows" }

func linuxIsKylinFamily() bool { return false }

var (
	modSas      = syscall.NewLazyDLL("sas.dll")
	procSendSAS = modSas.NewProc("SendSAS")
)

// SendCAD triggers Ctrl+Alt+Del (Secure Attention Sequence).
// Prefer the Session-0 service pipe — the in-session desktop worker is not an
// SCM service, so a bare SendSAS call is often ignored by Winlogon.
// Do not click the lock UI first: on "Press Ctrl+Alt+Del" a click can steal
// focus without helping, and RDP sessions need session-targeted SendSAS(TRUE).
func (i *winInput) SendCAD() error {
	if err := i.ensureInputDesktop(); err != nil {
		slog.Warn("SendCAD: 附着输入桌面失败，仍尝试注入 SAS", "err", err)
	}
	return injectSecureAttentionSequence()
}

// TypeText injects Unicode text via KEYEVENTF_UNICODE (works on lock screen
// password boxes; layout/CapsLock independent). Live typing and unlock both use
// this path for printable characters.
func (i *winInput) TypeText(text string) error {
	if err := i.ensureInputDesktop(); err != nil {
		return err
	}
	cb := unsafe.Sizeof(winMouseInput{})
	flush := func(batch []winKeyInput) error {
		if len(batch) == 0 {
			return nil
		}
		n, _, callErr := procSendInput.Call(uintptr(len(batch)), uintptr(unsafe.Pointer(&batch[0])), cb)
		if n == 0 {
			errno := win32LastError()
			if e, ok := callErr.(syscall.Errno); ok && e != 0 {
				errno = uint32(e)
			}
			i.logSendFail("type_text", errno, "count", len(batch))
			// Fallback: one rune at a time (some Winlogon builds reject large batches).
			if len(batch) > 2 {
				for off := 0; off+1 < len(batch); off += 2 {
					pair := batch[off : off+2]
					n2, _, _ := procSendInput.Call(2, uintptr(unsafe.Pointer(&pair[0])), cb)
					if n2 == 0 {
						return fmt.Errorf("UNICODE 注入失败（desktop=%s win32=%d）", i.curDeskName, win32LastError())
					}
					time.Sleep(time.Millisecond)
				}
				return nil
			}
			return fmt.Errorf("UNICODE 注入失败（desktop=%s）", i.curDeskName)
		}
		if n != uintptr(len(batch)) {
			// Partial accept — finish remaining events individually.
			rest := batch[n:]
			for off := 0; off+1 < len(rest); off += 2 {
				pair := rest[off : off+2]
				n2, _, _ := procSendInput.Call(2, uintptr(unsafe.Pointer(&pair[0])), cb)
				if n2 == 0 {
					return fmt.Errorf("UNICODE 注入不完整（desktop=%s）", i.curDeskName)
				}
			}
		}
		return nil
	}
	const chunk = 16 // smaller batches — Winlogon password edits drop large floods
	batch := make([]winKeyInput, 0, chunk*2)
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' {
			if err := flush(batch); err != nil {
				return err
			}
			batch = batch[:0]
			vk := 0x0D
			if r == '\t' {
				vk = 0x09
			}
			_ = i.Key(vk, true)
			_ = i.Key(vk, false)
			continue
		}
		// BMP only in one INPUT; skip/split non-BMP (passwords are BMP).
		if r > 0xFFFF {
			continue
		}
		batch = append(batch,
			winKeyInput{Type: inputKeyboard, Vk: 0, Scan: uint16(r), Flags: keyeventfUnicode},
			winKeyInput{Type: inputKeyboard, Vk: 0, Scan: uint16(r), Flags: keyeventfUnicode | keyeventfKeyUp},
		)
		if len(batch) >= chunk*2 {
			if err := flush(batch); err != nil {
				return err
			}
			batch = batch[:0]
			// Yield so LogonUI can process — keep tiny to avoid "one char / half second".
			time.Sleep(time.Millisecond)
		}
	}
	return flush(batch)
}

func (i *winInput) DeskInputMeta() deskInputMeta {
	ok := true
	if deskFollowSecureDesktop {
		if err := i.ensureInputDesktop(); err != nil {
			ok = false
		}
	}
	hint := deskLockHintForDesktop(i.curDeskName)
	if !deskWorkerMode {
		hint = "当前非桌面 worker：锁屏键鼠可能无效。请以管理员安装 Agent 服务（--install-service）。"
		ok = i.curDeskName != "" || !deskFollowSecureDesktop
	}
	return deskInputMeta{
		Desktop:        i.curDeskName,
		InputDesktopOK: ok,
		CAD:            deskWorkerMode || deskFollowSecureDesktop,
		TypeText:       true,
		SecureDesktop:  deskIsSecureName(i.curDeskName),
		LockHint:       hint,
	}
}

// deskH264Usable gates the ffmpeg H.264 path.
// Worker mode uses BitBlt → rawvideo stdin (not gdigrab) so lock/logon/Update
// secure desktops stay visible. Foreground sessions may use gdigrab.
func deskH264Usable() bool { return ffmpegAvailable() }
func deskPreferredCodec() string {
	if deskWorkerMode && ffmpegAvailable() {
		return "h264"
	}
	return ""
}
func deskNeedsRawH264() bool { return deskWorkerMode }

// deskLegacyCaptureHost is true on pre–Windows 10 kernels (Server 2012/R2, Win8/8.1)
// where full-res JPEG @15fps routinely stalls the reverse channel.
func deskLegacyCaptureHost() bool {
	maj, min, _, _ := winVersion()
	if maj < 6 {
		return true
	}
	if maj == 6 && min <= 3 {
		return true
	}
	return false
}
func deskAVFScreenIndex() int { return -1 }
