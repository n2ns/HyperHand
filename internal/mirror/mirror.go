// Package mirror implements bounded, verified directory mirror plans.
package mirror

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxEntries       = 4096
	MaxManifestBytes = 1 << 20
	bufferSize       = 1 << 20
)

type Entry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Exists  bool    `json:"exists"`
	Entries []Entry `json:"entries"`
}

type Change struct {
	Action string `json:"action"`
	Path   string `json:"path"`
}

type Failure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Result struct {
	Status    string   `json:"status"`
	Completed []Change `json:"completed"`
	Failed    *Failure `json:"failed,omitempty"`
	Pending   []Change `json:"pending"`
}

func key(p string) string { return strings.ToLower(p) }

func validComponent(s string) bool {
	if s == "" || s == "." || s == ".." || strings.TrimRight(s, " .") != s || strings.ContainsAny(s, `\/:<>"|?*`) {
		return false
	}
	for _, c := range s {
		if c < 32 {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(s, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" {
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return false
	}
	return true
}

func validRelative(p string) bool {
	if len(p) > 32760 || p == "" {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if !validComponent(s) {
			return false
		}
	}
	return true
}

// ValidateManifest rejects incomplete, ambiguous or unbounded inventories.
func ValidateManifest(m Manifest) error {
	if len(m.Entries) > MaxEntries {
		return fmt.Errorf("manifest exceeds %d entries", MaxEntries)
	}
	if !m.Exists && len(m.Entries) != 0 {
		return errors.New("missing directory has nonempty manifest")
	}
	seen := make(map[string]Entry, len(m.Entries))
	for _, e := range m.Entries {
		if !validRelative(e.Path) {
			return fmt.Errorf("invalid relative path %q", e.Path)
		}
		if _, ok := seen[key(e.Path)]; ok {
			return fmt.Errorf("case-insensitive path collision at %q", e.Path)
		}
		switch e.Kind {
		case "dir":
			if e.Size != 0 || e.SHA256 != "" {
				return fmt.Errorf("invalid directory metadata for %q", e.Path)
			}
		case "file":
			h, err := hex.DecodeString(e.SHA256)
			if e.Size < 0 || err != nil || len(h) != sha256.Size || strings.ToLower(e.SHA256) != e.SHA256 {
				return fmt.Errorf("invalid file metadata for %q", e.Path)
			}
		default:
			return fmt.Errorf("invalid entry kind %q for %q", e.Kind, e.Path)
		}
		seen[key(e.Path)] = e
	}
	for _, e := range m.Entries {
		if p := path.Dir(e.Path); p != "." {
			parent, ok := seen[key(p)]
			if !ok || parent.Kind != "dir" {
				return fmt.Errorf("missing directory parent for %q", e.Path)
			}
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > MaxManifestBytes {
		return fmt.Errorf("manifest exceeds %d JSON bytes", MaxManifestBytes)
	}
	return nil
}

// validateRoot also inspects every ancestor: OpenRoot by itself follows links in
// its argument, so a root reached through a reparse point must be rejected first.
func validateRoot(root string, allowMissing bool) (string, bool, error) {
	if !filepath.IsAbs(root) || strings.HasPrefix(root, `\\`) || strings.HasPrefix(root, "//") {
		return "", false, errors.New("mirror root must be an absolute local directory, not UNC or device path")
	}
	root = filepath.FromSlash(root)
	volume := filepath.VolumeName(root)
	rel := strings.TrimLeft(strings.TrimPrefix(root, volume), string(filepath.Separator))
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		if c != "" && !validComponent(c) {
			return "", false, fmt.Errorf("invalid mirror root component %q", c)
		}
	}
	root = filepath.Clean(root)
	if filepath.Dir(root) == root {
		return "", false, errors.New("filesystem root cannot be mirrored")
	}
	var ancestors []string
	for p := root; ; p = filepath.Dir(p) {
		ancestors = append(ancestors, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		info, err := os.Lstat(ancestors[i])
		if err != nil {
			if i == 0 && allowMissing && errors.Is(err, os.ErrNotExist) {
				return root, false, nil
			}
			return "", false, fmt.Errorf("inspect %q: %w", ancestors[i], err)
		}
		if isReparse(info) || !info.IsDir() {
			return "", false, fmt.Errorf("root or ancestor %q is not a plain directory", ancestors[i])
		}
	}
	return root, true, nil
}

// openPlainRoot opens each ancestor through an already bounded handle, checking
// the opened directory's identity against its non-reparse directory entry.
func openPlainRoot(root string) (*os.Root, error) {
	root = filepath.Clean(filepath.FromSlash(root))
	anchor := filepath.VolumeName(root) + string(filepath.Separator)
	r, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, err
	}
	rel := strings.TrimPrefix(root, anchor)
	if rel == "" {
		return r, nil
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		child, err := openPlainChild(r, component)
		r.Close()
		if err != nil {
			return nil, err
		}
		r = child
	}
	return r, nil
}

func openPlainChild(parent *os.Root, component string) (*os.Root, error) {
	before, err := parent.Lstat(component)
	if err != nil {
		return nil, err
	}
	if isReparse(before) || !before.IsDir() {
		return nil, fmt.Errorf("not a plain directory: %q", component)
	}
	child, err := parent.OpenRoot(component)
	if err != nil {
		return nil, err
	}
	opened, err := child.Stat(".")
	if err != nil {
		child.Close()
		return nil, err
	}
	after, err := parent.Lstat(component)
	if err != nil {
		child.Close()
		return nil, err
	}
	if isReparse(after) || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		child.Close()
		return nil, fmt.Errorf("directory changed while opening %q", component)
	}
	return child, nil
}

// Scan produces a full inventory or an error; it never returns a truncated tree.
func Scan(ctx context.Context, root string, allowMissing bool) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	p, exists, err := validateRoot(root, allowMissing)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Exists: exists, Entries: []Entry{}}
	if !exists {
		return m, nil
	}
	r, err := openPlainRoot(p)
	if err != nil {
		return Manifest{}, err
	}
	defer r.Close()
	encodedBytes := len(`{"exists":true,"entries":[]}`)
	var walk func(string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := plainPath(r, dir, false); err != nil {
			return err
		}
		f, err := r.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			entries, readErr := f.ReadDir(128)
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			for _, d := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				name := d.Name()
				if dir != "." {
					name = dir + "/" + name
				}
				if !validRelative(name) {
					return fmt.Errorf("unsupported path %q", name)
				}
				info, err := r.Lstat(name)
				if err != nil {
					return err
				}
				if isReparse(info) {
					return fmt.Errorf("reparse point or symbolic link is forbidden: %q", name)
				}
				e := Entry{Path: name, Kind: "dir"}
				if !info.IsDir() {
					if !info.Mode().IsRegular() {
						return fmt.Errorf("not a regular file: %q", name)
					}
					e.Kind = "file"
					e.SHA256, e.Size, err = hashFile(ctx, r, name)
					if err != nil {
						return err
					}
				}
				m.Entries = append(m.Entries, e)
				if len(m.Entries) > MaxEntries {
					return fmt.Errorf("manifest exceeds %d entries", MaxEntries)
				}
				encoded, err := json.Marshal(e)
				if err != nil {
					return err
				}
				encodedBytes += len(encoded)
				if len(m.Entries) > 1 {
					encodedBytes++
				}
				if encodedBytes > MaxManifestBytes {
					return fmt.Errorf("manifest exceeds %d JSON bytes", MaxManifestBytes)
				}
				if info.IsDir() {
					if err := walk(name); err != nil {
						return err
					}
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return Manifest{}, err
	}
	sort.Slice(m.Entries, func(i, j int) bool { return key(m.Entries[i].Path) < key(m.Entries[j].Path) })
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func entriesByPath(m Manifest) map[string]Entry {
	result := make(map[string]Entry, len(m.Entries))
	for _, e := range m.Entries {
		result[key(e.Path)] = e
	}
	return result
}

// Equal compares content with Windows case-insensitive path semantics.
func Equal(a, b Manifest) bool {
	if ValidateManifest(a) != nil || ValidateManifest(b) != nil || a.Exists != b.Exists || len(a.Entries) != len(b.Entries) {
		return false
	}
	entries := entriesByPath(b)
	for _, e := range a.Entries {
		other, ok := entries[key(e.Path)]
		if !ok || e.Kind != other.Kind || e.Size != other.Size || e.SHA256 != other.SHA256 {
			return false
		}
	}
	return true
}

func Diff(source, target Manifest, force bool) ([]Change, error) {
	if err := ValidateManifest(source); err != nil {
		return nil, err
	}
	if err := ValidateManifest(target); err != nil {
		return nil, err
	}
	if !source.Exists {
		return nil, errors.New("source directory does not exist")
	}
	src, dst := entriesByPath(source), entriesByPath(target)
	ordered := append([]Entry(nil), source.Entries...)
	sort.Slice(ordered, func(i, j int) bool {
		di, dj := strings.Count(ordered[i].Path, "/"), strings.Count(ordered[j].Path, "/")
		if di != dj {
			return di < dj
		}
		return key(ordered[i].Path) < key(ordered[j].Path)
	})
	actual := map[string]string{".": "."}
	changes := []Change{}
	if !target.Exists {
		changes = append(changes, Change{"mkdir", "."})
	}
	for _, e := range ordered {
		other, exists := dst[key(e.Path)]
		if exists && e.Kind != other.Kind {
			return nil, fmt.Errorf("file/directory type conflict at %q", e.Path)
		}
		p := e.Path
		if exists {
			p = other.Path
		} else if parent := path.Dir(p); parent != "." {
			p = actual[key(parent)] + "/" + path.Base(p)
		}
		actual[key(e.Path)] = p
		if e.Kind == "dir" {
			if !exists {
				changes = append(changes, Change{"mkdir", p})
			}
			continue
		}
		action := "copy"
		if exists {
			action = "overwrite"
			if !force && e.Size == other.Size && e.SHA256 == other.SHA256 {
				action = "skip"
			}
		}
		changes = append(changes, Change{action, p})
	}
	var deleted []Change
	for _, e := range target.Entries {
		if _, ok := src[key(e.Path)]; !ok {
			action := "delete_file"
			if e.Kind == "dir" {
				action = "delete_dir"
			}
			deleted = append(deleted, Change{action, e.Path})
		}
	}
	sort.Slice(deleted, func(i, j int) bool {
		if deleted[i].Action != deleted[j].Action {
			return deleted[i].Action == "delete_file"
		}
		di, dj := strings.Count(deleted[i].Path, "/"), strings.Count(deleted[j].Path, "/")
		if di != dj {
			return di > dj
		}
		return key(deleted[i].Path) < key(deleted[j].Path)
	})
	if err := ValidateManifest(beforeDeletion(source, target)); err != nil {
		return nil, fmt.Errorf("intermediate mirror tree exceeds scan limits: %w", err)
	}
	return append(changes, deleted...), nil
}

func beforeDeletion(source, target Manifest) Manifest {
	expected := Manifest{Exists: true, Entries: append([]Entry(nil), source.Entries...)}
	entries := entriesByPath(source)
	for _, e := range target.Entries {
		if _, ok := entries[key(e.Path)]; !ok {
			expected.Entries = append(expected.Entries, e)
		}
	}
	return expected
}

func plainPath(r *os.Root, name string, allowMissing bool) error {
	if name != "." && !validRelative(name) {
		return fmt.Errorf("invalid relative path %q", name)
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		info, err := r.Lstat(p)
		if err != nil {
			if allowMissing && i == len(parts)-1 && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if isReparse(info) {
			return fmt.Errorf("reparse point or symbolic link is forbidden: %q", p)
		}
		if i != len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("not a directory: %q", p)
		}
	}
	return nil
}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, bufferSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return total, ctxErr
			}
			wn, we := w.Write(buf[:n])
			total += int64(wn)
			if we != nil {
				return total, we
			}
			if wn != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func hashFile(ctx context.Context, r *os.Root, name string) (string, int64, error) {
	if err := plainPath(r, name, false); err != nil {
		return "", 0, err
	}
	f, err := r.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() {
		return "", 0, fmt.Errorf("not a regular file: %q", name)
	}
	h := sha256.New()
	n, err := copyContext(ctx, h, f)
	if err != nil {
		return "", 0, err
	}
	after, err := r.Lstat(name)
	if err != nil {
		return "", 0, err
	}
	if isReparse(after) || !os.SameFile(before, after) || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", 0, fmt.Errorf("file changed during scan: %q", name)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// WritePayload writes exactly the files required by the plan in plan order.
func WritePayload(ctx context.Context, w io.Writer, root string, source Manifest, changes []Change) error {
	current, err := Scan(ctx, root, false)
	if err != nil {
		return err
	}
	if !Equal(source, current) {
		return errors.New("source changed since mirror plan; create a new plan")
	}
	r, err := openPlainRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	entries := entriesByPath(source)
	for _, c := range changes {
		if c.Action != "copy" && c.Action != "overwrite" {
			continue
		}
		e, ok := entries[key(c.Path)]
		if !ok || e.Kind != "file" {
			return fmt.Errorf("invalid payload change %q", c.Path)
		}
		if err := plainPath(r, e.Path, false); err != nil {
			return err
		}
		f, err := r.Open(e.Path)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := copyContext(ctx, io.MultiWriter(w, h), io.LimitReader(f, e.Size))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
			return fmt.Errorf("source changed while transferring %q; create a new plan", e.Path)
		}
	}
	current, err = Scan(ctx, root, false)
	if err != nil {
		return err
	}
	if !Equal(source, current) {
		return errors.New("source changed while transferring; create a new plan")
	}
	return nil
}

// Apply stages and verifies the complete payload before changing the target.
// Replacements use a staged file; neither each replacement nor the directory
// transaction is guaranteed atomic on every platform.
// The payload reader must end at this request's payload boundary.
func Apply(ctx context.Context, root string, source, target Manifest, force bool, payload io.Reader) Result {
	result := Result{Status: "partial", Completed: []Change{}, Pending: []Change{}}
	fail := func(p string, err error) Result { result.Failed = &Failure{p, err.Error()}; return result }
	changes, err := Diff(source, target, force)
	if err != nil {
		return fail(root, err)
	}
	result.Pending = append(result.Pending, changes...)
	if err := ctx.Err(); err != nil {
		return fail(root, err)
	}
	root, _, err = validateRoot(root, true)
	if err != nil {
		return fail(root, err)
	}
	stagePath, err := os.MkdirTemp("", "hyperhand-mirror-")
	if err != nil {
		return fail(root, err)
	}
	defer os.RemoveAll(stagePath) // Only our private staging directory, never target data.
	stage, err := os.OpenRoot(stagePath)
	if err != nil {
		return fail(root, err)
	}
	defer stage.Close()
	entries := entriesByPath(source)
	staged := make(map[string]string)
	for i, c := range changes {
		if c.Action != "copy" && c.Action != "overwrite" {
			continue
		}
		e := entries[key(c.Path)]
		name := fmt.Sprintf("%d", i)
		f, err := stage.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fail(c.Path, err)
		}
		h := sha256.New()
		n, err := copyContext(ctx, io.MultiWriter(f, h), io.LimitReader(payload, e.Size))
		closeErr := f.Close()
		if err != nil {
			return fail(c.Path, err)
		}
		if closeErr != nil {
			return fail(c.Path, closeErr)
		}
		if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
			return fail(c.Path, errors.New("payload size or SHA-256 mismatch; no target changes applied"))
		}
		staged[key(c.Path)] = name
	}
	if err := ctx.Err(); err != nil {
		return fail(root, err)
	}
	var extra [1]byte
	if n, err := io.ReadFull(payload, extra[:]); n != 0 || (err != nil && err != io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing payload bytes")
		}
		return fail(root, err)
	}
	current, err := Scan(ctx, root, true)
	if err != nil {
		return fail(root, err)
	}
	if !Equal(current, target) {
		result.Status = "plan_stale"
		return fail(root, errors.New("target changed since mirror plan; create a new plan"))
	}
	if err := ctx.Err(); err != nil {
		return fail(root, err)
	}
	parent, err := openPlainRoot(filepath.Dir(root))
	if err != nil {
		return fail(root, err)
	}
	defer parent.Close()
	if !target.Exists {
		if err := parent.Mkdir(filepath.Base(root), 0755); err != nil {
			result.Status = "plan_stale"
			return fail(root, err)
		}
		result.Completed = append(result.Completed, changes[0])
		result.Pending = changes[1:]
	}
	if _, _, err := validateRoot(root, false); err != nil {
		return fail(root, err)
	}
	r, err := openPlainChild(parent, filepath.Base(root))
	if err != nil {
		return fail(root, err)
	}
	defer r.Close()
	oldEntries := entriesByPath(target)
	verifiedBeforeDelete := false
	for i, c := range changes {
		if c.Action == "mkdir" && c.Path == "." {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fail(c.Path, err)
		}
		if !verifiedBeforeDelete && (c.Action == "delete_file" || c.Action == "delete_dir") {
			current, err = Scan(ctx, root, false)
			if err != nil {
				return fail(c.Path, err)
			}
			if !Equal(beforeDeletion(source, target), current) {
				return fail(c.Path, errors.New("target changed or copied files failed verification; extra entries preserved"))
			}
			verifiedBeforeDelete = true
		}
		if err := plainPath(r, c.Path, c.Action == "mkdir" || c.Action == "copy"); err != nil {
			return fail(c.Path, err)
		}
		switch c.Action {
		case "mkdir":
			err = r.Mkdir(c.Path, 0755)
		case "copy", "overwrite":
			var old *Entry
			if e, ok := oldEntries[key(c.Path)]; ok {
				old = &e
			}
			err = installFile(ctx, r, stage, c.Path, staged[key(c.Path)], entries[key(c.Path)], old)
		case "skip":
			e := entries[key(c.Path)]
			err = checkExpected(ctx, r, c.Path, &e)
		case "delete_file":
			e := oldEntries[key(c.Path)]
			err = checkExpected(ctx, r, c.Path, &e)
			if err == nil {
				err = r.Remove(c.Path)
			}
		case "delete_dir":
			err = removeDirectory(r, c.Path)
		}
		if err != nil {
			return fail(c.Path, err)
		}
		result.Completed = append(result.Completed, c)
		result.Pending = changes[i+1:]
	}
	current, err = Scan(ctx, root, false)
	if err != nil {
		return fail(root, err)
	}
	if !Equal(source, current) {
		return fail(root, errors.New("target verification failed after apply; create a new plan"))
	}
	result.Status = "complete"
	return result
}

func checkExpected(ctx context.Context, r *os.Root, name string, expected *Entry) error {
	if err := plainPath(r, name, expected == nil); err != nil {
		return err
	}
	if expected == nil {
		if _, err := r.Lstat(name); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return fmt.Errorf("target %q appeared during apply; preserved", name)
	}
	h, size, err := hashFile(ctx, r, name)
	if err != nil {
		return err
	}
	if h != expected.SHA256 || size != expected.Size {
		return fmt.Errorf("target %q changed during apply; preserved", name)
	}
	return nil
}

func installFile(ctx context.Context, dst, stage *os.Root, name, staged string, expected Entry, old *Entry) error {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(name), ".hyperhand-"+hex.EncodeToString(token[:])+".hhpart")
	out, err := dst.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer dst.Remove(tmp)
	in, err := stage.Open(staged)
	if err != nil {
		out.Close()
		return err
	}
	h := sha256.New()
	n, err := copyContext(ctx, io.MultiWriter(out, h), in)
	in.Close()
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		return errors.New("staged file verification failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkExpected(ctx, dst, tmp, &expected); err != nil {
		return err
	}
	if err := checkExpected(ctx, dst, name, old); err != nil {
		return err
	}
	return dst.Rename(tmp, name)
}
