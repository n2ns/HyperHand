package host

import (
	"fmt"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// inputWindow resolves the selected window (root) and the window that receives input (target) in ws: root itself
// when it is in the foreground, or else the foreground window when it is one of root's own windows (inGroup), such as
// AutoCAD's command line under its main window. Without a selector both are the foreground window.
func inputWindow(ws []proto.WindowInfo, sel windowSelector) (root, target proto.WindowInfo, err error) {
	if sel == (windowSelector{}) {
		fg, ok := foreground(ws)
		if !ok {
			return root, target, refuse(codeNoWindow, "pass handle (from vm_windows or vm_observe)", nil, "no window is in the foreground")
		}
		sel.Handle = fg.Handle
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
		return root, target, refuse(codeTargetDisabled, fmt.Sprintf("act on handle %d first", target.Handle), map[string]any{"act_on": target.Handle}, "input target %s is disabled or minimized", describe(target))
	}
	return root, target, nil
}

// keySequence validates vm_key's keys or sequence and returns the combinations to press, in order.
func keySequence(keys string, sequence []string) ([]string, error) {
	if (keys == "") == (len(sequence) == 0) {
		return nil, refuse(codeInvalidArgument, "pass exactly one of keys or sequence", nil, "keys and sequence are both %s", map[bool]string{true: "set", false: "empty"}[keys != ""])
	}
	combos := sequence
	if keys != "" {
		combos = []string{keys}
	}
	if len(combos) > 256 {
		return nil, refuse(codeInvalidArgument, "split the sequence into calls of at most 256 combinations", nil, "sequence has %d combinations", len(combos))
	}
	for i, c := range combos {
		if err := hyperv.ValidateKeys(c); err != nil {
			return nil, refuse(codeInvalidArgument, "fix the key name; see the vm_key description for the key names", nil, "combination %d (%q): %v", i+1, c, err)
		}
	}
	return combos, nil
}
