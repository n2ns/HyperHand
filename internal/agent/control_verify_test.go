package agent

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"hyperhand/internal/proto"
)

func TestControlScrollState(t *testing.T) {
	yes, no := true, false
	for _, scrollable := range []*bool{nil, &yes, &no} {
		for _, value := range []float64{0, 50, 100, -1, math.NaN(), math.Inf(1)} {
			got := readableScrollPercent(value, scrollable)
			want := (scrollable == nil || *scrollable) && validScrollPercent(value)
			if (got != nil) != want || got != nil && *got != value {
				t.Errorf("readable percentage value=%v axis=%v: got %v", value, scrollable, got)
			}
		}
	}
	for _, action := range []string{"ScrollUp", "ScrollDown", "ScrollLeft", "ScrollRight"} {
		for _, test := range []struct {
			before, after float64
			want          bool
		}{
			{50, 60, action == "ScrollDown" || action == "ScrollRight"},
			{50, 40, action == "ScrollUp" || action == "ScrollLeft"},
			{0, 0, false}, {100, 100, false},
		} {
			before := &proto.ControlState{HorizontalScrollPercent: &test.before, VerticalScrollPercent: &test.before}
			after := &proto.ControlState{HorizontalScrollPercent: &test.after, VerticalScrollPercent: &test.after}
			if got := verifyControlState(action, before, after); got == nil || *got != test.want {
				t.Errorf("%s %v->%v: got %v want %v", action, test.before, test.after, got, test.want)
			}
			if got := verifyControlState(action, nil, after); got != nil {
				t.Errorf("%s unknown before: %v", action, got)
			}
			if got := verifyControlState(action, before, &proto.ControlState{}); got != nil {
				t.Errorf("%s unknown after: %v", action, got)
			}
		}
	}
	for _, v := range []float64{-1, -0.001, 100.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if validScrollPercent(v) {
			t.Errorf("invalid percentage accepted: %v", v)
		}
		p := 50.0
		if got := verifyControlState("ScrollDown", &proto.ControlState{VerticalScrollPercent: &p}, &proto.ControlState{VerticalScrollPercent: &v}); got != nil {
			t.Errorf("invalid readback verified: %v -> %v", v, got)
		}
	}
	for _, v := range []float64{0, 0.1, 50, 100} {
		if !validScrollPercent(v) {
			t.Errorf("valid percentage rejected: %v", v)
		}
	}
}

func TestControlScrollActions(t *testing.T) {
	available := make([]bool, len(uiaPatterns))
	available[patternFor("ScrollUp")] = true
	available[patternFor("Invoke")] = true
	yes, no := true, false
	for _, test := range []struct {
		state                *proto.ControlState
		horizontal, vertical bool
	}{
		{nil, true, true}, {&proto.ControlState{}, true, true},
		{&proto.ControlState{HorizontallyScrollable: &no, VerticallyScrollable: &yes}, false, true},
		{&proto.ControlState{HorizontallyScrollable: &yes, VerticallyScrollable: &no}, true, false},
		{&proto.ControlState{HorizontallyScrollable: &no, VerticallyScrollable: &no}, false, false},
	} {
		actions := actionNamesForState(available, test.state)
		for _, action := range []string{"ScrollLeft", "ScrollRight"} {
			if slices.Contains(actions, action) != test.horizontal {
				t.Errorf("horizontal actions %v, want enabled=%v", actions, test.horizontal)
			}
		}
		for _, action := range []string{"ScrollUp", "ScrollDown"} {
			if slices.Contains(actions, action) != test.vertical {
				t.Errorf("vertical actions %v, want enabled=%v", actions, test.vertical)
			}
		}
		if !slices.Contains(actions, "Invoke") || !slices.Contains(patternNames(available), "Scroll") {
			t.Errorf("unrelated action or legacy pattern dropped: %v", actions)
		}
	}
	available[patternFor("Invoke")] = false
	actions := actionNamesForState(available, &proto.ControlState{HorizontallyScrollable: &no, VerticallyScrollable: &no})
	data, err := json.Marshal(proto.ControlInfo{Patterns: []string{"Scroll"}, Actions: actions})
	if err != nil || !strings.Contains(string(data), `"actions":[]`) {
		t.Fatalf("explicit empty actions lost on wire: %s, %v", data, err)
	}
	var roundtrip proto.ControlInfo
	if err := json.Unmarshal(data, &roundtrip); err != nil || roundtrip.Actions == nil || len(roundtrip.Actions) != 0 {
		t.Fatalf("empty actions indistinguishable from legacy node: %+v, %v", roundtrip, err)
	}
}

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
