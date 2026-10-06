package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// startCopy starts a copy of the test binary as dir\hyperhand-agent.exe, which sleeps.
func startCopy(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	return exe
}

// %TEMP% on another volume than the agent's folder: the executable cannot be renamed there.
func TestRemoveFoldersTempOnOtherVolume(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	if strings.EqualFold(filepath.VolumeName(wd), filepath.VolumeName(temp)) {
		t.Skip("the package directory and the temporary directory are on the same volume")
	}
	base, err := os.MkdirTemp(wd, "uninstall-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir := filepath.Join(base, "HyperHand")
	exe := startCopy(t, dir)
	t.Chdir(wd) // restores the working directory, which removeFolders changes, before the folders are removed
	if _, failed := removeFolders(exe, temp, dir); len(failed) != 0 {
		t.Fatalf("failed: %v", failed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder remains: %v", err)
	}
}

// If the executable cannot be moved, the rest of its folder is still removed.
func TestRemoveFoldersMoveFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "HyperHand")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "sub", "data.txt")
	os.WriteFile(other, []byte("x"), 0o600)
	missing := filepath.Join(dir, "missing.exe") // renaming it fails
	wd, _ := os.Getwd()
	t.Chdir(wd) // restores the working directory, which removeFolders changes
	if _, failed := removeFolders(missing, t.TempDir(), dir); len(failed) == 0 {
		t.Fatal("no failure reported")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("other files kept: %v", err)
	}
}

// The uninstaller may have been started with its working directory in the folder it deletes.
func TestRemoveFoldersWorkingDirectoryInside(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "HyperHand")
	exe := startCopy(t, dir)
	t.Chdir(dir)
	if _, failed := removeFolders(exe, base, dir); len(failed) != 0 {
		t.Fatalf("failed: %v", failed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder remains: %v", err)
	}
}
