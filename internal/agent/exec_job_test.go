package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

func TestStartInJobFailureReapsShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
		CmdLine:       fmt.Sprintf(`cmd.exe /d /s /c "echo unexpected >"%s""`, marker),
	}
	if err := startInJob(cmd, 0); err == nil { // invalid job, after starting the suspended shell
		t.Fatal("assignment to invalid job succeeded")
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatal("suspended shell was not reaped")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell ran before job assignment: %v", err)
	}
}

// Invoked in a child process which outlives the cmd.exe that launched it.
func TestExecTreeChild(t *testing.T) {
	dir := os.Getenv("HYPERHAND_EXEC_TEST_DIR")
	if dir == "" {
		return
	}
	// Do not signal readiness until the launching shell is gone: cancellation
	// must exercise an orphaned child, not merely kill a still-live parent.
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(os.Getppid()))
	if err == nil {
		state, waitErr := windows.WaitForSingleObject(parent, 3000)
		windows.CloseHandle(parent)
		if waitErr != nil || state != windows.WAIT_OBJECT_0 {
			os.Exit(4)
		}
	} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) { // already exited
		os.Exit(5)
	}
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		os.Exit(2)
	}
	time.Sleep(3 * time.Second)
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("child survived"), 0o644); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestExecExitedShell(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"timeout", "cancel", "background"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HYPERHAND_EXEC_TEST_DIR", dir)
			command := fmt.Sprintf(`start "" /b "%s" -test.run=TestExecTreeChild`, exe)
			if mode == "background" {
				command += " >nul 2>&1"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeoutMs := 10000
			if mode == "timeout" {
				timeoutMs = 1500
			}
			type answer struct {
				result any
				err    error
			}
			done := make(chan answer, 1)
			go func() {
				r, _, err := execOp(ctx, mustJSON(proto.ExecArgs{Shell: "cmd", Command: command, TimeoutMs: timeoutMs}), nil)
				done <- answer{r, err}
			}()
			var pid int
			for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
				if data, err := os.ReadFile(filepath.Join(dir, "ready")); err == nil {
					pid, _ = strconv.Atoi(string(data))
					if pid != 0 {
						break
					}
				}
			}
			if pid == 0 {
				t.Fatal("child did not start")
			}
			h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(h)
			defer windows.TerminateProcess(h, 1) // only this test's child, even on failure
			if mode == "cancel" {
				cancel()
			}
			select {
			case a := <-done:
				if mode == "cancel" {
					if !errors.Is(a.err, context.Canceled) {
						t.Fatalf("cancel: %v", a.err)
					}
				} else if a.err != nil {
					t.Fatal(a.err)
				} else if r := a.result.(proto.ExecResult); r.TimedOut != (mode == "timeout") || (mode == "background" && r.ExitCode != 0) {
					t.Fatalf("result: %+v", r)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("exec did not return")
			}
			if state, err := windows.WaitForSingleObject(h, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("child still running: state=%d, err=%v", state, err)
			}
			_, markerErr := os.Stat(filepath.Join(dir, "marker"))
			if mode == "background" {
				if markerErr != nil {
					t.Fatalf("normal background child was terminated: %v", markerErr)
				}
			} else if !os.IsNotExist(markerErr) {
				t.Fatalf("child wrote after %s: %v", mode, markerErr)
			}
		})
	}
}
