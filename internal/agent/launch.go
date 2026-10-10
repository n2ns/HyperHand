package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

// launch starts a program detached from the request: not in the exec job object and with no captured stdio, so it
// outlives the request. Admin starts it elevated through the admin worker.
func launch(ctx context.Context, args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.LaunchArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	if a.Path == "" {
		return nil, nil, errors.New("launch requires a path")
	}
	if a.Admin {
		return launchAdminProcess(ctx, a)
	}
	pid, err := startDetached(a)
	if err != nil {
		return nil, nil, err
	}
	return proto.LaunchResult{PID: pid}, nil, nil
}

// startDetached starts a.Path with a.Args in a.Cwd and releases the process handle at once.
func startDetached(a proto.LaunchArgs) (uint32, error) {
	cmd := exec.Command(a.Path, a.Args...)
	cmd.Dir = a.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := uint32(cmd.Process.Pid)
	cmd.Process.Release()
	return pid, nil
}
