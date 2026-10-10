package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"hyperhand/internal/mirror"
	"hyperhand/internal/proto"
)

func mirrorScan(ctx context.Context, args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.PathArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	if a.Path == "" {
		return nil, nil, fmt.Errorf("path is required")
	}
	result, err := mirror.Scan(ctx, a.Path, true)
	return result, nil, err
}

func mirrorApply(ctx context.Context, args json.RawMessage, payload []byte) (any, []byte, error) {
	result, err := mirrorApplyStream(ctx, args, bytes.NewReader(payload))
	return result, nil, err
}

func mirrorApplyStream(ctx context.Context, args json.RawMessage, payload io.Reader) (any, error) {
	var a proto.MirrorApplyArgs
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if a.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	return mirror.Apply(ctx, a.Path, a.Source, a.Target, a.Force, payload), nil
}

// Stage the complete wire payload before the serial request loop can mutate a target.
// Operation errors drain the remaining frame; truncated frames instead end the connection.
func stageMirrorPayload(r io.Reader, args json.RawMessage, size int64) (f *os.File, opErr, connErr error) {
	if size < 0 {
		return nil, nil, fmt.Errorf("invalid mirror payload size")
	}
	payload := &io.LimitedReader{R: r, N: size}
	defer func() {
		if _, err := io.Copy(io.Discard, payload); err != nil {
			connErr = err
		}
		if payload.N != 0 && connErr == nil {
			connErr = io.ErrUnexpectedEOF
		}
		if opErr != nil || connErr != nil {
			removeMirrorPayload(f)
			f = nil
		}
	}()
	var a proto.MirrorApplyArgs
	if err := decode(args, &a); err != nil {
		return nil, err, nil
	}
	if a.Path == "" {
		return nil, fmt.Errorf("path is required"), nil
	}
	// Diff validates both manifests before any disk space is allocated.
	changes, err := mirror.Diff(a.Source, a.Target, a.Force)
	if err != nil {
		return nil, err, nil
	}
	entries := make(map[string]mirror.Entry, len(a.Source.Entries))
	for _, entry := range a.Source.Entries {
		entries[strings.ToLower(entry.Path)] = entry
	}
	var expected int64
	for _, change := range changes {
		if change.Action != "copy" && change.Action != "overwrite" {
			continue
		}
		n := entries[strings.ToLower(change.Path)].Size
		if n > math.MaxInt64-expected {
			return nil, fmt.Errorf("mirror payload size exceeds supported range"), nil
		}
		expected += n
	}
	if expected != size {
		return nil, fmt.Errorf("mirror payload size mismatch: got %d, expected %d; no target changes applied", size, expected), nil
	}
	f, opErr = os.CreateTemp("", "hyperhand-mirror-payload-*")
	if opErr != nil {
		return f, opErr, nil
	}
	if _, opErr = io.Copy(f, payload); opErr != nil {
		return f, opErr, nil
	}
	_, opErr = f.Seek(0, io.SeekStart)
	return f, opErr, nil
}

func removeMirrorPayload(f *os.File) {
	if f != nil {
		name := f.Name()
		f.Close()
		os.Remove(name)
	}
}

// A drained request rejected before Apply is a confirmed failure with no writes,
// not an unknown outcome caused by losing the connection.
func mirrorStageFailure(args json.RawMessage, err error) mirror.Result {
	var a proto.MirrorApplyArgs
	_ = decode(args, &a)
	pending, _ := mirror.Diff(a.Source, a.Target, a.Force)
	if pending == nil {
		pending = []mirror.Change{}
	}
	return mirror.Result{
		Status: "partial", Completed: []mirror.Change{}, Pending: pending,
		Failed: &mirror.Failure{Path: a.Path, Reason: err.Error()},
	}
}
