//go:build !windows

package mirror

import (
	"fmt"
	"os"
)

func isReparse(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }

func removeDirectory(r *os.Root, name string) error {
	info, err := r.Lstat(name)
	if err != nil {
		return err
	}
	if isReparse(info) || !info.IsDir() {
		return fmt.Errorf("not a plain directory: %q", name)
	}
	return r.Remove(name)
}
