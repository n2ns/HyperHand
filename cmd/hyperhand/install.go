package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// install copies hyperhand.exe (and hyperhand-agent.exe if present) to dir, creates the logon task and starts it.
func install(dir string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return runAs("install")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Stop a running copy so its exe can be replaced.
	run("schtasks", "/End", "/TN", "HyperHand")
	run("taskkill", "/F", "/IM", "hyperhand.exe", "/FI", fmt.Sprintf("PID ne %d", os.Getpid()))
	time.Sleep(time.Second)

	exe := filepath.Join(dir, "hyperhand.exe")
	if err := copyFile(self, exe); err != nil {
		return err
	}
	if agent := filepath.Join(filepath.Dir(self), "hyperhand-agent.exe"); fileExists(agent) {
		if err := copyFile(agent, filepath.Join(dir, "hyperhand-agent.exe")); err != nil {
			return err
		}
	}
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
	msgBox("HyperHand installed to "+dir+" and started.\nMCP: http://127.0.0.1:8770/mcp", windows.MB_ICONINFORMATION)
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

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func copyFile(src, dst string) error {
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
