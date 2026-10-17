//go:build windows

package safefs

import (
	"os"
	"syscall"
)

// linkCount returns how many names the open file has. Go's FileInfo carries no
// link count on Windows, so it is read from the handle.
func linkCount(f *os.File) (uint64, bool) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, false
	}
	var n uint64
	var ok bool
	if err := rc.Control(func(fd uintptr) {
		var info syscall.ByHandleFileInformation
		if syscall.GetFileInformationByHandle(syscall.Handle(fd), &info) == nil {
			n, ok = uint64(info.NumberOfLinks), true
		}
	}); err != nil {
		return 0, false
	}
	return n, ok
}
