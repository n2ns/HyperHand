package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func launchAdmin(ctx context.Context, endpoint adminEndpoint) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := json.Marshal(endpoint)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "--admin-launch", base64.RawURLEncoding.EncodeToString(data))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("admin launcher: %w: %s", err, out)
	}
	return nil
}

// RunAdminHelper handles private subprocess roles before the agent's tray and
// single-instance mutex are initialized. No command is passed on the command line.
func RunAdminHelper(args []string) (bool, error) {
	if len(args) == 0 || (args[0] != "--admin-launch" && args[0] != "--admin-worker") {
		return false, nil
	}
	if len(args) != 2 {
		return true, fmt.Errorf("invalid admin helper arguments")
	}
	data, err := base64.RawURLEncoding.DecodeString(args[1])
	if err != nil {
		return true, err
	}
	var endpoint adminEndpoint
	if err := json.Unmarshal(data, &endpoint); err != nil {
		return true, err
	}
	if args[0] == "--admin-worker" {
		if !windows.GetCurrentProcessToken().IsElevated() {
			return true, fmt.Errorf("admin worker is not elevated")
		}
		return true, runAdminWorker(endpoint)
	}
	exe, err := os.Executable()
	if err != nil {
		return true, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err != nil {
		return true, err
	}
	defer windows.CoUninitialize()
	h, err := runElevated(exe, "--admin-worker "+args[1])
	if err == nil {
		windows.CloseHandle(h)
	}
	return true, err
}

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

// Called only by the disposable launcher; UAC may block this syscall indefinitely.
func runElevated(file, params string) (windows.Handle, error) {
	sei := shellExecuteInfo{
		fMask:        0x40 | 0x100 | 0x400, // NOCLOSEPROCESS | NOASYNC | FLAG_NO_UI
		lpVerb:       windows.StringToUTF16Ptr("runas"),
		lpFile:       windows.StringToUTF16Ptr(file),
		lpParameters: windows.StringToUTF16Ptr(params),
		nShow:        windows.SW_HIDE,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	proc := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")
	if r, _, err := proc.Call(uintptr(unsafe.Pointer(&sei))); r == 0 {
		return 0, fmt.Errorf("ShellExecuteEx runas: %w", err)
	}
	if sei.hProcess == 0 {
		return 0, fmt.Errorf("ShellExecuteEx runas: no process handle")
	}
	return sei.hProcess, nil
}
