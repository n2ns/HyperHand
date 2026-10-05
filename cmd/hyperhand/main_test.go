package main

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRestartWaitsForPreviousProcess(t *testing.T) {
	if os.Getenv("HYPERHAND_TEST_PREVIOUS") == "1" {
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRestartWaitsForPreviousProcess$")
	cmd.Env = append(os.Environ(), "HYPERHAND_TEST_PREVIOUS=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := waitForPrevious(uint32(cmd.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		t.Fatal(err)
	}
	if code == 259 {
		t.Fatal("restart returned before the previous process exited")
	}
}
