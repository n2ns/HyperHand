//go:build windows

package mirror

import (
	"fmt"
	"os"
	"path"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func isReparse(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// Bind deletion to an opened directory, using FILE_DIRECTORY_FILE so a file
// that replaced the directory cannot be accidentally removed. The parent handle
// comes from os.Root; no absolute path is passed to the native filesystem call.
func removeDirectory(r *os.Root, name string) error {
	if err := plainPath(r, name, false); err != nil {
		return err
	}
	parent, err := r.Open(path.Dir(name))
	if err != nil {
		return err
	}
	defer parent.Close()
	leaf, err := windows.NewNTUnicodeString(path.Base(name))
	if err != nil {
		return err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: leaf, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.DELETE|windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES, &oa, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return fmt.Errorf("open directory for deletion %q: %w", name, err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("reparse point is forbidden: %q", name)
	}
	deleteOnClose := byte(1)
	if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &deleteOnClose, 1); err != nil {
		return fmt.Errorf("remove empty directory %q: %w", name, err)
	}
	return nil
}
