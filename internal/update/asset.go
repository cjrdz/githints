package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Size caps for what a release may hand us. The real archives are a few MiB.
const (
	maxArchiveBytes   = 64 << 20
	maxChecksumsBytes = 1 << 20
	maxBinaryBytes    = 128 << 20
)

// ArchiveName is the release asset for one platform, named as .goreleaser.yml
// names it and as install.sh and install.ps1 download it:
// githints_<version without v>_<os>_<arch>.tar.gz, or .zip on Windows.
func ArchiveName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "githints_" + strings.TrimPrefix(tag, "v") + "_" + goos + "_" + goarch + ext
}

// binaryName is the executable inside an archive.
func binaryName(goos string) string {
	if goos == "windows" {
		return "githints.exe"
	}
	return "githints"
}

// expectedSum finds archive's line in checksums.txt ("<sha256>  <name>"). A
// missing entry is an error: the install scripts can be told to skip it, but
// update never installs unverified.
func expectedSum(sums []byte, archive string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == archive {
			if _, err := hex.DecodeString(f[0]); err != nil || len(f[0]) != sha256.Size*2 {
				return "", fmt.Errorf("checksums.txt has a malformed entry for %s", archive)
			}
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not listed in checksums.txt; refusing to install it unverified", archive)
}

// verifySum checks data against checksums.txt.
func verifySum(data, sums []byte, archive string) error {
	want, err := expectedSum(sums, archive)
	if err != nil {
		return err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s; refusing to install it", archive)
	}
	return nil
}

// extractBinary returns the githints executable from a release archive. Only
// a regular file at the archive's top level named exactly like the binary is
// taken; nothing else is read past its header, and nothing is written to disk
// here, so a crafted entry name ("../x", "/etc/x", "a/../../x") has nowhere
// to go.
func extractBinary(archive []byte, goos string) ([]byte, error) {
	want := binaryName(goos)
	if goos == "windows" {
		return fromZip(archive, want)
	}
	return fromTarGz(archive, want)
}

func topLevel(name, want string) bool {
	return name == want || name == "./"+want
}

func fromTarGz(archive []byte, want string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive has no %s", want)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || !topLevel(h.Name, want) {
			continue
		}
		return readCapped(tr, want)
	}
}

func fromZip(archive []byte, want string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	for _, f := range zr.File {
		if !f.Mode().IsRegular() || !topLevel(f.Name, want) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		defer rc.Close()
		return readCapped(rc, want)
	}
	return nil, fmt.Errorf("the archive has no %s", want)
}

func readCapped(r io.Reader, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBinaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(data) > maxBinaryBytes {
		return nil, fmt.Errorf("%s in the archive is larger than %d MiB", name, maxBinaryBytes>>20)
	}
	return data, nil
}
