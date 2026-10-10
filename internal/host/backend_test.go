package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
)

type lifecycleBackend struct {
	Backend    // Unexpected operations fail rather than reaching the real machine.
	calls      []string
	state      string
	restoreErr error
}

func (b *lifecycleBackend) Find(name string) (hyperv.VM, error) {
	b.calls = append(b.calls, "find:"+name)
	return hyperv.VM{Name: "test", ID: "test-id", State: b.state}, nil
}
func (b *lifecycleBackend) Start(name string) error {
	b.calls = append(b.calls, "start:"+name)
	b.state = "Running"
	return nil
}
func (b *lifecycleBackend) RestoreCheckpoint(vm, name string) error {
	b.calls = append(b.calls, "restore:"+vm+":"+name)
	b.state = "Off"
	return b.restoreErr
}

func TestRestoreThroughBackend(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		fail bool
		want []string
	}{
		{"default starts", map[string]any{"name": "baseline"}, false, []string{"find:", "restore:test:baseline", "find:test", "start:test"}},
		{"stay off", map[string]any{"vm": "test", "name": "baseline", "start": false}, false, []string{"find:test", "restore:test:baseline", "find:test"}},
		{"failure does not start", map[string]any{"name": "baseline"}, true, []string{"find:", "restore:test:baseline"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &lifecycleBackend{state: "Running"}
			if tc.fail {
				b.restoreErr = errors.New("backend restore failed")
			}
			m := &Manager{Backend: b}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, ct := mcp.NewInMemoryTransports()
			ss, err := NewServer(m).Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_restore", Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if r.IsError != tc.fail {
				t.Fatalf("tool result error=%v, want %v: %+v", r.IsError, tc.fail, r)
			}
			if !reflect.DeepEqual(b.calls, tc.want) {
				t.Fatalf("backend calls=%v, want %v", b.calls, tc.want)
			}
			if !tc.fail {
				var out struct{ VM, Restored, State string }
				if err := json.Unmarshal([]byte(resultText(r)), &out); err != nil || out.VM != "test" || out.Restored != "baseline" {
					t.Fatalf("result %s: %v", resultText(r), err)
				}
				if want := map[bool]string{true: "running", false: "off"}[tc.name == "default starts"]; out.State != want {
					t.Fatalf("state %q, want %q", out.State, want)
				}
			}
		})
	}
}
