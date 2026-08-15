//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const agentServiceName = "AiopsMonitorAgent"
const agentServiceDisplay = "AIOps Agent"
const agentServiceDesc = "AIOps 采集端（含远程桌面，支持锁屏/登录界面）。以 LocalSystem 运行。"

var (
	modAdvapi32Ctl = syscall.NewLazyDLL("advapi32.dll")

	procOpenSCManagerW                = modAdvapi32Ctl.NewProc("OpenSCManagerW")
	procCreateServiceW                = modAdvapi32Ctl.NewProc("CreateServiceW")
	procOpenServiceW                  = modAdvapi32Ctl.NewProc("OpenServiceW")
	procDeleteServiceCtl              = modAdvapi32Ctl.NewProc("DeleteService")
	procCloseServiceHandle            = modAdvapi32Ctl.NewProc("CloseServiceHandle")
	procStartServiceW                 = modAdvapi32Ctl.NewProc("StartServiceW")
	procControlServiceCtl             = modAdvapi32Ctl.NewProc("ControlService")
	procQueryServiceStatus            = modAdvapi32Ctl.NewProc("QueryServiceStatus")
	procChangeServiceConfigW          = modAdvapi32Ctl.NewProc("ChangeServiceConfigW")
	procChangeServiceConfig2W         = modAdvapi32Ctl.NewProc("ChangeServiceConfig2W")
	procStartServiceCtrlDispatcherW   = modAdvapi32Ctl.NewProc("StartServiceCtrlDispatcherW")
	procRegisterServiceCtrlHandlerExW = modAdvapi32Ctl.NewProc("RegisterServiceCtrlHandlerExW")
	procSetServiceStatus              = modAdvapi32Ctl.NewProc("SetServiceStatus")
)

const (
	scManagerAllAccess     = 0xF003F
	serviceAllAccess       = 0xF01FF
	serviceWin32OwnProcess = 0x00000010
	serviceAutoStart       = 0x00000002
	serviceErrorNormal     = 0x00000001

	serviceControlStop          = 0x00000001
	serviceControlShutdown      = 0x00000005
	serviceControlSessionChange = 0x0000000E
	serviceControlInterrogate   = 0x00000004

	serviceStopped      = 0x00000001
	serviceStartPending = 0x00000002
	serviceStopPending  = 0x00000003
	serviceRunning      = 0x00000004

	serviceAcceptStop          = 0x00000001
	serviceAcceptShutdown      = 0x00000004
	serviceAcceptSessionChange = 0x00000080

	serviceConfigDescription    = 1
	serviceConfigFailureActions = 2
	serviceNoChange             = 0xFFFFFFFF
	scActionRestart             = 1
)

type serviceStatus struct {
	ServiceType             uint32
	CurrentState            uint32
	ControlsAccepted        uint32
	Win32ExitCode           uint32
	ServiceSpecificExitCode uint32
	CheckPoint              uint32
	WaitHint                uint32
}

type serviceTableEntry struct {
	ServiceName *uint16
	ServiceProc uintptr
}

type serviceDescription struct {
	LpDescription *uint16
}

type scAction struct {
	Type  uint32
	Delay uint32 // milliseconds
}

type serviceFailureActions struct {
	ResetPeriod  uint32 // seconds; counter resets after this idle window
	RebootMsg    *uint16
	Command      *uint16
	ActionsCount uint32
	Actions      *scAction
}

// ---- install / uninstall ------------------------------------------------

