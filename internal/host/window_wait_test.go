package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hyperhand/internal/proto"
)

func TestWaitWindowTransitions(t *testing.T) {
	for _, kind := range []string{"window_exists", "window_gone", "window_foreground"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			call := func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error) {
				calls++
				if vm != "CAD-VM" || op != proto.OpListWindows {
					t.Fatalf("unexpected call %q %q", vm, op)
				}
				w := testWindows()[0]
				var ws []proto.WindowInfo
				switch kind {
				case "window_exists":
					if calls >= 2 {
						ws = []proto.WindowInfo{w}
					}
				case "window_gone":
					if calls == 1 {
						ws = []proto.WindowInfo{w}
					}
				case "window_foreground":
					w.Foreground = calls >= 2
					ws = []proto.WindowInfo{w}
				}
				result.(*proto.WindowsResult).Windows = ws
				return nil, nil
			}
			r, err := waitWindow(context.Background(), call, waitIn{VM: "CAD-VM", Kind: kind, Title: "OPTIONS", PID: 100, Exact: true, TimeoutMs: 1500})
			if err != nil {
				t.Fatal(err)
			}
			got := resultText(r)
			if calls < 2 || !strings.Contains(got, "satisfied: true") {
				t.Fatalf("calls=%d result=%q", calls, got)
			}
			if kind == "window_gone" {
				if got != "satisfied: true" {
					t.Errorf("gone result: %q", got)
				}
			} else if !strings.Contains(got, "handle: 10") || !strings.Contains(got, "Options") {
				t.Errorf("missing resolved window: %q", got)
			}
		})
	}
}

func TestWaitWindowAbsentAndTimeout(t *testing.T) {
	for _, kind := range []string{"window_exists", "window_gone", "window_foreground"} {
		t.Run(kind, func(t *testing.T) {
			f := &fakeCall{results: map[string]any{proto.OpListWindows: proto.WindowsResult{}}}
			r, err := waitWindow(context.Background(), f.call, waitIn{Kind: kind, Handle: 10, TimeoutMs: 20})
			if err != nil {
				t.Fatal(err)
			}
			want := "satisfied: false"
			if kind == "window_gone" {
				want = "satisfied: true"
			}
			if resultText(r) != want {
				t.Errorf("want %q, got %q", want, resultText(r))
			}
			if len(f.ops) == 0 {
				t.Error("must query windows")
			}
			if kind == "window_gone" && len(f.ops) != 1 {
				t.Errorf("gone should return immediately, calls=%d", len(f.ops))
			}
		})
	}
}

func TestWaitWindowRefusesAmbiguousAndFailedQueries(t *testing.T) {
	for _, kind := range []string{"window_exists", "window_gone", "window_foreground"} {
		t.Run(kind, func(t *testing.T) {
			f := newAgent(10)
			if _, err := waitWindow(context.Background(), f.call, waitIn{Kind: kind, Title: "autocad", TimeoutMs: 20}); err == nil || errors.Is(err, errWindowNotFound) {
				t.Errorf("ambiguous: %v", err)
			}
			failure := errors.New("guest connection closed")
			f = &fakeCall{errs: map[string]error{proto.OpListWindows: failure}}
			if _, err := waitWindow(context.Background(), f.call, waitIn{Kind: kind, Handle: 10, TimeoutMs: 20}); !errors.Is(err, failure) {
				t.Errorf("query failure must not satisfy wait: %v", err)
			}
			f = oldAgent()
			if _, err := waitWindow(context.Background(), f.call, waitIn{Kind: kind, Handle: 10, TimeoutMs: 20}); err == nil || !strings.Contains(err.Error(), "vm_update_agent") {
				t.Errorf("old agent: %v", err)
			}
		})
	}
}

func TestWaitWindowCancellation(t *testing.T) {
	for _, preCanceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if preCanceled {
			cancel()
		}
		calls := 0
		call := func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error) {
			calls++
			cancel()
			return nil, nil
		}
		_, err := waitWindow(ctx, call, waitIn{Kind: "window_exists", Handle: 10, TimeoutMs: 1500})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("preCanceled=%v calls=%d: %v", preCanceled, calls, err)
		}
	}
	// Expiration of the parent context is cancellation, not the tool's ordinary timeout result.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	f := &fakeCall{results: map[string]any{proto.OpListWindows: proto.WindowsResult{}}}
	if _, err := waitWindow(ctx, f.call, waitIn{Kind: "window_exists", Handle: 10, TimeoutMs: 1500}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("parent deadline: %v", err)
	}
}

func TestWaitWindowValidatesSelector(t *testing.T) {
	for _, in := range []waitIn{{Kind: "window_exists"}, {Kind: "window_gone", Exact: true}, {Kind: "window_foreground", PID: 100, Exact: true}} {
		f := newAgent(10)
		if _, err := waitWindow(context.Background(), f.call, in); err == nil || errors.Is(err, errWindowNotFound) {
			t.Errorf("invalid selector %+v: %v", in, err)
		}
	}
}

func TestWaitWindowTimeoutDuringQuery(t *testing.T) {
	call := func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	r, err := waitWindow(context.Background(), call, waitIn{Kind: "window_gone", Handle: 10, TimeoutMs: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(r); got != "satisfied: false" {
		t.Errorf("query deadline must not mean gone: %q", got)
	}
}
