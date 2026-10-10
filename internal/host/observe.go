package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"hyperhand/internal/proto"
)

// Control tree limits of vm_observe (the agent's own defaults and ceilings).
const (
	defaultMaxDepth = 4
	defaultMaxNodes = 200
	limitMaxDepth   = 10
	limitMaxNodes   = 1000
)

// agentUnreachable reports whether err means the guest agent could not be reached (no VM connection, the agent is not
// running, or the connection broke), as opposed to an error the agent answered with.
func agentUnreachable(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	s := err.Error()
	return strings.HasPrefix(s, "connect to agent") || strings.HasPrefix(s, "agent: ")
}

// uiaTimedOut reports whether a list_controls error means the target did not answer UI Automation in time.
func uiaTimedOut(err error) bool {
	s := err.Error()
	return strings.Contains(s, "timed out") || strings.Contains(s, "interrupted")
}

// windowSummary is the compact description of a window in candidates lists of refusals.
type windowSummary struct {
	Handle  uint64 `json:"handle"`
	PID     uint32 `json:"pid"`
	Process string `json:"process"`
	Title   string `json:"title"`
	Class   string `json:"class"`
}

// windowCandidates summarizes the windows a selector could have meant: those of the PID when one is given, else all.
func windowCandidates(ws []proto.WindowInfo, pid uint32) []windowSummary {
	out := []windowSummary{}
	for _, w := range ws {
		if pid == 0 || w.PID == pid {
			out = append(out, windowSummary{Handle: w.Handle, PID: w.PID, Process: w.Process, Title: w.Title, Class: w.Class})
		}
	}
	return out
}

// observedWindow converts a listed window to the observation result's window.
func observedWindow(w proto.WindowInfo) *observeWindow {
	return &observeWindow{Handle: w.Handle, PID: w.PID, Process: w.Process, Title: w.Title, Class: w.Class, Rect: w.Rect,
		Foreground: w.Foreground, Enabled: w.Enabled, GroupRoot: w.GroupRoot, Integrity: w.Integrity}
}

