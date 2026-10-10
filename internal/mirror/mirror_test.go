package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func put(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func scan(t *testing.T, root string) Manifest {
	t.Helper()
	m, err := Scan(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func plannedPayload(t *testing.T, source, target string, force bool) (Manifest, Manifest, []Change, []byte) {
	t.Helper()
	src, dst := scan(t, source), scan(t, target)
	changes, err := Diff(src, dst, force)
	if err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	if err := WritePayload(context.Background(), &payload, source, src, changes); err != nil {
		t.Fatal(err)
	}
	return src, dst, changes, payload.Bytes()
}

func fileEntry(p, content string) Entry {
	h := sha256.Sum256([]byte(content))
	return Entry{Path: p, Kind: "file", Size: int64(len(content)), SHA256: hex.EncodeToString(h[:])}
}

func TestMirrorRoundTrip(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	put(t, source, "same.dll", "same")
	put(t, target, "same.dll", "same")
	put(t, source, "change.dll", "new")
	put(t, target, "change.dll", "old")
	put(t, source, "nested/new.dll", strings.Repeat("data", (1<<20)+13))
	if err := os.MkdirAll(filepath.Join(source, "empty", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	put(t, target, "extra/deep/old.dll", "old")
	src, dst, changes, payload := plannedPayload(t, source, target, false)
	want := map[string]string{"same.dll": "skip", "change.dll": "overwrite", "nested/new.dll": "copy", "empty": "mkdir", "empty/nested": "mkdir", "extra/deep/old.dll": "delete_file", "extra/deep": "delete_dir", "extra": "delete_dir"}
	for _, c := range changes {
		if action, ok := want[c.Path]; ok {
			if c.Action != action {
				t.Errorf("%+v, want %s", c, action)
			}
			delete(want, c.Path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing changes: %v", want)
	}
	r := Apply(context.Background(), target, src, dst, false, bytes.NewReader(payload))
	if r.Status != "complete" || r.Failed != nil || len(r.Pending) != 0 || !reflect.DeepEqual(changes, r.Completed) {
		t.Fatalf("result: %+v failure: %+v", r, r.Failed)
	}
	if !Equal(src, scan(t, target)) {
		t.Fatal("target does not match source")
	}
}

func TestEmptySourceAndMissingTarget(t *testing.T) {
	t.Run("empty source removes contents but keeps root", func(t *testing.T) {
		source, target := t.TempDir(), t.TempDir()
		put(t, target, "deep/old.dll", "old")
		src, dst, _, payload := plannedPayload(t, source, target, false)
		r := Apply(context.Background(), target, src, dst, false, bytes.NewReader(payload))
		if r.Status != "complete" {
			t.Fatalf("%+v failure: %+v", r, r.Failed)
		}
		if got := scan(t, target); !got.Exists || len(got.Entries) != 0 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("missing root appears in plan", func(t *testing.T) {
		source, target := t.TempDir(), filepath.Join(t.TempDir(), "new")
		src, dst, changes, payload := plannedPayload(t, source, target, false)
		if !reflect.DeepEqual(changes, []Change{{"mkdir", "."}}) {
			t.Fatalf("%+v", changes)
		}
		r := Apply(context.Background(), target, src, dst, false, bytes.NewReader(payload))
		if r.Status != "complete" || !reflect.DeepEqual(changes, r.Completed) {
			t.Fatalf("%+v", r)
		}
	})
}

func TestCaseInsensitivePathsAndForce(t *testing.T) {
	src := Manifest{Exists: true, Entries: []Entry{{Path: "Plugins", Kind: "dir"}, fileEntry("Plugins/Test.DLL", "same"), fileEntry("Plugins/New.DLL", "new")}}
	dst := Manifest{Exists: true, Entries: []Entry{{Path: "plugins", Kind: "dir"}, fileEntry("plugins/test.dll", "same")}}
	changes, err := Diff(src, dst, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changes, []Change{{"copy", "plugins/New.DLL"}, {"skip", "plugins/test.dll"}}) {
		t.Fatalf("%+v", changes)
	}
	changes, err = Diff(src, dst, true)
	if err != nil {
		t.Fatal(err)
	}
	if changes[1].Action != "overwrite" {
		t.Fatalf("force changes: %+v", changes)
	}
	if !Equal(Manifest{Exists: true, Entries: []Entry{fileEntry("A.DLL", "same")}}, Manifest{Exists: true, Entries: []Entry{fileEntry("a.dll", "same")}}) {
		t.Fatal("case-only difference must be equal")
	}
}

func TestInvalidManifests(t *testing.T) {
	cases := map[string]Manifest{
		"collision":            {Exists: true, Entries: []Entry{fileEntry("A", "1"), fileEntry("a", "2")}},
		"missing parent":       {Exists: true, Entries: []Entry{fileEntry("parent/file", "x")}},
		"file parent":          {Exists: true, Entries: []Entry{fileEntry("parent", "x"), fileEntry("parent/file", "x")}},
		"kind":                 {Exists: true, Entries: []Entry{{Path: "x", Kind: "link"}}},
		"bad hash":             {Exists: true, Entries: []Entry{{Path: "x", Kind: "file", SHA256: "bad"}}},
		"missing with entries": {Entries: []Entry{fileEntry("x", "x")}},
	}
	for _, p := range []string{"../escape", "/absolute", "a/../b", "a\\b", "x:stream", "NUL", "COM1.txt", "file.", "file ", "a//b"} {
		cases[p] = Manifest{Exists: true, Entries: []Entry{fileEntry(p, "x")}}
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateManifest(m); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
	t.Run("entry limit", func(t *testing.T) {
		m := Manifest{Exists: true}
		for i := 0; i <= MaxEntries; i++ {
			m.Entries = append(m.Entries, fileEntry(fmt.Sprint(i), "x"))
		}
		if err := ValidateManifest(m); err == nil {
			t.Fatal("accepted too many entries")
		}
	})
	t.Run("byte limit", func(t *testing.T) {
		m := Manifest{Exists: true}
		for i := 0; i < 4000; i++ {
			m.Entries = append(m.Entries, fileEntry(fmt.Sprint(i)+strings.Repeat("a", 220), "x"))
		}
		if err := ValidateManifest(m); err == nil || !strings.Contains(err.Error(), "JSON bytes") {
			t.Fatalf("%v", err)
		}
	})
}

func TestTypeConflict(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		src := Manifest{Exists: true, Entries: []Entry{fileEntry("thing", "x")}}
		dst := Manifest{Exists: true, Entries: []Entry{{Path: "thing", Kind: "dir"}}}
		if reverse {
			src, dst = dst, src
		}
		if _, err := Diff(src, dst, false); err == nil {
			t.Fatal("type conflict accepted")
		}
	}
}

func TestPayloadFailurePreservesTarget(t *testing.T) {
	for _, kind := range []string{"short", "trailing", "corrupt", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			put(t, source, "new.dll", "new content")
			put(t, target, "old.dll", "keep")
			src, dst, _, payload := plannedPayload(t, source, target, false)
			ctx := context.Background()
			switch kind {
			case "short":
				payload = payload[:len(payload)-1]
			case "trailing":
				payload = append(payload, '!')
			case "corrupt":
				payload[0] ^= 1
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r := Apply(ctx, target, src, dst, false, bytes.NewReader(payload))
			if r.Status != "partial" || r.Failed == nil || len(r.Completed) != 0 || len(r.Pending) == 0 {
				t.Fatalf("%+v", r)
			}
			if !Equal(dst, scan(t, target)) {
				t.Fatal("target changed on failed payload")
			}
		})
	}
}

type eofAction struct {
	io.Reader
	action func()
}

func (r *eofAction) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && r.action != nil {
		r.action()
		r.action = nil
	}
	return n, err
}

func TestTargetDriftDuringPayload(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	put(t, source, "new.dll", "new")
	put(t, target, "old.dll", "old")
	src, dst, _, payload := plannedPayload(t, source, target, false)
	reader := &eofAction{bytes.NewReader(payload), func() { put(t, target, "old.dll", "changed") }}
	r := Apply(context.Background(), target, src, dst, false, reader)
	if r.Status != "plan_stale" || len(r.Completed) != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(target, "new.dll")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new file created: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "old.dll"))
	if err != nil || string(got) != "changed" {
		t.Fatalf("old file not preserved: %q %v", got, err)
	}
}

type actionWriter struct {
	io.Writer
	action func()
}

func (w *actionWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if w.action != nil {
		w.action()
		w.action = nil
	}
	return n, err
}

func TestSourceDrift(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprint(during), func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			put(t, source, "copy", "new")
			put(t, source, "skip", "same")
			put(t, target, "skip", "same")
			src, dst := scan(t, source), scan(t, target)
			changes, err := Diff(src, dst, false)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			var writer io.Writer = &out
			if during {
				writer = &actionWriter{&out, func() { put(t, source, "skip", "drift") }}
			} else {
				put(t, source, "copy", "drift")
			}
			if err := WritePayload(context.Background(), writer, source, src, changes); err == nil {
				t.Fatal("source drift accepted")
			}
			if !during && out.Len() != 0 {
				t.Fatal("stale source payload emitted")
			}
		})
	}
}

type observingContext struct {
	context.Context
	observe func() error
}

func (c observingContext) Err() error {
	if err := c.observe(); err != nil {
		return err
	}
	return c.Context.Err()
}

func TestReverifyBeforeDeletion(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	put(t, source, "new.dll", "new")
	put(t, target, "old.dll", "keep")
	src, dst, _, payload := plannedPayload(t, source, target, false)
	changed := false
	ctx := observingContext{context.Background(), func() error {
		if !changed {
			if _, err := os.Stat(filepath.Join(target, "new.dll")); err == nil {
				changed = true
				put(t, target, "new.dll", "external change")
			}
		}
		return nil
	}}
	r := Apply(ctx, target, src, dst, false, bytes.NewReader(payload))
	if r.Status != "partial" || r.Failed == nil || !strings.Contains(r.Failed.Reason, "preserved") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(target, "old.dll")); err != nil {
		t.Fatalf("extra file deleted: %v", err)
	}
}

func TestCancellationAfterCopyPreservesExtra(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	put(t, source, "new.dll", "new")
	put(t, target, "old.dll", "keep")
	src, dst, _, payload := plannedPayload(t, source, target, false)
	ctx := observingContext{context.Background(), func() error {
		if _, err := os.Stat(filepath.Join(target, "new.dll")); err == nil {
			return context.Canceled
		}
		return nil
	}}
	r := Apply(ctx, target, src, dst, false, bytes.NewReader(payload))
	if r.Status != "partial" || r.Failed == nil || len(r.Completed) != 1 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(target, "old.dll")); err != nil {
		t.Fatalf("extra file deleted: %v", err)
	}
}

