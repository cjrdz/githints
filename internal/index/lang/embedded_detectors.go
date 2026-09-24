package lang

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"sync"
)

// detectorFS holds every framework detector shipped with the binary. Like
// language specs, adding one is adding a file.
//
//go:embed detectors/*.json
var detectorFS embed.FS

var (
	detectorOnce sync.Once
	detectorSet  *DetectorSet
	detectorErrs []error
)

// EmbeddedDetectors returns the compiled detectors built into this binary.
//
// Compiled once per process: a scan builds this for every file otherwise, and
// a DetectorSet is immutable.
func EmbeddedDetectors() (*DetectorSet, []error) {
	detectorOnce.Do(loadEmbeddedDetectors)
	return detectorSet, detectorErrs
}

func loadEmbeddedDetectors() {
	entries, err := fs.ReadDir(detectorFS, "detectors")
	if err != nil {
		detectorErrs = append(detectorErrs, fmt.Errorf("read embedded detectors: %w", err))
		detectorSet = &DetectorSet{}
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && path.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var detectors []Detector
	for _, name := range names {
		data, err := detectorFS.ReadFile(path.Join("detectors", name))
		if err != nil {
			detectorErrs = append(detectorErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		d, err := LoadDetector(data)
		if err != nil {
			detectorErrs = append(detectorErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		detectors = append(detectors, d)
	}

	set, err := NewDetectorSet(detectors)
	if err != nil {
		detectorErrs = append(detectorErrs, err)
		set = &DetectorSet{}
	}
	detectorSet = set
}
