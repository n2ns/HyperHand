package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"hyperhand/internal/broker"
	"hyperhand/internal/hyperv"
)

// consoleTask is registered by install: it runs vmconnect.exe localhost "<VM>" with the owner's full token, because
// VMConnect needs Hyper-V rights that the owner's filtered (non-elevated) token does not have.
const consoleTask = "HyperHand Console"

// openConsole brings vm's open VMConnect window to the front, or else opens VMConnect for vm through consoleTask,
// without a UAC prompt. A second VMConnect for the same VM would ask to take over the console connection.
func openConsole(vm string) error {
	vms, err := (&broker.Client{}).ListVMs()
	if err != nil {
		return err
	}
	if err := checkConsoleVM(vm, vms); err != nil {
		return err
	}
	if focusConsole(vm) {
		return nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 { // S_FALSE = already initialized
			return err
		}
	}
	defer ole.CoUninitialize()
	unk, err := oleutil.CreateObject("Schedule.Service")
	if err != nil {
		return err
	}
	defer unk.Release()
	svc, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer svc.Release()
	if _, err := oleutil.CallMethod(svc, "Connect"); err != nil {
		return err
	}
	folder, err := oleutil.CallMethod(svc, "GetFolder", `\`)
	if err != nil {
		return err
	}
	defer folder.Clear()
	task, err := oleutil.CallMethod(folder.ToIDispatch(), "GetTask", consoleTask)
	if err != nil {
		return fmt.Errorf("the %q task is missing; run hyperhand.exe install again: %w", consoleTask, err)
	}
	defer task.Clear()
	run, err := oleutil.CallMethod(task.ToIDispatch(), "Run", vm)
	if err != nil {
		return err
	}
	run.Clear()
	return nil
}

// checkConsoleVM allows only the exact name of an existing VM, which the task passes to vmconnect.exe inside quotes
// (a trailing backslash would escape the closing quote). It guards the tray's own requests only: any process of the
// user can run the task with any argument.
func checkConsoleVM(vm string, vms []hyperv.VM) error {
	if vm == "" || strings.ContainsAny(vm, "\"\r\n") || strings.HasSuffix(vm, `\`) {
		return fmt.Errorf("invalid VM name %q", vm)
	}
	for _, v := range vms {
		if v.Name == vm {
			return nil
		}
	}
	return fmt.Errorf("no VM named %q", vm)
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	pEnumWindows         = user32.NewProc("EnumWindows")
	pIsWindowVisible     = user32.NewProc("IsWindowVisible")
	pIsIconic            = user32.NewProc("IsIconic")
	pGetWindowTextW      = user32.NewProc("GetWindowTextW")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pSwitchToThisWindow  = user32.NewProc("SwitchToThisWindow")
	enumMu               sync.Mutex // the EnumWindows callback below is shared
	enumFound            []windows.HWND
	enumCallback         = syscall.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		enumFound = append(enumFound, h)
		return 1
	})
)

// focusConsole brings an open VMConnect window for vm to the front and reports whether it found one. VMConnect runs
// elevated, so this process cannot show or restore it with ShowWindow (UIPI); SetForegroundWindow and
// SwitchToThisWindow still work.
func focusConsole(vm string) bool {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return false
	}
	vmconnect := filepath.Join(system, "vmconnect.exe")
	enumMu.Lock()
	enumFound = nil
	pEnumWindows.Call(enumCallback, 0)
	found := enumFound
	enumMu.Unlock()
	for _, h := range found {
		if r, _, _ := pIsWindowVisible.Call(uintptr(h)); r == 0 {
			continue
		}
		if !strings.EqualFold(windowProcessImage(h), vmconnect) {
			continue
		}
		if name, ok := consoleTitleVM(windowText(h)); !ok || name != vm {
			continue
		}
		if r, _, _ := pIsIconic.Call(uintptr(h)); r != 0 {
			pSwitchToThisWindow.Call(uintptr(h), 1)
		}
		pSetForegroundWindow.Call(uintptr(h))
		return true
	}
	return false
}

func windowText(h windows.HWND) string {
	buf := make([]uint16, 512)
	n, _, _ := pGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func windowProcessImage(h windows.HWND) string {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil {
		return ""
	}
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(p)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// consoleTitleVM extracts the VM name from the title of a VMConnect window opened for "localhost", such as
// "Win10 on localhost - Virtual Machine Connection" or the localized "localhost 上的 Win10 - 虚拟机连接": before the
// last " - ", the host and the one word joining it to the VM name are dropped. A title in another layout gives false,
// and the console is then opened anew rather than another VM's window being taken for it.
func consoleTitleVM(title string) (string, bool) {
	const host = "localhost"
	i := strings.LastIndex(title, " - ")
	if i < 0 {
		return "", false
	}
	p := title[:i]
	if rest, ok := strings.CutPrefix(p, host+" "); ok { // "localhost 上的 Win10"
		if _, name, ok := strings.Cut(rest, " "); ok && name != "" {
			return name, true
		}
	}
	if rest, ok := strings.CutSuffix(p, " "+host); ok { // "Win10 on localhost"
		if j := strings.LastIndex(rest, " "); j > 0 {
			return rest[:j], true
		}
	}
	return "", false
}

// enhancedSessionAllowed reports whether the host allows enhanced session mode. VMConnect then may connect in an
// enhanced session, which moves the guest user's session off the console that HyperHand's screenshots and input use.
func enhancedSessionAllowed() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("EnhancedMode")
	return err == nil && v != 0
}

const enhancedSessionWarning = "This host allows enhanced session mode. If VMConnect opens an enhanced session, the guest session moves to it " +
	"and HyperHand's screenshots and input reach the lock screen instead. Use View > Enhanced Session to switch it off in VMConnect, " +
	"or turn off \"Allow enhanced session mode\" in the Hyper-V host settings."

// consoleSettings is the per-user list of VMs whose console opens when vm_start starts them.
type consoleSettings struct {
	mu   sync.Mutex
	path string
	vms  map[string]bool
}

type settingsFile struct {
	OpenConsoleOnStart []string `json:"open_console_on_start"`
}

func loadConsoleSettings(path string) *consoleSettings {
	s := &consoleSettings{path: path, vms: map[string]bool{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Print("settings: ", err)
		}
		return s
	}
	var f settingsFile
	if err := json.Unmarshal(b, &f); err != nil {
		log.Print("settings: ", err)
		return s
	}
	for _, vm := range f.OpenConsoleOnStart {
		s.vms[vm] = true
	}
	return s
}

func (s *consoleSettings) onStart(vm string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vms[vm]
}

// setOnStart changes and saves the setting for vm.
func (s *consoleSettings) setOnStart(vm string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.vms[vm] = true
	} else {
		delete(s.vms, vm)
	}
	f := settingsFile{OpenConsoleOnStart: []string{}}
	for v := range s.vms {
		f.OpenConsoleOnStart = append(f.OpenConsoleOnStart, v)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o644)
}