func installAgentService(exePath, cfgPath string) error {
	scm, err := openSCM()
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(scm)

	binPath := fmt.Sprintf(`"%s" --service`, exePath)
	if cfgPath != "" {
		binPath += fmt.Sprintf(` --config "%s"`, cfgPath)
	}
	namePtr, _ := syscall.UTF16PtrFromString(agentServiceName)
	dispPtr, _ := syscall.UTF16PtrFromString(agentServiceDisplay)
	binPtr, _ := syscall.UTF16PtrFromString(binPath)

	svc, _, e := procCreateServiceW.Call(
		scm,
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(dispPtr)),
		uintptr(serviceAllAccess),
		uintptr(serviceWin32OwnProcess),
		uintptr(serviceAutoStart),
		uintptr(serviceErrorNormal),
		uintptr(unsafe.Pointer(binPtr)),
		0, 0, 0,
		0, // lpServiceStartName = NULL → LocalSystem
		0,
	)
	if svc == 0 {
		// ERROR_SERVICE_EXISTS = 1073 → reopen and update binPath (upgrade path).
		svc = reopenAndUpdate(scm, namePtr, binPtr)
		if svc == 0 {
			return fmt.Errorf("CreateService 失败: %v", e)
		}
	}
	defer procCloseServiceHandle.Call(svc)

	// Description (best-effort).
	if descPtr, e2 := syscall.UTF16PtrFromString(agentServiceDesc); e2 == nil {
		sd := serviceDescription{LpDescription: descPtr}
		_, _, _ = procChangeServiceConfig2W.Call(svc, uintptr(serviceConfigDescription), uintptr(unsafe.Pointer(&sd)))
	}

	// Crash recovery: auto-restart the service after 5s/5s/10s and reset the
	// failure counter after a day. Without this a crash leaves the host offline
	// until the next reboot — the exact "重启/崩溃后掉线" symptom.
	setServiceRecovery(svc)

	// Allow software Ctrl+Alt+Del (SendSAS) from the service / desktop worker.
	if err := ensureSoftwareSASPolicy(); err != nil {
		slog.Warn("启用 SoftwareSASGeneration 失败（锁屏 Ctrl+Alt+Del 可能无效）", "err", err)
	}

	// Start it now and wait until SCM reports Running. Returning success while
	// the service is still StartPending / immediately Stopped is how Win10/11
	// and locked-down Server installs looked "green" with an empty dashboard.
	if r, _, e3 := procStartServiceW.Call(svc, 0, 0); r == 0 {
		// ERROR_SERVICE_ALREADY_RUNNING = 1056 is fine.
		if en, ok := e3.(syscall.Errno); !ok || en != 1056 {
			return fmt.Errorf("StartService 失败: %v", e3)
		}
	}
	if err := waitServiceRunning(svc, 45*time.Second); err != nil {
		return fmt.Errorf("服务未能进入 Running：%w（查看安装目录 agent.log）", err)
	}
	return nil
}

