package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"hyperhand/internal/proto"
)

// uninstall undoes install and the tray's registration: stops the tray, deletes the logon task,
// the Hyper-V socket service key and the log folder. The exe itself is left in place.
func uninstall() {
	if !windows.GetCurrentProcessToken().IsElevated() {
		if err := runAs("uninstall"); err != nil {
			msgBox("HyperHand uninstall: elevation failed:\n"+err.Error(), windows.MB_ICONERROR)
		}
		return
	}
	var done, failed []string
	step := func(what string, err error) {
		if err != nil {
			failed = append(failed, what+": "+err.Error())
		} else {
			done = append(done, what)
		}
	}

	// Stop the tray (task instance and any other hyperhand.exe) and wait for it to exit.
	run("schtasks", "/End", "/TN", "HyperHand")
	pid := fmt.Sprint(os.Getpid())
	run("taskkill", "/F", "/IM", "hyperhand.exe", "/FI", "PID ne "+pid)
	var err error
	for i := 0; ; i++ {
		out, _ := output("tasklist", "/NH", "/FI", "IMAGENAME eq hyperhand.exe", "/FI", "PID ne "+pid)
		if !strings.Contains(strings.ToLower(out), "hyperhand.exe") {
			break
		}
		if i >= 25 {
			err = errors.New("still running after 5 s")
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	step("stopped running HyperHand", err)

	if run("schtasks", "/Query", "/TN", "HyperHand") == nil {
		step("scheduled task HyperHand", run("schtasks", "/Delete", "/F", "/TN", "HyperHand"))
	}

	key := `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\` + proto.ServiceID
	if err := registry.DeleteKey(registry.LOCAL_MACHINE, key); !errors.Is(err, registry.ErrNotExist) {
		step(`HKLM\`+key, err)
	}

	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "HyperHand")
	if _, err := os.Stat(dir); err == nil {
		// The stopping tray may still hold its log file, or append a last line (recreating the folder) while it
		// exits: delete, wait a moment, and repeat until the folder stays gone (up to 5 s).
		var err error
		for i := 0; i < 10; i++ {
			err = os.RemoveAll(dir)
			time.Sleep(500 * time.Millisecond)
			if _, statErr := os.Stat(dir); err == nil && os.IsNotExist(statErr) {
				break
			}
		}
		step(dir, err)
	}

	text := "HyperHand uninstalled.\n\nRemoved:\n  " + strings.Join(done, "\n  ")
	flags := uint32(windows.MB_ICONINFORMATION)
	if len(failed) > 0 {
		text += "\n\nFailed:\n  " + strings.Join(failed, "\n  ")
		flags = windows.MB_ICONWARNING
	}
	msgBox(text, flags)
}

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
