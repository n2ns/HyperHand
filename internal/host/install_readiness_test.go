package host

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

func TestInstallWaitRejectsPreviousAgent(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, oldID := range []string{"", "previous-installation"} {
		t.Run(oldID, func(t *testing.T) {
			var calls int
			b := &windowMCPBackend{respond: func(_ string, req proto.Request) (any, error) {
				calls++
				p := proto.PingResult{Version: "same-version", Protocol: proto.Protocol, InstallID: oldID}
				if calls == 3 {
					p.InstallID = id
				}
				return p, nil
			}}
			c := NewClient(func(ctx context.Context) (net.Conn, error) { return b.Dial(ctx, "A") })
			defer c.Close()
			p, err := waitAgentPing(context.Background(), c, id, time.Second, time.Millisecond)
			if err != nil || p.InstallID != id || calls != 3 {
				t.Fatalf("accepted previous process: %+v, calls=%d, err=%v", p, calls, err)
			}
		})
	}
}

func TestInstallWaitTimeoutAndCancellation(t *testing.T) {
	b := &windowMCPBackend{respond: func(_ string, _ proto.Request) (any, error) {
		return proto.PingResult{Version: "same-version", Protocol: proto.Protocol}, nil
	}}
	c := NewClient(func(ctx context.Context) (net.Conn, error) { return b.Dial(ctx, "A") })
	defer c.Close()
	_, err := waitAgentPing(context.Background(), c, "new-installation", 30*time.Millisecond, time.Millisecond)
	var te *toolError
	if !errors.As(err, &te) || te.Fields["install_id"] != "new-installation" || !strings.Contains(te.Next, "installation was not confirmed") {
		t.Fatalf("unconfirmed installation: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err = waitInstalledAgent(ctx, c, "new-installation")
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("cancellation did not interrupt wait: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = waitAgentPing(ctx, c, "new-installation", time.Minute, 5*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("cancellation did not interrupt polling sleep: %v", err)
	}
}

type installMCPBackend struct {
	*windowMCPBackend
	mu      sync.Mutex
	command string
	keys    []string
	copies  int
}

func (b *installMCPBackend) CopyToGuest(vm, source, target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.copies++
	if vm != "CAD" || !strings.HasSuffix(source, "hyperhand-agent.exe") || target != GuestAgentPath {
		return errors.New("wrong installation paths")
	}
	return nil
}
func (b *installMCPBackend) PressKeys(_ string, keys string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.keys = append(b.keys, keys)
	return nil
}
func (b *installMCPBackend) TypeText(_ string, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.command = text
	return nil
}

func TestInstallMCPConfirmsSameVersionReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &installMCPBackend{windowMCPBackend: &windowMCPBackend{find: func(string) (hyperv.VM, error) { return hyperv.VM{ID: "A", Name: "CAD"}, nil }}}
	var pings int
	b.respond = func(_ string, _ proto.Request) (any, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		pings++
		p := proto.PingResult{Version: "same-version", Protocol: proto.Protocol}
		if pings > 1 {
			p.InstallID = strings.TrimPrefix(b.command, GuestAgentPath+" install --install-id ")
		}
		return p, nil
	}
	manager := &Manager{Backend: b}
	defer manager.Drop("A")
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewServer(manager).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "install-test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_install_agent", Arguments: map[string]any{"vm": "CAD"}})
	if err != nil {
		t.Fatal(err)
	}
	result := resultJSON(t, r)
	if r.IsError {
		t.Fatalf("install failed: %v", result)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id := strings.TrimPrefix(b.command, GuestAgentPath+" install --install-id ")
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 || b.copies != 1 || pings != 2 || strings.Join(b.keys, ",") != "win+r,enter" {
		t.Fatalf("installation sequence: command=%q copies=%d pings=%d keys=%v", b.command, b.copies, pings, b.keys)
	}
	agent := result["agent"].(map[string]any)
	if agent["install_id"] != id {
		t.Fatalf("result lost confirmation identity: %v", result)
	}
}
