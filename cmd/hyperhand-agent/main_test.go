package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUninstallRunningProcess(t *testing.T) {
	if os.Getenv("HYPERHAND_UNINSTALL_TEST_PROCESS") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// The folder of a running agent can be deleted at once after its executable has been moved out.
func TestMoveOutLetsFolderBeDeleted(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "HyperHand ' & [测试]")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "hyperhand-agent.exe")
	copyTestBinary(t, exe)
	child := exec.Command(exe, "-test.run=^TestUninstallRunningProcess$")
	child.Env = append(os.Environ(), "HYPERHAND_UNINSTALL_TEST_PROCESS=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	time.Sleep(200 * time.Millisecond)
	if err := os.RemoveAll(dir); err == nil {
		t.Fatal("deleted the executable of a running process")
	}
	moved, err := moveOut(exe, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("folder not deleted after moving the executable out: %v", err)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatal(err)
	}
}

func copyTestBinary(t *testing.T, dst string) {
	t.Helper()
	in, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
