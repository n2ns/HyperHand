package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestValidateStartupTask(t *testing.T) {
	owner := setupIdentity{SID: "S-1-5-21-123", User: `PC\User`}
	const exe = `C:\Program Files\HyperHand\hyperhand.exe`
	valid := taskInfo{userSID: owner.SID, runLevel: taskRunLevelLUA, triggers: 1, actions: []taskAction{{path: exe}}}
	for _, enabled := range []bool{true, false} {
		valid.enabled = enabled
		if err := validateStartupTask(&valid, owner, exe); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateStartupTask(nil, owner, exe); err == nil {
		t.Fatal("accepted missing installation")
	}
	for name, change := range map[string]func(*taskInfo){
		"other owner": func(tk *taskInfo) { tk.userSID = "S-1-5-21-456" },
		"elevated":    func(tk *taskInfo) { tk.runLevel = 1 },
		"no trigger":  func(tk *taskInfo) { tk.triggers = 0 },
		"no action":   func(tk *taskInfo) { tk.actions = nil },
		"other exe":   func(tk *taskInfo) { tk.actions = []taskAction{{path: `C:\Other\hyperhand.exe`}} },
		"arguments":   func(tk *taskInfo) { tk.actions = []taskAction{{path: exe, args: "service"}} },
	} {
		t.Run(name, func(t *testing.T) {
			tk := valid
			change(&tk)
			if err := validateStartupTask(&tk, owner, exe); err == nil {
				t.Fatal("accepted unexpected task")
			}
		})
	}
}

// Use a disposable task with no triggers; never modify the installed HyperHand task.
func TestTaskEnabledRoundTrip(t *testing.T) {
	owner, err := setupOwner(nil)
	if err != nil {
		t.Fatal(err)
	}
	withTaskScheduler(t, func(ts *taskScheduler) {
		name := fmt.Sprintf("HyperHand startup test %d", os.Getpid())
		xml := strings.Replace(consoleTaskXML(`C:\Windows\System32\notepad.exe`, owner), "HighestAvailable", "LeastPrivilege", 1)
		if err := ts.register(name, xml); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := ts.delete(name); err != nil {
				t.Error(err)
			}
		}()
		for _, enabled := range []bool{false, true, false} {
			if err := ts.setEnabled(name, enabled); err != nil {
				t.Fatal(err)
			}
			tk, err := ts.task(name)
			if err != nil || tk == nil {
				t.Fatalf("read task: %v", err)
			}
			if tk.enabled != enabled {
				t.Fatalf("enabled = %v, want %v", tk.enabled, enabled)
			}
		}
	})
}

func TestStartupReadOnly(t *testing.T) {
	owner, err := setupOwner(nil)
	if err != nil {
		t.Fatal(err)
	}
	withTaskScheduler(t, func(ts *taskScheduler) {
		tk, err := ts.task(logonTask)
		if err != nil {
			t.Fatal(err)
		}
		if tk == nil {
			t.Skip("HyperHand is not installed")
		}
		enabled, err := startupEnabled(owner, nil)
		if err != nil || enabled != tk.enabled {
			t.Fatalf("startup state = %v, %v; task enabled = %v", enabled, err, tk.enabled)
		}
	})
}
