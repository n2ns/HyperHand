package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hyperhand/internal/mirror"
	"hyperhand/internal/proto"
)

func mirrorFixture(t *testing.T) (proto.MirrorApplyArgs, []byte) {
	t.Helper()
	source, target := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("mirror payload\n"), 250000)
	for path, content := range map[string][]byte{
		filepath.Join(source, "plugin.dll"): data,
		filepath.Join(target, "old.dll"):    []byte("remove after copy"),
	} {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, err := mirror.Scan(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := mirror.Scan(context.Background(), target, false)
	if err != nil {
		t.Fatal(err)
	}
	return proto.MirrorApplyArgs{Path: target, Source: src, Target: dst}, data
}

func mirrorPipe(t *testing.T) (net.Conn, <-chan error) {
	t.Helper()
	client, server := net.Pipe()
	client.SetDeadline(time.Now().Add(15 * time.Second))
	done := make(chan error, 1)
	go func() { done <- Serve(server) }()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not stop after disconnect")
		}
	})
	return client, done
}

func TestMirrorDispatchScan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.dll"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, _, err := Dispatch(context.Background(), proto.OpMirrorScan, mustJSON(proto.PathArgs{Path: dir}), nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := result.(mirror.Manifest)
	if !manifest.Exists || len(manifest.Entries) != 1 || manifest.Entries[0].Path != "plugin.dll" {
		t.Fatalf("scan: %+v", manifest)
	}
	result, _, err = Dispatch(context.Background(), proto.OpMirrorScan, mustJSON(proto.PathArgs{Path: filepath.Join(dir, "missing")}), nil)
	if err != nil || result.(mirror.Manifest).Exists {
		t.Fatalf("missing target scan: %+v, %v", result, err)
	}
	for _, op := range []string{proto.OpMirrorScan, proto.OpMirrorApply} {
		for _, args := range []json.RawMessage{nil, json.RawMessage(`"invalid"`)} {
			if _, _, err := Dispatch(context.Background(), op, args, nil); err == nil {
				t.Fatalf("%s accepted invalid args %s", op, args)
			}
		}
	}
}

func TestServeMirrorApply(t *testing.T) {
	a, data := mirrorFixture(t)
	client, _ := mirrorPipe(t)
	var manifest mirror.Manifest
	call(t, client, proto.OpMirrorScan, proto.PathArgs{Path: a.Path}, nil, &manifest)
	if len(manifest.Entries) != 1 || manifest.Entries[0].Path != "old.dll" {
		t.Fatalf("target scan: %+v", manifest)
	}
	var result mirror.Result
	call(t, client, proto.OpMirrorApply, a, data, &result)
	if result.Status != "complete" {
		t.Fatalf("apply: %+v", result)
	}
	if got, err := os.ReadFile(filepath.Join(a.Path, "plugin.dll")); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("mirrored file: len=%d, %v", len(got), err)
	}
	if _, err := os.Stat(filepath.Join(a.Path, "old.dll")); !os.IsNotExist(err) {
		t.Fatalf("obsolete file remains: %v", err)
	}
	call(t, client, proto.OpPing, nil, nil, nil)
}

func TestServeMirrorInvalidArgsDrainsPayload(t *testing.T) {
	client, _ := mirrorPipe(t)
	for _, args := range []json.RawMessage{json.RawMessage(`"invalid"`), json.RawMessage(`{}`)} {
		if err := proto.WriteFrame(client, proto.Request{Op: proto.OpMirrorApply, Args: args}, bytes.Repeat([]byte("x"), 3<<20)); err != nil {
			t.Fatal(err)
		}
		var resp proto.Response
		if _, err := proto.ReadFrame(client, &resp); err != nil || resp.Error != "" {
			t.Fatalf("invalid args: %+v, %v", resp, err)
		}
		var result mirror.Result
		if err := json.Unmarshal(resp.Result, &result); err != nil || result.Status != "partial" || result.Failed == nil || len(result.Completed) != 0 {
			t.Fatalf("unconfirmed staging rejection: %+v, %v", result, err)
		}
		call(t, client, proto.OpPing, nil, nil, nil)
	}
}

