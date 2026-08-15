//go:build windows

package main

import (
	"log/slog"
	"os"
	"sync"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

// Windows pseudo-console (ConPTY, Windows 10 1809+) backing for the remote
// terminal — a real interactive TTY so colours, line editing, Ctrl+C and
// full-screen programs work. Pure syscall (no cgo, no third-party), matching the
// zero-dependency Win32 approach in collector_windows.go.

var (
	modkernel32c = syscall.NewLazyDLL("kernel32.dll")

	procCreatePseudoConsole               = modkernel32c.NewProc("CreatePseudoConsole")
	procResizePseudoConsole               = modkernel32c.NewProc("ResizePseudoConsole")
	procClosePseudoConsole                = modkernel32c.NewProc("ClosePseudoConsole")
	procCreatePipe                        = modkernel32c.NewProc("CreatePipe")
	procInitializeProcThreadAttributeList = modkernel32c.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = modkernel32c.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = modkernel32c.NewProc("DeleteProcThreadAttributeList")
	procCreateProcessW2                   = modkernel32c.NewProc("CreateProcessW")
	procWaitForSingleObjectT              = modkernel32c.NewProc("WaitForSingleObject")
	procTerminateProcessT                 = modkernel32c.NewProc("TerminateProcess")
	procCloseHandleT                      = modkernel32c.NewProc("CloseHandle")
	procMultiByteToWideChar               = modkernel32c.NewProc("MultiByteToWideChar")
	procWideCharToMultiByte               = modkernel32c.NewProc("WideCharToMultiByte")
)

const (
	cpACP  = 0 // system default ANSI code page (GBK on Chinese Windows)
	cpUTF8 = 65001
)

const (
	procThreadAttrPseudoConsole = 0x00020016
	extendedStartupInfoPresent  = 0x00080000
	startfUseStdHandles         = 0x00000100
	createUnicodeEnvironment    = 0x00000400
	infinite                    = 0xFFFFFFFF
)

// startupInfoExW mirrors STARTUPINFOEXW: STARTUPINFOW followed by the attribute
// list pointer.
type startupInfoExW struct {
	startupInfo   syscall.StartupInfo
	attributeList uintptr
}

// coordVal packs a COORD (two SHORTs) into the DWORD-by-value form the ConPTY
// APIs take (X in the low word, Y in the high word).
func coordVal(cols, rows int) uintptr {
	return uintptr(uint32(uint16(cols)) | uint32(uint16(rows))<<16)
}

type conptyShell struct {
	hpc     uintptr
	hProc   syscall.Handle
	hThread syscall.Handle
	inFile  *os.File // write keystrokes here → ConPTY input
	outFile *os.File // read shell output here ← ConPTY output
	attrBuf []byte   // keeps the attribute list memory alive
	convBuf []byte   // leftover bytes from UTF-8 conversion (may exceed read buffer)
	rawPend []byte   // incomplete ACP/MBCS bytes waiting for the next Read

	termOnce sync.Once // guards shell termination (Close)
	reapOnce sync.Once // guards process/thread handle close — only after Wait sees the exit
}

// newPTY starts the shell attached to a fresh pseudo console. Returns nil on any
// failure so the caller falls back to piped stdio.
func newPTY(cols, rows int) termShell {
	if err := procCreatePseudoConsole.Find(); err != nil { // ConPTY unavailable (< Win10 1809)
		slog.Warn("ConPTY 不可用(将回退管道)", "err", err)
		return nil
	}
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 30
	}

	var inR, inW, outR, outW syscall.Handle
	if r, _, _ := procCreatePipe.Call(uintptr(unsafe.Pointer(&inR)), uintptr(unsafe.Pointer(&inW)), 0, 0); r == 0 {
		return nil
	}
	if r, _, _ := procCreatePipe.Call(uintptr(unsafe.Pointer(&outR)), uintptr(unsafe.Pointer(&outW)), 0, 0); r == 0 {
		procCloseHandleT.Call(uintptr(inR))
		procCloseHandleT.Call(uintptr(inW))
		return nil
	}

	var hpc uintptr
	// HRESULT CreatePseudoConsole(COORD, hInput, hOutput, dwFlags, HPCON*)
	hr, _, _ := procCreatePseudoConsole.Call(coordVal(cols, rows), uintptr(inR), uintptr(outW), 0, uintptr(unsafe.Pointer(&hpc)))
	// The pseudo console dups the ends it needs; drop our copies of the pipe ends
	// it now owns so a shell exit propagates EOF to outR.
	procCloseHandleT.Call(uintptr(inR))
	procCloseHandleT.Call(uintptr(outW))
	if hr != 0 || hpc == 0 {
		slog.Error("ConPTY CreatePseudoConsole 失败", "hr", hr, "hpc", hpc)
		procCloseHandleT.Call(uintptr(inW))
		procCloseHandleT.Call(uintptr(outR))
		return nil
	}

	// Build a process/thread attribute list carrying the pseudo console handle.
	var listSize uintptr
	procInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&listSize)))
	if listSize == 0 {
		slog.Warn("ConPTY InitializeProcThreadAttributeList(size) 返回 0")
		closeConPTY(hpc, inW, outR)
		return nil
	}
	attrBuf := make([]byte, listSize)
	attrList := uintptr(unsafe.Pointer(&attrBuf[0]))
	if r, _, e := procInitializeProcThreadAttributeList.Call(attrList, 1, 0, uintptr(unsafe.Pointer(&listSize))); r == 0 {
		slog.Error("ConPTY InitializeProcThreadAttributeList 失败", "err", e)
		closeConPTY(hpc, inW, outR)
		return nil
	}
	if r, _, e := procUpdateProcThreadAttribute.Call(attrList, 0, procThreadAttrPseudoConsole, hpc, unsafe.Sizeof(hpc), 0, 0); r == 0 {
		slog.Error("ConPTY UpdateProcThreadAttribute 失败", "err", e)
		procDeleteProcThreadAttributeList.Call(attrList)
		closeConPTY(hpc, inW, outR)
		return nil
	}

	var si startupInfoExW
	si.startupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.attributeList = attrList
	// The agent's own stdio may be redirected to a log file; without this the
	// child would inherit a *copy* of that stdout and write its banner/prompt
	// there instead of into the pseudo console. Forcing STARTF_USESTDHANDLES with
	// NULL std handles makes cmd fall back to the pseudo console (CONIN$/CONOUT$).
	si.startupInfo.Flags = startfUseStdHandles

	cmdline, err := syscall.UTF16FromString(shellExe())
	if err != nil {
		procDeleteProcThreadAttributeList.Call(attrList)
		closeConPTY(hpc, inW, outR)
		return nil
	}
	// Start the shell in a usable directory (not LocalSystem systemprofile).
	var cwdPtr uintptr
	if dir := interactiveShellDir(); dir != "" {
		cwd, _ := syscall.UTF16PtrFromString(dir)
		cwdPtr = uintptr(unsafe.Pointer(cwd))
	}
	envBlock := envToUTF16Block(buildShellEnv())
	var envPtr uintptr
	// Break away from the SCM job and start a new process group so a service
	// stop cannot tear down Java/cmd started from the remote terminal.
	creationFlags := uintptr(extendedStartupInfoPresent | createBreakawayJob | createNewProcessGroup)
	if len(envBlock) > 0 {
		envPtr = uintptr(unsafe.Pointer(&envBlock[0]))
		creationFlags |= createUnicodeEnvironment
	}
	var pi syscall.ProcessInformation
	r, _, _ := procCreateProcessW2.Call(
		0,                                    // application name
		uintptr(unsafe.Pointer(&cmdline[0])), // command line (mutable buffer)
		0, 0,                                 // process / thread security
		0,              // bInheritHandles = FALSE (ConPTY passes stdio via the attribute)
		creationFlags,  // creation flags
		envPtr, cwdPtr, // environment / current dir
		uintptr(unsafe.Pointer(&si)), // lpStartupInfo (STARTUPINFOEX)
		uintptr(unsafe.Pointer(&pi)), // lpProcessInformation
	)
	procDeleteProcThreadAttributeList.Call(attrList)
	if r == 0 {
		slog.Error("ConPTY CreateProcess 失败")
		closeConPTY(hpc, inW, outR)
		return nil
	}

	slog.Info("ConPTY 已启动", "cols", cols, "rows", rows, "pid", pi.ProcessId)
	return &conptyShell{
		hpc: hpc, hProc: pi.Process, hThread: pi.Thread,
		inFile:  os.NewFile(uintptr(inW), "conpty-in"),
		outFile: os.NewFile(uintptr(outR), "conpty-out"),
		attrBuf: attrBuf,
	}
}

