//go:build windows

package mirror

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestFailedReplacementPreservesExtra(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	put(t, source, "a-new.dll", "new")
	put(t, source, "z-locked.dll", "replacement")
	put(t, target, "z-locked.dll", "original")
	put(t, target, "extra.dll", "keep")
	src, dst, _, payload := plannedPayload(t, source, target, false)
	p, err := windows.UTF16PtrFromString(filepath.Join(target, "z-locked.dll"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	r := Apply(context.Background(), target, src, dst, false, bytes.NewReader(payload))
	if r.Status != "partial" || r.Failed == nil || r.Failed.Path != "z-locked.dll" || len(r.Completed) != 1 {
		t.Fatalf("%+v", r)
	}
	for name, want := range map[string]string{"a-new.dll": "new", "z-locked.dll": "original", "extra.dll": "keep"} {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 3 {
		t.Fatalf("temporary file leaked: %v %v", entries, err)
	}
}

func TestScanRejectsJunctionAndAncestorJunction(t *testing.T) {
	outside, base := t.TempDir(), t.TempDir()
	put(t, outside, "sentinel", "keep")
	junction := filepath.Join(base, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Skipf("mklink /J unavailable: %v %s", err, out)
	}
	for _, root := range []string{base, junction, filepath.Join(junction, "absent")} {
		if _, err := Scan(context.Background(), root, true); err == nil {
			t.Errorf("accepted junction path %q", root)
		}
	}
	source := t.TempDir()
	src := scan(t, source)
	r := Apply(context.Background(), junction, src, Manifest{Exists: true}, false, bytes.NewReader(nil))
	if r.Status == "complete" {
		t.Fatal("applied through junction")
	}
	got, err := os.ReadFile(filepath.Join(outside, "sentinel"))
	if err != nil || string(got) != "keep" {
		t.Fatalf("outside modified: %q %v", got, err)
	}
}

func TestDirectoryDeletionPreservesReplacementFile(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	put(t, target, "a-extra.dll", "old")
	if err := os.Mkdir(filepath.Join(target, "z-directory"), 0755); err != nil {
		t.Fatal(err)
	}
	src, dst, _, payload := plannedPayload(t, source, target, false)
	changed := false
	ctx := observingContext{context.Background(), func() error {
		if !changed {
			if _, err := os.Stat(filepath.Join(target, "a-extra.dll")); os.IsNotExist(err) {
				changed = true
				if err := os.Remove(filepath.Join(target, "z-directory")); err != nil {
					t.Fatal(err)
				}
				put(t, target, "z-directory", "new external file")
			}
		}
		return nil
	}}
	r := Apply(ctx, target, src, dst, false, bytes.NewReader(payload))
	if r.Status != "partial" || r.Failed == nil || r.Failed.Path != "z-directory" {
		t.Fatalf("%+v", r)
	}
	got, err := os.ReadFile(filepath.Join(target, "z-directory"))
	if err != nil || string(got) != "new external file" {
		t.Fatalf("replacement deleted: %q %v", got, err)
	}
}
