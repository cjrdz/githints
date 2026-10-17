//go:build !unix && !windows

package safefs

import "os"

// linkCount is unknown on this platform, so a hard link is not detected.
func linkCount(*os.File) (uint64, bool) { return 0, false }
