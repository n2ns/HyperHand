package agent

import (
	"math"
	"time"

	"hyperhand/internal/proto"
)

// readVerifiedControlAction observes asynchronous providers for up to 250 ms.
// It never repeats the action and returns immediately when the target state is observed.
func readVerifiedControlAction(action string, before *proto.ControlState, read func() proto.ControlActionResult) proto.ControlActionResult {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		r := read()
		if action != "SetValue" {
			r.Verified = verifyControlState(action, before, r.State)
		}
		switch action {
		case "SetValue", "Toggle", "Expand", "Collapse", "Select", "ScrollIntoView", "ScrollUp", "ScrollDown", "ScrollLeft", "ScrollRight":
		default:
			return r
		}
		if r.Verified != nil && *r.Verified {
			return r
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return r
		}
		time.Sleep(min(25*time.Millisecond, remaining))
	}
}

// A nil result means that UIA supplied no comparable state, or the action has no
// generic success state (Invoke). False only means the read-back did not match.
func verifyControlState(action string, before, after *proto.ControlState) *bool {
	if after == nil {
		return nil
	}
	var matched bool
	switch action {
	case "Toggle":
		if before == nil || before.Toggle == nil || after.Toggle == nil {
			return nil
		}
		matched = *before.Toggle != *after.Toggle
	case "Expand", "Collapse":
		if after.ExpandCollapse == nil {
			return nil
		}
		want := "expanded"
		if action == "Collapse" {
			want = "collapsed"
		}
		matched = *after.ExpandCollapse == want
	case "Select":
		if after.Selected == nil {
			return nil
		}
		matched = *after.Selected
	case "ScrollIntoView":
		if after.Offscreen == nil {
			return nil
		}
		matched = !*after.Offscreen
	case "ScrollUp", "ScrollDown", "ScrollLeft", "ScrollRight":
		if before == nil {
			return nil
		}
		old, current := before.VerticalScrollPercent, after.VerticalScrollPercent
		if action == "ScrollLeft" || action == "ScrollRight" {
			old, current = before.HorizontalScrollPercent, after.HorizontalScrollPercent
		}
		if old == nil || current == nil || !validScrollPercent(*old) || !validScrollPercent(*current) {
			return nil
		}
		if action == "ScrollUp" || action == "ScrollLeft" {
			matched = *current < *old
		} else {
			matched = *current > *old
		}
	default:
		return nil
	}
	return &matched
}

func validScrollPercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func readableScrollPercent(value float64, scrollable *bool) *float64 {
	if scrollable != nil && !*scrollable || !validScrollPercent(value) {
		return nil
	}
	return &value
}
