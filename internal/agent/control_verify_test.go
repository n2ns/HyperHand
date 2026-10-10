package agent

import (
	"testing"
	"time"

	"hyperhand/internal/proto"
)

func TestVerifyControlState(t *testing.T) {
	on, off, indeterminate := "on", "off", "indeterminate"
	expanded, collapsed, partial, leaf := "expanded", "collapsed", "partially_expanded", "leaf"
	yes, no := true, false
	tests := []struct {
		name, action  string
		before, after *proto.ControlState
		want          *bool
	}{
		{"toggle on", "Toggle", &proto.ControlState{Toggle: &off}, &proto.ControlState{Toggle: &on}, &yes},
		{"toggle indeterminate", "Toggle", &proto.ControlState{Toggle: &on}, &proto.ControlState{Toggle: &indeterminate}, &yes},
		{"toggle wrap", "Toggle", &proto.ControlState{Toggle: &indeterminate}, &proto.ControlState{Toggle: &off}, &yes},
		{"toggle unchanged", "Toggle", &proto.ControlState{Toggle: &on}, &proto.ControlState{Toggle: &on}, &no},
		{"toggle unknown before", "Toggle", nil, &proto.ControlState{Toggle: &on}, nil},
		{"expand", "Expand", nil, &proto.ControlState{ExpandCollapse: &expanded}, &yes},
		{"expand partial", "Expand", nil, &proto.ControlState{ExpandCollapse: &partial}, &no},
		{"collapse", "Collapse", nil, &proto.ControlState{ExpandCollapse: &collapsed}, &yes},
		{"collapse leaf", "Collapse", nil, &proto.ControlState{ExpandCollapse: &leaf}, &no},
		{"select", "Select", nil, &proto.ControlState{Selected: &yes}, &yes},
		{"unselected", "Select", nil, &proto.ControlState{Selected: &no}, &no},
		{"scroll visible", "ScrollIntoView", nil, &proto.ControlState{Offscreen: &no}, &yes},
		{"scroll invisible", "ScrollIntoView", nil, &proto.ControlState{Offscreen: &yes}, &no},
		{"invoke unknown", "Invoke", nil, &proto.ControlState{Selected: &yes}, nil},
		{"locate", "Locate", nil, &proto.ControlState{Selected: &yes}, nil},
		{"element disappeared", "Select", nil, nil, nil},
		{"property unreadable", "Select", nil, &proto.ControlState{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verifyControlState(tt.action, tt.before, tt.after)
			if (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestControlVerificationReadback(t *testing.T) {
	yes, no := true, false
	t.Run("immediate success", func(t *testing.T) {
		calls := 0
		r := readVerifiedControlAction("Select", nil, func() proto.ControlActionResult {
			calls++
			return proto.ControlActionResult{State: &proto.ControlState{Selected: &yes}}
		})
		if calls != 1 || r.Verified == nil || !*r.Verified {
			t.Fatalf("calls=%d result=%+v", calls, r)
		}
	})
	t.Run("asynchronous success", func(t *testing.T) {
		calls := 0
		r := readVerifiedControlAction("Select", nil, func() proto.ControlActionResult {
			calls++
			selected := calls >= 3
			return proto.ControlActionResult{State: &proto.ControlState{Selected: &selected}}
		})
		if calls != 3 || r.Verified == nil || !*r.Verified {
			t.Fatalf("calls=%d result=%+v", calls, r)
		}
	})
	for _, unreadable := range []bool{false, true} {
		t.Run(map[bool]string{false: "mismatch", true: "unavailable"}[unreadable], func(t *testing.T) {
			start, calls := time.Now(), 0
			r := readVerifiedControlAction("Select", nil, func() proto.ControlActionResult {
				calls++
				if unreadable {
					return proto.ControlActionResult{}
				}
				return proto.ControlActionResult{State: &proto.ControlState{Selected: &no}}
			})
			if calls < 2 || calls > 15 || time.Since(start) > time.Second {
				t.Fatalf("unbounded readback: calls=%d duration=%s", calls, time.Since(start))
			}
			if unreadable && r.Verified != nil || !unreadable && (r.Verified == nil || *r.Verified) {
				t.Fatalf("incorrect unknown/mismatch result: %+v", r)
			}
		})
	}
	t.Run("invoke never implies business success", func(t *testing.T) {
		calls := 0
		r := readVerifiedControlAction("Invoke", nil, func() proto.ControlActionResult {
			calls++
			return proto.ControlActionResult{Verified: &yes}
		})
		if calls != 1 || r.Verified != nil {
			t.Fatalf("calls=%d result=%+v", calls, r)
		}
	})
	t.Run("value readback preserves comparison", func(t *testing.T) {
		calls := 0
		r := readVerifiedControlAction("SetValue", nil, func() proto.ControlActionResult {
			calls++
			return proto.ControlActionResult{Value: "value", HasValue: true, Verified: &yes}
		})
		if calls != 1 || r.Verified == nil || !*r.Verified || r.Value != "value" {
			t.Fatalf("calls=%d result=%+v", calls, r)
		}
	})
}
