package host

import (
	"context"
	"fmt"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

func inputWindow(ctx context.Context, call agentCall, vm string, sel windowSelector) (proto.WindowInfo, error) {
	ws, err := listWindows(ctx, call, vm)
	if err != nil {
		return proto.WindowInfo{}, err
	}
	if sel == (windowSelector{}) {
		for _, w := range ws {
			if w.Foreground {
				sel.Handle = w.Handle
				break
			}
		}
	}
	w, err := resolveWindow(ws, sel)
	if err != nil {
		return w, err
	}
	if !w.Foreground || !w.Enabled || w.Minimized {
		return w, fmt.Errorf("input target %s must be enabled, restored and foreground", describe(w))
	}
	return w, nil
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