func closeConPTY(hpc uintptr, inW, outR syscall.Handle) {
	procClosePseudoConsole.Call(hpc)
	procCloseHandleT.Call(uintptr(inW))
	procCloseHandleT.Call(uintptr(outR))
}

// shellExe returns the shell to launch (absolute cmd.exe) with Path repair via
// bootstrap .cmd when possible (avoids /K quote mangling on older Windows).
func shellExe() string {
	if boot := writeWindowsShellBootstrap(); boot != "" {
		return windowsCmdPath() + " /K " + boot
	}
	return windowsCmdPath() + " /K " + windowsShellInitCmd()
}

// envToUTF16Block builds a Windows CreateProcess environment block
// (UTF-16LE, KEY=VAL\0 … \0).
func envToUTF16Block(env []string) []uint16 {
	if len(env) == 0 {
		return nil
	}
	var buf []uint16
	for _, e := range env {
		u, err := syscall.UTF16FromString(e)
		if err != nil {
			continue
		}
		buf = append(buf, u...)
	}
	if len(buf) == 0 {
		return nil
	}
	buf = append(buf, 0) // extra NUL terminator
	return buf
}

// ensureUTF8 converts possible non-UTF-8 bytes (GBK on Chinese Windows) to
// UTF-8. Used by the playbook exec session to fix output from programs that
// don't respect chcp 65001. Delegates to convertToUTF8 (Windows API).
func ensureUTF8(b []byte) []byte {
	out, _ := ensureUTF8Hold(b)
	return out
}

