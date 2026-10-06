package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"

	"hyperhand/internal/proto"
)

const controlsHelperRole = "--hyperhand-controls-helper"
const controlsOutputLimit = 16 << 20

func controlsArgs(a proto.ControlsArgs) (proto.ControlsArgs, error) {
	if a.Handle == 0 || a.PID == 0 {
		return a, errors.New("controls require a nonzero window handle and PID")
	}
	if a.MaxDepth == 0 {
		a.MaxDepth = 4
	}
	if a.MaxNodes == 0 {
		a.MaxNodes = 200
	}
	if a.MaxDepth < 1 || a.MaxDepth > 10 {
		return a, errors.New("max_depth must be between 1 and 10")
	}
	if a.MaxNodes < 1 || a.MaxNodes > 1000 {
		return a, errors.New("max_nodes must be between 1 and 1000")
	}
	return a, nil
}

func listControls(ctx context.Context, args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.ControlsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, nil, err
	}
	a, err := controlsArgs(a)
	if err != nil {
		return nil, nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	opctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := runControlsCommand(opctx, exec.CommandContext(opctx, exe, controlsHelperRole), a)
	return r, nil, err
}

type controlsReply struct {
	Result proto.ControlsResult `json:"result"`
	Error  string               `json:"error,omitempty"`
}

// A broken provider can hang even during COM Release. Only the disposable process calls UIA.
func runControlsCommand(ctx context.Context, cmd *exec.Cmd, a proto.ControlsArgs) (proto.ControlsResult, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return proto.ControlsResult{}, err
	}
	cmd.Stdin = bytes.NewReader(data)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.WaitDelay = time.Second
	var out controlsBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return proto.ControlsResult{}, fmt.Errorf("UI Automation interrupted: %w", ctx.Err())
		}
		return proto.ControlsResult{}, fmt.Errorf("UI Automation helper: %w", err)
	}
	var reply controlsReply
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		return proto.ControlsResult{}, fmt.Errorf("UI Automation helper response: %w", err)
	}
	if reply.Error != "" {
		return proto.ControlsResult{}, errors.New(reply.Error)
	}
	return reply.Result, nil
}

type controlsBuffer struct{ buffer bytes.Buffer }

func (b *controlsBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *controlsBuffer) Write(p []byte) (int, error) {
	if len(p) > controlsOutputLimit-b.buffer.Len() {
		return 0, errors.New("UI Automation output exceeds limit")
	}
	return b.buffer.Write(p)
}

// RunControlsHelper is a private process role, handled before the tray and its single-instance mutex.
func RunControlsHelper(args []string) (bool, error) {
	if len(args) == 0 || args[0] != controlsHelperRole {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("invalid controls helper arguments")
	}
	var a proto.ControlsArgs
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 4096)).Decode(&a); err != nil {
		return true, err
	}
	a, err := controlsArgs(a)
	var result proto.ControlsResult
	if err == nil {
		result, err = collectControls(a)
	}
	reply := controlsReply{Result: result}
	if err != nil {
		reply.Error = err.Error()
	}
	return true, json.NewEncoder(os.Stdout).Encode(reply)
}

type controlElement interface {
	info() (proto.ControlInfo, bool, bool, error) // node, password, text truncated
	first() (controlElement, error)
	next() (controlElement, error)
	release()
}

func walkControls(root controlElement, a proto.ControlsArgs) (proto.ControlsResult, error) {
	r := proto.ControlsResult{Nodes: []proto.ControlInfo{}}
	truncate := func(reason string) {
		r.Truncated = true
		if !slices.Contains(r.Truncation, reason) {
			r.Truncation = append(r.Truncation, reason)
		}
	}
	var visit func(controlElement, int, int) error
	visit = func(e controlElement, parent, depth int) error {
		n, password, textCut, err := e.info()
		if err != nil {
			return err
		}
		if textCut {
			truncate("text_length")
		}
		n.Index, n.Parent, n.Depth = len(r.Nodes), parent, depth
		r.Nodes = append(r.Nodes, n)
		if password {
			truncate("password_subtree")
			return nil
		}
		child, err := e.first()
		if err != nil {
			return err
		}
		if child == nil {
			return nil
		}
		if depth == a.MaxDepth {
			child.release()
			truncate("max_depth")
			return nil
		}
		for child != nil {
			if len(r.Nodes) == a.MaxNodes {
				child.release()
				truncate("max_nodes")
				return nil
			}
			if err := visit(child, n.Index, depth+1); err != nil {
				child.release()
				return err
			}
			next, err := child.next()
			child.release()
			if err != nil {
				return err
			}
			child = next
		}
		return nil
	}
	if err := visit(root, -1, 0); err != nil {
		return proto.ControlsResult{}, err
	}
	return r, nil
}
