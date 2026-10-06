package host

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

type screenshotBackend struct {
	Backend
	data    []byte
	capture *proto.ScreenshotResult
}

func (b *screenshotBackend) Find(string) (hyperv.VM, error) {
	return hyperv.VM{ID: "A", Name: "fixed"}, nil
}
func (b *screenshotBackend) Screenshot(string) ([]byte, int, int, error) { return b.data, 8, 6, nil }
func (b *screenshotBackend) Dial(context.Context, string) (net.Conn, error) {
	a, c := net.Pipe()
	go func() {
		defer c.Close()
		for {
			var req proto.Request
			if _, err := proto.ReadFrame(c, &req); err != nil {
				return
			}
			var result json.RawMessage
			if b.capture != nil {
				result, _ = json.Marshal(b.capture)
			}
			if err := proto.WriteFrame(c, proto.Response{Result: result}, b.data); err != nil {
				return
			}
		}
	}()
	return a, nil
}
func TestScreenshotMCPMetadata(t *testing.T) {
	data, _ := screenshotFixture(t, 8, 6)
	for _, tt := range []struct {
		name, source string
		capture      *proto.ScreenshotResult
		compatible   bool
	}{
		{"host", "host", nil, true},
		{"old-agent", "agent", nil, false},
		{"console", "agent", &proto.ScreenshotResult{Width: 8, Height: 6, OriginX: -8, OriginY: 2, SessionID: 1, Console: true}, true},
		{"remote", "agent", &proto.ScreenshotResult{Width: 8, Height: 6, SessionID: 2}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			manager := &Manager{Backend: &screenshotBackend{data: data, capture: tt.capture}}
			defer manager.Drop("A")
			st, ct := mcp.NewInMemoryTransports()
			ss, err := NewServer(manager).Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "screenshot-test", Version: "test"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_screenshot", Arguments: map[string]any{"source": tt.source, "region": map[string]int{"x": 2, "y": 1, "width": 4, "height": 4}, "max_size": 2}})
			if err != nil {
				t.Fatal(err)
			}
			if r.IsError {
				t.Fatal(resultText(r))
			}
			var meta struct {
				screenshotGeometry
				VM, VMID, Source string
				OriginX          int  `json:"origin_x"`
				Console          bool `json:"console_coordinates"`
			}
			if len(r.Content) != 2 {
				t.Fatalf("content count %d", len(r.Content))
			}
			metadata, ok := r.Content[1].(*mcp.TextContent)
			if !ok {
				t.Fatal("missing metadata")
			}
			if err := json.Unmarshal([]byte(metadata.Text), &meta); err != nil {
				t.Fatal(err)
			}
			if meta.X != 2 || meta.Y != 1 || meta.OutputWidth != 2 || meta.ScaleX != 0.5 || meta.Console != tt.compatible || meta.VM != "fixed" {
				t.Fatalf("metadata: %+v", meta)
			}
			if tt.capture != nil && meta.OriginX != tt.capture.OriginX {
				t.Fatalf("lost origin: %+v", meta)
			}
			if len(r.Content) != 2 {
				t.Fatalf("content count %d", len(r.Content))
			}
			if _, ok := r.Content[0].(*mcp.ImageContent); !ok {
				t.Fatal("missing image")
			}
		})
	}
}