// ensureUTF8Hold is a streaming-safe ACP→UTF-8 converter: incomplete trailing
// multi-byte sequences are returned in hold for the next Read (pipe/ConPTY
// chunk boundaries often split GBK characters).
func ensureUTF8Hold(data []byte) (out, hold []byte) {
	if len(data) == 0 {
		return data, nil
	}
	if utf8.Valid(data) {
		return data, nil
	}
	// Peel 0..3 trailing bytes that may be an incomplete ACP sequence.
	const mbErrInvalidChars = 8
	for peel := 0; peel <= 3 && peel < len(data); peel++ {
		chunk := data[:len(data)-peel]
		if len(chunk) == 0 {
			break
		}
		converted, ok := convertACPToUTF8(chunk, mbErrInvalidChars)
		if ok {
			if peel == 0 {
				return converted, nil
			}
			return converted, append([]byte{}, data[len(data)-peel:]...)
		}
	}
	// Last resort: convert without strict validation (may drop bad bytes).
	return convertToUTF8(data), nil
}

func (c *conptyShell) Read(b []byte) (int, error) {
	// Return leftover converted data from a previous read first.
	if len(c.convBuf) > 0 {
		n := copy(b, c.convBuf)
		c.convBuf = c.convBuf[n:]
		return n, nil
	}
	n, err := c.outFile.Read(b)
	if n > 0 {
		chunk := append(append([]byte{}, c.rawPend...), b[:n]...)
		converted, hold := ensureUTF8Hold(chunk)
		c.rawPend = hold
		if len(converted) <= len(b) {
			n = copy(b, converted)
		} else {
			// UTF-8 output can be larger than GBK input; buffer the excess.
			n = copy(b, converted)
			c.convBuf = append(c.convBuf[:0], converted[n:]...)
		}
	}
	return n, err
}
func (c *conptyShell) Write(b []byte) (int, error) { return c.inFile.Write(b) }
func (c *conptyShell) Resize(cols, rows int) error {
	procResizePseudoConsole.Call(c.hpc, coordVal(cols, rows))
	return nil
}
func (c *conptyShell) Wait() error {
	if c.hProc != 0 {
		procWaitForSingleObjectT.Call(uintptr(c.hProc), infinite)
	}
	c.reap() // process has exited — now it's safe to close the handles
	return nil
}

