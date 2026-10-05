package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupWaitingProcess(t *testing.T) {
	if os.Getenv("HYPERHAND_CLEANUP_TEST_PROCESS") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestCleanupWaitsForExit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "HyperHand ' & [测试]")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(file, []byte("keep until exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestCleanupWaitingProcess$")
	child.Env = append(os.Environ(), "HYPERHAND_CLEANUP_TEST_PROCESS=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	cleanup := cleanupCommand(dir, child.Process.Pid)
	if err := cleanup.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup.Process.Kill() })
	finished := make(chan error, 1)
	go func() { finished <- cleanup.Wait() }()
	select {
	case err := <-finished:
		t.Fatalf("cleanup exited before the process: %v", err)
	case <-time.After(3 * time.Second):
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("file removed while process was alive: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("cleanup failed after process exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cleanup did not finish after process exit")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory remains after process exit: %v", err)
	}
}

func TestCleanupAlreadyExitedProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "HyperHand")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestCleanupWaitingProcess$")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	cleanup := cleanupCommand(dir, child.Process.Pid)
	if out, err := cleanup.CombinedOutput(); err != nil {
		t.Fatalf("cleanup after process exit failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
}