func TestServeMirrorPayloadMismatchPreservesTarget(t *testing.T) {
	for _, trailing := range []bool{false, true} {
		t.Run(map[bool]string{false: "short", true: "trailing"}[trailing], func(t *testing.T) {
			a, data := mirrorFixture(t)
			if trailing {
				data = append(data, '!')
			} else {
				data = data[:len(data)-1]
			}
			client, _ := mirrorPipe(t)
			var result mirror.Result
			call(t, client, proto.OpMirrorApply, a, data, &result)
			if result.Status != "partial" || result.Failed == nil || !strings.Contains(result.Failed.Reason, "payload size mismatch") || len(result.Completed) != 0 || len(result.Pending) != 2 {
				t.Fatalf("invalid payload: %+v", result)
			}
			assertMirrorTargetPreserved(t, a.Path)
			call(t, client, proto.OpPing, nil, nil, nil)
		})
	}
}

func TestServeMirrorDisconnectBeforePayloadComplete(t *testing.T) {
	a, data := mirrorFixture(t)
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(server) }()
	defer client.Close()
	if err := proto.WriteFrameFrom(client, proto.Request{Op: proto.OpMirrorApply, Args: mustJSON(a)}, int64(len(data)), bytes.NewReader(data[:100])); err == nil {
		t.Fatal("expected short source error")
	}
	client.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("truncated frame accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop after truncated frame")
	}
	assertMirrorTargetPreserved(t, a.Path)
}

func TestMirrorCanceledBeforeApply(t *testing.T) {
	a, data := mirrorFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, _, err := Dispatch(ctx, proto.OpMirrorApply, mustJSON(a), data)
	if err != nil {
		t.Fatal(err)
	}
	if result.(mirror.Result).Status == "complete" {
		t.Fatalf("canceled apply completed: %+v", result)
	}
	assertMirrorTargetPreserved(t, a.Path)
}

func TestStageMirrorPayload(t *testing.T) {
	hash := sha256.Sum256([]byte("abc"))
	a := proto.MirrorApplyArgs{Path: t.TempDir(), Source: mirror.Manifest{Exists: true, Entries: []mirror.Entry{
		{Path: "file", Kind: "file", Size: 3, SHA256: hex.EncodeToString(hash[:])},
	}}}
	args := mustJSON(a)
	f, opErr, connErr := stageMirrorPayload(strings.NewReader("abc"), args, 3)
	if opErr != nil || connErr != nil || f == nil {
		t.Fatalf("stage: %v, %v", opErr, connErr)
	}
	name := f.Name()
	got, err := io.ReadAll(f)
	removeMirrorPayload(f)
	if err != nil || string(got) != "abc" {
		t.Fatalf("staged data: %q, %v", got, err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("payload temp file not removed: %v", err)
	}
	f, _, connErr = stageMirrorPayload(strings.NewReader("ab"), args, 3)
	if f != nil || connErr == nil {
		t.Fatalf("truncated payload accepted: %v, %v", f, connErr)
	}
	a.Source.Entries[0].Path = "../escape"
	f, opErr, connErr = stageMirrorPayload(strings.NewReader("abc"), mustJSON(a), 3)
	if f != nil || opErr == nil || !strings.Contains(opErr.Error(), "invalid relative path") || connErr != nil {
		t.Fatalf("invalid manifest: file=%v, op=%v, conn=%v", f, opErr, connErr)
	}
	a.Source.Entries = []mirror.Entry{
		{Path: "a", Kind: "file", Size: math.MaxInt64, SHA256: hex.EncodeToString(hash[:])},
		{Path: "b", Kind: "file", Size: 1, SHA256: hex.EncodeToString(hash[:])},
	}
	f, opErr, connErr = stageMirrorPayload(strings.NewReader(""), mustJSON(a), 0)
	if f != nil || opErr == nil || !strings.Contains(opErr.Error(), "exceeds supported range") || connErr != nil {
		t.Fatalf("payload size overflow: file=%v, op=%v, conn=%v", f, opErr, connErr)
	}
}

func assertMirrorTargetPreserved(t *testing.T, root string) {
	t.Helper()
	if got, err := os.ReadFile(filepath.Join(root, "old.dll")); err != nil || string(got) != "remove after copy" {
		t.Fatalf("obsolete file changed on failed apply: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "plugin.dll")); !os.IsNotExist(err) {
		t.Fatalf("new file exists after failed apply: %v", err)
	}
}
