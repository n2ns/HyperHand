package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// fakeAgentBackend serves one running VM whose agent answers with respond; checkpoint operations are recorded by the
// embedded vmToolsBackend.
type fakeAgentBackend struct {
	*vmToolsBackend
	respond func(req proto.Request) (any, error)
}

func newFakeAgentBackend(respond func(req proto.Request) (any, error)) *fakeAgentBackend {
	return &fakeAgentBackend{
		vmToolsBackend: &vmToolsBackend{vms: []hyperv.VM{{Name: "Win10", ID: "id-a", State: "Running"}}},
		respond:        respond,
	}
}

func (b *fakeAgentBackend) Dial(_ context.Context, id string) (net.Conn, error) {
	client, guest := net.Pipe()
	go func() {
		defer guest.Close()
		for {
			var req proto.Request
			if _, err := proto.ReadFrame(guest, &req); err != nil {
				return
			}
			result, err := b.respond(req)
			resp := proto.Response{}
			if err != nil {
				resp.Error = err.Error()
			} else {
				resp.Result, _ = json.Marshal(result)
			}
			if err := proto.WriteFrame(guest, resp, nil); err != nil {
				return
			}
		}
	}()
	return client, nil
}

func TestLaunchPollsForWindow(t *testing.T) {
	launchPollInterval = 5 * time.Millisecond
	defer func() { launchPollInterval = 300 * time.Millisecond }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lists atomic.Int32
	var launch proto.LaunchArgs
	b := newFakeAgentBackend(func(req proto.Request) (any, error) {
		switch req.Op {
		case proto.OpLaunch:
			json.Unmarshal(req.Args, &launch)
			return proto.LaunchResult{PID: 4120}, nil
		case proto.OpListWindows:
			n := lists.Add(1)
			ws := []proto.WindowInfo{{Handle: 1, PID: 999, Title: "Other", Class: "Other"}}
			if n >= 3 { // the splash (untitled) and the main window appear on the third poll
				ws = append(ws, proto.WindowInfo{Handle: 50, PID: 4120, Class: "Splash"}, proto.WindowInfo{Handle: 60, PID: 4120, Title: "AutoCAD", Class: "Afx:400000"})
			}
			return proto.WindowsResult{Windows: ws}, nil
		}
		return nil, errors.New("unexpected op " + req.Op)
	})
	cs, _ := connectTools(t, ctx, b)
	var out launchOut
	callJSON(t, ctx, cs, "vm_launch", map[string]any{"path": `C:\acad.exe`, "args": []string{"/nologo"}, "cwd": `C:\`}, &out)
	if out.PID != 4120 || out.Handle != 60 || out.Title != "AutoCAD" || out.Class != "Afx:400000" || out.ElapsedMs < 0 {
		t.Errorf("launch result %+v", out)
	}
	if launch.Path != `C:\acad.exe` || len(launch.Args) != 1 || launch.Args[0] != "/nologo" || launch.Cwd != `C:\` || launch.Admin {
		t.Errorf("launch args %+v", launch)
	}
	if lists.Load() != 3 {
		t.Errorf("list_windows called %d times, want 3", lists.Load())
	}
}

func TestLaunchPrefersTitledWindowElseFirst(t *testing.T) {
	ws := []proto.WindowInfo{{Handle: 1, PID: 7}, {Handle: 2, PID: 7, Title: "Main"}, {Handle: 3, PID: 7, Title: "Second"}}
	if w, ok := windowOfPID(ws, 7); !ok || w.Handle != 2 {
		t.Errorf("titled: %+v %v", w, ok)
	}
	if w, ok := windowOfPID(ws[:1], 7); !ok || w.Handle != 1 {
		t.Errorf("untitled only: %+v %v", w, ok)
	}
	if _, ok := windowOfPID(ws, 8); ok {
		t.Error("other PID matched")
	}
}

func TestLaunchTimeoutLeavesProcessRunning(t *testing.T) {
	launchPollInterval = 5 * time.Millisecond
	defer func() { launchPollInterval = 300 * time.Millisecond }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ops []string
	b := newFakeAgentBackend(func(req proto.Request) (any, error) {
		ops = append(ops, req.Op)
		switch req.Op {
		case proto.OpLaunch:
			return proto.LaunchResult{PID: 77}, nil
		case proto.OpListWindows:
			return proto.WindowsResult{}, nil
		}
		return nil, errors.New("unexpected op " + req.Op)
	})
	cs, d := connectTools(t, ctx, b)
	e := callRefused(t, ctx, cs, "vm_launch", map[string]any{"path": "notepad.exe", "wait_window_ms": 30})
	if e["error"] != codeNoWindow || e["pid"] != float64(77) || e["run_id"] != d.runID || e["next"] != "call vm_windows later, or vm_observe without handle to see a splash screen or dialog" {
		t.Errorf("timeout refusal %v", e)
	}
	for _, op := range ops {
		if op != proto.OpLaunch && op != proto.OpListWindows {
			t.Errorf("unexpected op %s (the process must be left alone)", op)
		}
	}
	if e := callRefused(t, ctx, cs, "vm_launch", map[string]any{"path": ""}); e["error"] != codeInvalidArgument {
		t.Errorf("missing path: %v", e)
	}
}
