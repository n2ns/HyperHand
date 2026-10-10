package host

import (
	"context"
	"fmt"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// inputWindow resolves the selected window (root) and the window that receives input (target): root itself when it is
// in the foreground, or else the foreground window when it is one of root's own windows (inGroup), such as AutoCAD's
// command line under its main window. Without a selector both are the foreground window.
func inputWindow(ctx context.Context, call agentCall, vm string, sel windowSelector) (root, target proto.WindowInfo, err error) {
	ws, err := listWindows(ctx, call, vm)
	if err != nil {
		return root, target, err
	}
	if sel == (windowSelector{}) {
		if fg, ok := foreground(ws); ok {
			sel.Handle = fg.Handle
		}
	}
	root, err = resolveWindow(ws, sel)
	if err != nil {
		return root, target, err
	}
	// root must be enabled too: a modal dialog that pops up mid-sequence stops the input instead of receiving it.
	if err := checkUsable(ws, root); err != nil {
		return root, target, err
	}
	target = root
	if !root.Foreground {
		target, _ = foreground(ws)
	}
	if !target.Enabled || target.Minimized {
		return root, target, fmt.Errorf("input target %s must be enabled and restored; call vm_focus_window with handle %d first", describe(target), target.Handle)
	}
	return root, target, nil
}

func keySequence(in keyIn) ([]string, error) {
	if (in.Keys == "") == (len(in.Sequence) == 0) {
		return nil, fmt.Errorf("pass exactly one of keys or sequence")
	}
	keys := in.Sequence
	if in.Keys != "" {
		keys = []string{in.Keys}
	}
	if len(keys) > 256 {
		return nil, fmt.Errorf("sequence exceeds 256 combinations")
	}
	for _, key := range keys {
		if err := hyperv.ValidateKeys(key); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