// waitServiceRunning polls SCM until the service is Running, or fails.
func waitServiceRunning(svc uintptr, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var st serviceStatus
	for time.Now().Before(deadline) {
		r, _, e := procQueryServiceStatus.Call(svc, uintptr(unsafe.Pointer(&st)))
		if r == 0 {
			return fmt.Errorf("QueryServiceStatus: %v", e)
		}
		switch st.CurrentState {
		case serviceRunning:
			return nil
		case serviceStopped:
			return fmt.Errorf("服务启动后立即停止 (Win32ExitCode=%d ServiceSpecific=%d)", st.Win32ExitCode, st.ServiceSpecificExitCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("等待 Running 超时 (最后状态=%d)", st.CurrentState)
}

// reopenAndUpdate reopens an existing service and rewrites its binary path so an
// in-place upgrade (new exe location / config) takes effect without delete+create
// (which races with ERROR_SERVICE_MARKED_FOR_DELETE).
func reopenAndUpdate(scm uintptr, namePtr, binPtr *uint16) uintptr {
	svc, _, _ := procOpenServiceW.Call(scm, uintptr(unsafe.Pointer(namePtr)), uintptr(serviceAllAccess))
	if svc == 0 {
		return 0
	}
	_, _, _ = procChangeServiceConfigW.Call(
		svc,
		uintptr(serviceWin32OwnProcess),
		uintptr(serviceAutoStart),
		uintptr(serviceErrorNormal),
		uintptr(unsafe.Pointer(binPtr)),
		0, 0, 0, 0, 0, 0,
	)
	return svc
}

// setServiceRecovery configures SCM to restart the service on failure.
func setServiceRecovery(svc uintptr) {
	actions := [3]scAction{
		{Type: scActionRestart, Delay: 5000},
		{Type: scActionRestart, Delay: 5000},
		{Type: scActionRestart, Delay: 10000},
	}
	fa := serviceFailureActions{
		ResetPeriod:  86400,
		ActionsCount: uint32(len(actions)),
		Actions:      &actions[0],
	}
	_, _, _ = procChangeServiceConfig2W.Call(svc, uintptr(serviceConfigFailureActions), uintptr(unsafe.Pointer(&fa)))
}

func uninstallAgentService() error {
	scm, err := openSCM()
	if err != nil {
		return err
	}
	defer procCloseServiceHandle.Call(scm)
	namePtr, _ := syscall.UTF16PtrFromString(agentServiceName)
	svc, _, e := procOpenServiceW.Call(scm, uintptr(unsafe.Pointer(namePtr)), uintptr(serviceAllAccess))
	if svc == 0 {
		return fmt.Errorf("OpenService 失败(服务可能未安装): %v", e)
	}
	defer procCloseServiceHandle.Call(svc)

	// Try to stop first (ignore errors — may already be stopped).
	var st serviceStatus
	_, _, _ = procControlServiceCtl.Call(svc, uintptr(serviceControlStop), uintptr(unsafe.Pointer(&st)))
	time.Sleep(500 * time.Millisecond)

	if r, _, e2 := procDeleteServiceCtl.Call(svc); r == 0 {
		return fmt.Errorf("DeleteService 失败: %v", e2)
	}
	return nil
}

func openSCM() (uintptr, error) {
	scm, _, e := procOpenSCManagerW.Call(0, 0, uintptr(scManagerAllAccess))
	if scm == 0 {
		return 0, fmt.Errorf("OpenSCManager 失败(需管理员权限): %v", e)
	}
	return scm, nil
}

// ---- service runtime ----------------------------------------------------

var (
	svcAgent        *Agent
	svcCfgPath      string
	svcExePath      string
	svcStatusHandle uintptr
	svcCancel       context.CancelFunc
	svcStopOnce     sync.Once
	svcStopped      chan struct{}
	svcSessionNudge chan struct{}
	svcNamePtr      *uint16

	// keep callbacks alive for the process lifetime
	svcMainCallback    uintptr
	svcHandlerCallback uintptr
)

// runAgentAsService is the entry point when started by the SCM (--service).
func runAgentAsService(agent *Agent, cfgPath string) error {
	svcAgent = agent
	svcCfgPath = cfgPath
	if exe, err := os.Executable(); err == nil {
		svcExePath = exe
	}
	svcStopped = make(chan struct{})
	svcSessionNudge = make(chan struct{}, 4)
	svcNamePtr, _ = syscall.UTF16PtrFromString(agentServiceName)
	svcMainCallback = syscall.NewCallback(serviceMain)

	table := []serviceTableEntry{
		{ServiceName: svcNamePtr, ServiceProc: svcMainCallback},
		{ServiceName: nil, ServiceProc: 0},
	}
	if r, _, e := procStartServiceCtrlDispatcherW.Call(uintptr(unsafe.Pointer(&table[0]))); r == 0 {
		return fmt.Errorf("StartServiceCtrlDispatcher 失败(是否直接运行了 --service?): %v", e)
	}
	return nil
}

// serviceMain / serviceHandler are invoked by the SCM via syscall.NewCallback,
// which requires every parameter to be pointer-width (uintptr) — using uint32
// here would misread the stack on 64-bit Windows.
func serviceMain(argc uintptr, argv uintptr) uintptr {
	svcHandlerCallback = syscall.NewCallback(serviceHandler)
	h, _, _ := procRegisterServiceCtrlHandlerExW.Call(
		uintptr(unsafe.Pointer(svcNamePtr)),
		svcHandlerCallback,
		0,
	)
	if h == 0 {
		return 0
	}
	svcStatusHandle = h

	setSvcStatus(serviceStartPending, 0, 3000)

	ctx, cancel := context.WithCancel(context.Background())
	svcCancel = cancel

	// Delegate the desktop channel to a per-session worker; run everything else.
	svcAgent.desktopDisabled = true

	// Reconcile the canonical host id BEFORE spawning the desktop worker,
	// otherwise the supervisor races ahead and the worker permanently deskWaits
	// on a stale id (UI rings the new host → timeout).
	//
	// It must NOT run on the SCM start path though: it POSTs to the server with a
	// 30s client timeout, and a firewall that drops the SYN makes that block for
	// the full 30s. The SCM gives a service 30s to report SERVICE_RUNNING, so on
	// exactly the hosts that cannot reach the server the start timed out with
	// error 1053, crash-recovery restarted the service, and it looped forever —
	// no host in the dashboard and no console to see it happen. Report RUNNING
	// first and let the supervisor wait for the reconcile instead.
	reconciled := make(chan struct{})
	go func() {
		defer close(reconciled)
		svcAgent.reconcileIdentity()
	}()
	go func() {
		<-reconciled
		svcAgent.Run(ctx)
	}()
	go svcRunSupervisor(ctx.Done(), reconciled)
	go serveSASPipe(ctx.Done())

	setSvcStatus(serviceRunning, serviceAcceptStop|serviceAcceptShutdown|serviceAcceptSessionChange, 0)
	slog.Info("Agent 服务已启动(LocalSystem)")

	<-svcStopped
	setSvcStatus(serviceStopped, 0, 0)
	return 0
}

func serviceHandler(control uintptr, eventType uintptr, eventData uintptr, ctxPtr uintptr) uintptr {
	switch control {
	case serviceControlStop, serviceControlShutdown:
		setSvcStatus(serviceStopPending, 0, 5000)
		triggerSvcStop()
	case serviceControlSessionChange:
		select {
		case svcSessionNudge <- struct{}{}:
		default:
		}
	case serviceControlInterrogate:
		// Re-report current state; assume running.
		setSvcStatus(serviceRunning, serviceAcceptStop|serviceAcceptShutdown|serviceAcceptSessionChange, 0)
	}
	return 0 // NO_ERROR
}

func triggerSvcStop() {
	svcStopOnce.Do(func() {
		if svcCancel != nil {
			svcCancel()
		}
		close(svcStopped)
	})
}

func setSvcStatus(state, accepted, waitHintMs uint32) {
	st := serviceStatus{
		ServiceType:      serviceWin32OwnProcess,
		CurrentState:     state,
		ControlsAccepted: accepted,
		WaitHint:         waitHintMs,
	}
	_, _, _ = procSetServiceStatus.Call(svcStatusHandle, uintptr(unsafe.Pointer(&st)))
}

// svcRunSupervisor keeps exactly one desktop worker alive in the active console
// session, respawning it when the console session changes (logon / fast user
// switch / reconnect) or when the worker dies. Locking the screen does NOT
// change the console session — the worker stays up and follows the secure
// desktop from inside the session.
func svcRunSupervisor(stop <-chan struct{}, reconciled <-chan struct{}) {
	var worker *deskWorkerProc
	defer func() {
		if worker != nil {
			worker.kill()
		}
	}()
	// Spawning a worker before the canonical host id is known leaves it waiting on
	// an id the UI will never ring. Cap the wait so an unreachable server degrades
	// to "desktop starts late" instead of "desktop never starts".
	select {
	case <-reconciled:
	case <-stop:
		return
	case <-time.After(45 * time.Second):
		slog.Warn("规范身份同步超时，桌面 worker 先行启动")
	}
	reapLeftoverDesktopWorkers(svcExePath)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		// Target the session whose desktop is actually rendered (an RDP session
		// when someone is connected), not just the physical console — capturing a
		// disconnected console session produces black frames.
		active := activeUserSession()
		if active != invalidSession && active != 0 {
			if worker == nil || !worker.alive() || worker.session != active {
				if worker != nil {
					worker.kill()
					worker = nil
				}
				w, err := spawnDesktopWorker(svcExePath, svcCfgPath, active)
				if err != nil {
					slog.Warn("派生桌面 worker 失败", "err", err, "session", active)
				} else {
					worker = w
				}
			}
		} else if worker != nil {
			worker.kill()
			worker = nil
		}

		select {
		case <-stop:
			return
		case <-svcSessionNudge:
		case <-ticker.C:
		}
	}
}