func TestRootBoundaries(t *testing.T) {
	base := t.TempDir()
	put(t, base, "file", "x")
	roots := []string{"relative", filepath.VolumeName(base) + string(filepath.Separator), filepath.Join(base, "missing", "child"), filepath.Join(base, "file"), base + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape", `\\server\share\directory`, `\\?\C:\directory`, filepath.Join(base, "file:stream")}
	for _, root := range roots {
		if _, err := Scan(context.Background(), root, true); err == nil {
			t.Errorf("accepted unsafe root %q", root)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := Scan(context.Background(), filepath.ToSlash(base), false); err != nil {
			t.Fatalf("forward slash root: %v", err)
		}
	}
	if _, err := Scan(context.Background(), filepath.Join(base, "absent"), false); err == nil {
		t.Fatal("missing source accepted")
	}
}

func TestScanRejectsLinksAndAncestorLinks(t *testing.T) {
	outside, base := t.TempDir(), t.TempDir()
	put(t, outside, "sentinel", "keep")
	link := filepath.Join(base, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	for _, root := range []string{base, link, filepath.Join(link, "absent")} {
		if _, err := Scan(context.Background(), root, true); err == nil {
			t.Errorf("accepted link path %q", root)
		}
	}
}

func TestScanEntryLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= MaxEntries; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprint(i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Scan(context.Background(), root, false); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("%v", err)
	}
}

func TestInstallPreservesConcurrentTarget(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			target, stageDir := t.TempDir(), t.TempDir()
			put(t, stageDir, "staged", "new")
			var old *Entry
			if existing {
				e := fileEntry("file", "old")
				old = &e
			}
			put(t, target, "file", "external change")
			dst, err := os.OpenRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			defer dst.Close()
			stage, err := os.OpenRoot(stageDir)
			if err != nil {
				t.Fatal(err)
			}
			defer stage.Close()
			if err := installFile(context.Background(), dst, stage, "file", "staged", fileEntry("file", "new"), old); err == nil {
				t.Fatal("concurrent target overwritten")
			}
			got, err := os.ReadFile(filepath.Join(target, "file"))
			if err != nil || string(got) != "external change" {
				t.Fatalf("%q %v", got, err)
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary file leaked: %v %v", entries, err)
			}
		})
	}
}
