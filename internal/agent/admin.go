package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

type adminEndpoint struct {
	Pipe          string
	ParentPID     uint32
	ParentCreated windows.Filetime
}

type adminRequest struct {
	Args     proto.ExecArgs
	Deadline time.Time
}

func execAdmin(ctx context.Context, a proto.ExecArgs) (any, []byte, error) {
	return execAdminWithLauncher(ctx, a, launchAdmin)
}

// The launcher runs only ShellExecuteEx, in a disposable ordinary process. The
// elevated worker receives the command only over this live, single-use pipe.
func execAdminWithLauncher(ctx context.Context, a proto.ExecArgs, launch func(context.Context, adminEndpoint) error) (any, []byte, error) {
	opctx, cancel := context.WithTimeout(ctx, timeout(a.TimeoutMs))
	defer cancel()
	if a.Shell != "" && a.Shell != "powershell" && a.Shell != "cmd" {
		return nil, nil, fmt.Errorf("unknown shell %q", a.Shell)
	}
	if err := opctx.Err(); err != nil {
		return adminInterrupted(ctx, err)
	}
	if a.Cwd == "" {
		var err error
		a.Cwd, err = os.Getwd()
		if err != nil {
			return nil, nil, err
		}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	endpoint := adminEndpoint{Pipe: `\\.\pipe\hyperhand-admin-` + rand.Text(), ParentPID: uint32(os.Getpid())}
	var exited, kernel, userTime windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &endpoint.ParentCreated, &exited, &kernel, &userTime); err != nil {
		return nil, nil, err
	}
	listener, err := winio.ListenPipe(endpoint.Pipe, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;BA)(A;;GA;;;SY)",
	})
	if err != nil {
		return nil, nil, err
	}
	var conn net.Conn
	var acceptErr error
	accepted := make(chan struct{})
	go func() { conn, acceptErr = listener.Accept(); close(accepted) }()
	stopAccept := context.AfterFunc(opctx, func() { listener.Close() })
	defer func() {
		stopAccept()
		listener.Close()
		<-accepted
		if conn != nil {
			conn.Close()
		}
	}()
	launchCtx, stopLaunch := context.WithCancel(opctx)
	var launchErr error
	launched := make(chan struct{})
	go func() { launchErr = launch(launchCtx, endpoint); close(launched) }()
	defer func() { stopLaunch(); <-launched }()
	launchDone := launched
	for {
		select {
		case <-opctx.Done():
			return adminInterrupted(ctx, opctx.Err())
		case <-launchDone:
			if opctx.Err() != nil {
				return adminInterrupted(ctx, opctx.Err())
			}
			if launchErr != nil {
				return nil, nil, launchErr
			}
			launchDone = nil // runas succeeded; the worker may still be connecting
		case <-accepted:
			if opctx.Err() != nil {
				return adminInterrupted(ctx, opctx.Err())
			}
			if acceptErr != nil {
				return nil, nil, acceptErr
			}
			deadline, _ := opctx.Deadline()
			// The worker enforces the original deadline. Allow it to reap its job
			// and return captured output; caller cancellation closes the pipe immediately.
			conn.SetDeadline(deadline.Add(5 * time.Second))
			stopIO := context.AfterFunc(ctx, func() { conn.Close() })
			defer stopIO()
			a.Admin = false
			if err := json.NewEncoder(conn).Encode(adminRequest{a, deadline}); err != nil {
				return adminInterrupted(ctx, err)
			}
			var reply struct {
				Result proto.ExecResult
				Error  string
			}
			if err := json.NewDecoder(conn).Decode(&reply); err != nil {
				if opctx.Err() != nil {
					return adminInterrupted(ctx, opctx.Err())
				}
				return adminInterrupted(ctx, err)
			}
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if reply.Error != "" {
				return nil, nil, errors.New(reply.Error)
			}
			return reply.Result, nil, nil
		}
	}
}

func adminInterrupted(ctx context.Context, err error) (any, []byte, error) {
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return proto.ExecResult{ExitCode: -1, TimedOut: true}, nil, nil
	}
	return nil, nil, err
}

// runAdminWorker executes one request. Checking both PID and creation time stops
// a late UAC approval from trusting a replacement pipe server after its parent exits.
func runAdminWorker(endpoint adminEndpoint) error {
	parent, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, endpoint.ParentPID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	var created, exited, kernel, userTime windows.Filetime
	if err := windows.GetProcessTimes(parent, &created, &exited, &kernel, &userTime); err != nil {
		return err
	}
	state, err := windows.WaitForSingleObject(parent, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) || created != endpoint.ParentCreated {
		return errors.New("admin request owner is no longer running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, endpoint.Pipe)
	if err != nil {
		return err
	}
	defer conn.Close()
	var serverPID uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(conn.(interface{ Fd() uintptr }).Fd()), &serverPID); err != nil {
		return err
	}
	if serverPID != endpoint.ParentPID {
		return errors.New("admin pipe server is not the request owner")
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var request adminRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Time{})
	execCtx, stopExec := context.WithDeadline(context.Background(), request.Deadline)
	defer stopExec()
	watchDone := make(chan struct{})
	go func() {
		var b [1]byte
		conn.Read(b[:]) // EOF (or any further input) revokes the request
		stopExec()
		close(watchDone)
	}()
	defer func() { conn.Close(); <-watchDone }()
	request.Args.Admin = false
	result, _, execErr := execAdminCommand(execCtx, request.Args)
	reply := struct {
		Result proto.ExecResult
		Error  string
	}{}
	if execErr != nil {
		reply.Error = execErr.Error()
	} else {
		reply.Result = result.(proto.ExecResult)
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return json.NewEncoder(conn).Encode(reply)
}

func execAdminCommand(ctx context.Context, a proto.ExecArgs) (any, []byte, error) {
	if a.Shell != "" && a.Shell != "powershell" {
		return execCommand(ctx, a, "")
	}
	f, err := os.CreateTemp("", "hh-admin-*.ps1")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(f.Name())
	// -File needs a BOM for UTF-8 and explicit propagation of the last statement's failure.
	_, err = f.WriteString("\xEF\xBB\xBF[Console]::OutputEncoding=[Text.Encoding]::UTF8\r\n" + a.Command + "\r\nif (-not $?) { exit 1 }\r\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, nil, err
	}
	return execCommand(ctx, a, f.Name())
}
