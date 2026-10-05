package agent

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startInJob assigns the shell before it can spawn children, so the job still
// owns its descendants after the shell exits. Failed setup reaps the suspended shell.
func startInJob(cmd *exec.Cmd, job windows.Handle) (err error) {
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open suspended shell: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fmt.Errorf("assign command job: %w", err)
	}

	// os/exec closes the initial thread handle; find that thread while the shell
	// is still suspended and has not executed any user code.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot shell thread: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(cmd.Process.Pid) {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("open shell thread: %w", err)
		}
		defer windows.CloseHandle(thread)
		if _, err := windows.ResumeThread(thread); err != nil {
			return fmt.Errorf("resume shell thread: %w", err)
		}
		return nil
	}
	return fmt.Errorf("find suspended shell thread: %w", err)
}
