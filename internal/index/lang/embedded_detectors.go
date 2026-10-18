package lang

import (
	"fmt"
	"sync"
)

var (
	detectorOnce sync.Once
	detectorSet  *DetectorSet
	detectorErrs []error
)

// EmbeddedDetectors returns the detectors built into this binary.
//
// Loaded once per process, and each detector compiled the first time a file
// of its language is checked: a hook run touching one Go file compiles the Go
// detectors and nothing else. TestEmbeddedDetectorsAreValid compiles them
// all, so an invalid one fails the build rather than a user's scan.
func EmbeddedDetectors() (*DetectorSet, []error) {
	detectorOnce.Do(loadEmbeddedDetectors)
	return detectorSet, detectorErrs
}

// loadEmbeddedDetectors reads every detector in packs/: any JSON file other
// than a language.json. Like language specs, adding one is adding a file.
func loadEmbeddedDetectors() {
	names, err := packFiles(false)
	if err != nil {
		detectorErrs = append(detectorErrs, err)
		detectorSet = &DetectorSet{}
		return
	}

	var detectors []Detector
	for _, name := range names {
		data, err := packFS.ReadFile(name)
		if err != nil {
			detectorErrs = append(detectorErrs, fmt.Errorf("%s: %w", packLabel(name), err))
			continue
		}
		d, err := decodeDetector(data)
		if err != nil {
			detectorErrs = append(detectorErrs, fmt.Errorf("%s: %w", packLabel(name), err))
			continue
		}
		detectors = append(detectors, d)
	}

	// Compiled on first use, per detector: see lazyDetector.
	detectorSet = newLazyDetectorSet(detectors)
}
