//go:build unix

package safefs

import (
	"os"
	"syscall"
)

// linkCount returns how many names the open file has.
func linkCount(f *os.File) (uint64, bool) {
	fi, err := f.Stat()
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true //nolint:unconvert // Nlink is uint16 or uint32 depending on the platform
}