// observeVM is vm_observe (observeFunc). It resolves the VM once, lists the windows, selects the window (handle/pid) or
// the whole screen, takes the host console screenshot cropped to the window, reads the control tree when asked, stores
// the observation and returns its JSON item and PNG.
func (d *deps) observeVM(ctx context.Context, in observeIn) (*observeOut, []byte, error) {
	if in.MaxDepth < 0 || in.MaxDepth > limitMaxDepth || in.MaxNodes < 0 || in.MaxNodes > limitMaxNodes {
		return nil, nil, refuse(codeInvalidArgument, fmt.Sprintf("pass max_depth 0..%d and max_nodes 0..%d (0 uses the defaults %d and %d)", limitMaxDepth, limitMaxNodes, defaultMaxDepth, defaultMaxNodes), nil, "max_depth %d, max_nodes %d out of range", in.MaxDepth, in.MaxNodes)
	}
	if in.MaxSize < 0 {
		return nil, nil, refuse(codeInvalidArgument, "pass max_size 0 (original size) or a positive longest side in pixels", nil, "max_size %d is negative", in.MaxSize)
	}
	if in.MaxDepth == 0 {
		in.MaxDepth = defaultMaxDepth
	}
	if in.MaxNodes == 0 {
		in.MaxNodes = defaultMaxNodes
	}
	wantImage := in.Screenshot == nil || *in.Screenshot
	wantControls := in.Controls || in.DiffFrom != ""
	windowed := in.Handle != 0 || in.PID != 0

	v, err := d.raw.Find(in.VM)
	if err != nil {
		return nil, nil, refuse(codeFailed, "call vm_list and pass vm", nil, "%v", err)
	}
	vm := v.Name
	out := &observeOut{VM: vm, CapturedAt: time.Now().UTC().Format(time.RFC3339)}
	obs := &observation{VM: vm, Scale: 1}
	var risks []string

	var wr proto.WindowsResult
	online := true
	if _, err := d.call(ctx, vm, proto.OpListWindows, nil, nil, &wr); err != nil {
		if !agentUnreachable(err) {
			return nil, nil, err
		}
		if windowed {
			return nil, nil, agentRequired(err)
		}
		online = false
		out.Agent = "offline"
	}
	obs.Windows = wr.Windows

	// The window whose rect crops the screenshot (windowed) and whose control tree is read (windowed, or the
	// foreground window for a whole-screen observation with controls).
	var target *proto.WindowInfo
	if windowed {
		w, err := resolveWindow(wr.Windows, windowSelector{Handle: in.Handle, PID: in.PID})
		if err != nil {
			// resolveWindow refuses with no_window or ambiguous_target; add the windows the selector could have meant.
			te := asToolError(err)
			if te.Fields == nil {
				te.Fields = map[string]any{}
			}
			te.Fields["candidates"] = windowCandidates(wr.Windows, in.PID)
			return nil, nil, te
		}
		target = &w
		obs.Window = &w
		out.Window = observedWindow(w)
	} else if wantControls && online {
		if fg, ok := findWindow(wr.Windows, wr.Foreground); ok && wr.Foreground != 0 {
			target = &fg
			out.Window = observedWindow(fg)
		} else {
			risks = append(risks, "no foreground window: control tree not read")
		}
	}

	var png []byte
	if wantImage {
		data, sw, sh, err := d.raw.Screenshot(vm)
		if err != nil {
			return nil, nil, refuse(codeFailed, "call vm_status and make sure the VM is running", nil, "screenshot: %v", err)
		}
		opts := screenshotOptions{MaxSize: in.MaxSize}
		if windowed {
			r, ok := clampRegion(target.Rect, sw, sh)
			if !ok {
				state := "entirely off the %dx%d screen"
				if target.Minimized {
					state = "minimized, so it is not on the %dx%d screen"
				}
				return nil, nil, refuse(codeInvalidArgument, "restore the window first (vm_key, or act on it by index), or observe without handle", nil,
					"window %s (rect %d,%d %dx%d) is "+state, describe(*target), target.Rect.Left, target.Rect.Top, target.Rect.Right-target.Rect.Left, target.Rect.Bottom-target.Rect.Top, sw, sh)
			}
			opts.Region = &r
		}
		img, g, err := transformScreenshot(data, opts)
		if err != nil {
			return nil, nil, refuse(codeFailed, "call vm_observe again", nil, "screenshot: %v", err)
		}
		png = img
		obs.Crop = screenshotRegion{X: g.X, Y: g.Y, Width: g.Width, Height: g.Height}
		obs.Scale = g.ScaleX
		obs.OutputWidth, obs.OutputHgt = g.OutputWidth, g.OutputHeight
		obs.HasImage = true
		out.Screenshot = &observeScreenshot{Width: g.OutputWidth, Height: g.OutputHeight, OriginX: g.X, OriginY: g.Y, Scale: g.ScaleX}
	}

	var cr proto.ControlsResult
	captured := false
	if wantControls && target != nil {
		args := proto.ControlsArgs{Handle: target.Handle, PID: target.PID, MaxDepth: in.MaxDepth, MaxNodes: in.MaxNodes}
		if _, err := d.call(ctx, vm, proto.OpListControls, args, nil, &cr); err != nil {
			switch {
			case agentUnreachable(err) && windowed:
				return nil, nil, agentRequired(err)
			case agentUnreachable(err):
				out.Agent, out.Window = "offline", nil
			case uiaTimedOut(err):
				risks = append(risks, "target not responding: control tree not read")
			default:
				return nil, nil, err
			}
		} else {
			captured = true
			if cr.Nodes == nil {
				cr.Nodes = []proto.ControlInfo{}
			}
			obs.Nodes = cr.Nodes
			obs.Window = target // for a whole-screen observation: the foreground window the indexes belong to
			out.ControlsTruncated = cr.Truncated
			out.SelectedText = cr.SelectedText
		}
	}

	if captured && cr.Focused >= 0 && cr.Focused < len(cr.Nodes) {
		n := cr.Nodes[cr.Focused]
		out.Focused = &observeFocused{Index: n.Index, Name: n.Name, ControlType: proto.ControlTypeName(n.ControlType), ClassName: n.ClassName, Rect: n.Rect}
	} else if f := wr.Focused; f != nil && out.Agent == "" && (!windowed || inGroup(wr.Windows, *target, f.Window)) {
		out.Focused = &observeFocused{Index: -1, Name: f.Name, ControlType: f.ControlType, ClassName: f.ClassName, Rect: f.Rect}
	}

	if captured {
		text := renderControls(cr.Nodes)
		out.Controls = &text
		if in.DiffFrom != "" {
			if prev, reason := d.diffBase(in.DiffFrom, vm, target.Handle); prev != nil {
				out.Controls = nil
				out.ControlsDiff = diffControls(prev.Nodes, cr.Nodes)
			} else {
				risks = append(risks, "diff_from ignored: "+reason)
			}
		}
	} else if in.DiffFrom != "" && target != nil {
		risks = append(risks, "diff_from ignored: no control tree in this observation")
	}
	out.StaleRisk = strings.Join(risks, "; ")

	obs.ID = newID()
	obs.At = time.Now()
	d.obs.put(obs)
	out.ObservationID = obs.ID
	return out, png, nil
}

// diffBase returns the observation diff_from names when it can be diffed against the current one (same VM, same
// window, has a control tree); otherwise nil and the reason to report in stale_risk.
func (d *deps) diffBase(id, vm string, handle uint64) (*observation, string) {
	prev, err := d.obs.get(id)
	switch {
	case err != nil:
		return nil, fmt.Sprintf("observation %q is unknown (expired or never issued)", id)
	case prev.VM != vm:
		return nil, fmt.Sprintf("observation %s is of VM %s", id, prev.VM)
	case prev.Window == nil || prev.Window.Handle != handle:
		return nil, fmt.Sprintf("observation %s is of a different window", id)
	case prev.Nodes == nil:
		return nil, fmt.Sprintf("observation %s has no control tree", id)
	}
	return prev, ""
}
