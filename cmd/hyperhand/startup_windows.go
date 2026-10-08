package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

func validateStartupTask(t *taskInfo, owner setupIdentity, hostExe string) error {
	if t == nil {
		return errors.New("HyperHand is not installed; run hyperhand.exe install first")
	}
	if t.userSID != owner.SID || t.runLevel != taskRunLevelLUA || t.triggers != 1 ||
		len(t.actions) != 1 || !strings.EqualFold(strings.Trim(t.actions[0].path, `"`), hostExe) || t.actions[0].args != "" {
		return errors.New("the installed logon task is not this user's HyperHand tray task")
	}
	return nil
}

// startupEnabled reads the actual task state; changing it never stops the tray or service.
func startupEnabled(owner setupIdentity, change *bool) (bool, error) {
	p, err := installPaths()
	if err != nil {
		return false, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ts, err := connectTaskScheduler()
	if err != nil {
		return false, err
	}
	defer ts.close()
	t, err := ts.task(logonTask)
	if err != nil {
		return false, err
	}
	if err := validateStartupTask(t, owner, p.hostExe); err != nil {
		return false, err
	}
	if change != nil {
		if err := ts.setEnabled(logonTask, *change); err != nil {
			return t.enabled, err
		}
		t, err = ts.task(logonTask)
		if err != nil {
			return false, err
		}
		if t == nil || t.enabled != *change {
			return false, errors.New("logon task startup verification failed")
		}
	}
	return t.enabled, nil
}

func changeStartup(enabled bool) error {
	owner, err := setupOwner(nil)
	if err != nil {
		return err
	}
	_, err = startupEnabled(owner, &enabled)
	if oleCode(err) == 0x80070005 && !windows.GetCurrentProcessToken().IsElevated() {
		state := "disable"
		if enabled {
			state = "enable"
		}
		return runAs("startup", state, "--owner-sid", owner.SID, "--owner-user", owner.User)
	}
	return err
}

func startupCommand(args []string) error {
	if len(args) == 0 || (args[0] != "enable" && args[0] != "disable") {
		return fmt.Errorf("usage: hyperhand.exe startup enable|disable")
	}
	owner, err := setupOwner(args[1:])
	if err != nil {
		return err
	}
	enabled := args[0] == "enable"
	_, err = startupEnabled(owner, &enabled)
	return err
}
