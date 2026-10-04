package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// install creates the logon task for this hyperhand.exe (where it is now) and starts it.
func install() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return runAs("install")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	run("schtasks", "/End", "/TN", "HyperHand")
	if err := run("schtasks", "/Create", "/F", "/TN", "HyperHand", "/SC", "ONLOGON", "/RL", "HIGHEST", "/TR", `"`+exe+`"`); err != nil {
		return err
	}
	// schtasks defaults to a 72 h run limit and no start on battery; lift both.
	if err := run("powershell", "-NoProfile", "-Command",
		"Set-ScheduledTask -TaskName HyperHand -Settings (New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0 -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries)"); err != nil {
		return err
	}
	if err := run("schtasks", "/Run", "/TN", "HyperHand"); err != nil {
		return err
	}
	msgBox("HyperHand will start at logon from "+exe+" and is running now.\nMCP: http://127.0.0.1:8770/mcp", windows.MB_ICONINFORMATION)
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
