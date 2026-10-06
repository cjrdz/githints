package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version, commit and date are stamped at link time by goreleaser:
//
//	-X main.version=... -X main.commit=... -X main.date=...
//
// A plain `go build` leaves them empty, and versionString falls back to the
// VCS information the Go toolchain embeds on its own.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// versionString is what `githints version` prints: enough to tell which
// build a bug report came from.
func versionString() string {
	c, d, dirty := commit, date, false
	if info, ok := debug.ReadBuildInfo(); ok && c == "" {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				c = s.Value
			case "vcs.time":
				d = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if len(c) > 12 {
		c = c[:12]
	}
	if c == "" {
		c = "unknown"
	}
	if dirty {
		c += "-dirty"
	}
	if d == "" {
		d = "unknown"
	}
	return fmt.Sprintf("githints %s (commit %s, built %s, %s %s/%s)", version, c, d, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