// Close terminates the shell and closes the pipes, but does NOT close the
// process/thread handles. Handle close happens in reap(), only after Wait()
// observes the exit — otherwise CloseHandle(hProc) would race the concurrent
// WaitForSingleObject(hProc) and, with handle-value reuse across rapid
// back-to-back sessions, crash the agent.
func (c *conptyShell) Close() error {
	c.termOnce.Do(func() {
		procClosePseudoConsole.Call(c.hpc) // signals the shell to exit and closes the pipes
		if c.hProc != 0 {
			procTerminateProcessT.Call(uintptr(c.hProc), 0)
		}
		_ = c.outFile.Close()
		_ = c.inFile.Close()
	})
	return nil
}

// reap closes the process + thread handles exactly once.
func (c *conptyShell) reap() {
	c.reapOnce.Do(func() {
		if c.hThread != 0 {
			procCloseHandleT.Call(uintptr(c.hThread))
		}
		if c.hProc != 0 {
			procCloseHandleT.Call(uintptr(c.hProc))
		}
	})
}

// convertToUTF8 converts bytes from the system ANSI code page (e.g., GBK on
// Chinese Windows) to UTF-8. If the data is already valid UTF-8, it is returned
// as-is. This handles ConPTY output that was emitted before chcp 65001 took
// effect, or programs that bypass the console code page.
func convertToUTF8(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	if utf8.Valid(data) {
		return data
	}
	out, ok := convertACPToUTF8(data, 0)
	if !ok {
		return data
	}
	return out
}

// convertACPToUTF8 runs MultiByteToWideChar(CP_ACP) → WideCharToMultiByte(CP_UTF8).
// When flags includes MB_ERR_INVALID_CHARS (8), incomplete/invalid sequences fail
// so the caller can peel trailing bytes for a streaming hold buffer.
func convertACPToUTF8(data []byte, flags uintptr) ([]byte, bool) {
	if len(data) == 0 {
		return data, true
	}
	n, _, _ := procMultiByteToWideChar.Call(
		cpACP, flags,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		0, 0,
	)
	if n == 0 {
		return nil, false
	}
	utf16Buf := make([]byte, int(n)*2)
	n, _, _ = procMultiByteToWideChar.Call(
		cpACP, flags,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		uintptr(unsafe.Pointer(&utf16Buf[0])), n,
	)
	if n == 0 {
		return nil, false
	}
	m, _, _ := procWideCharToMultiByte.Call(
		cpUTF8, 0,
		uintptr(unsafe.Pointer(&utf16Buf[0])), n,
		0, 0, 0, 0,
	)
	if m == 0 {
		return nil, false
	}
	utf8Buf := make([]byte, int(m))
	m, _, _ = procWideCharToMultiByte.Call(
		cpUTF8, 0,
		uintptr(unsafe.Pointer(&utf16Buf[0])), n,
		uintptr(unsafe.Pointer(&utf8Buf[0])), m,
		0, 0,
	)
	if m == 0 {
		return nil, false
	}
	return utf8Buf[:m], true
}
