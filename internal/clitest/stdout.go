package clitest

import (
	"io"
	"os"
	"testing"
)

// CaptureStdout redirects os.Stdout for the duration of fn and returns what
// it wrote. Safe only for small outputs: fn runs to completion before the
// pipe is drained, so anything past the pipe buffer would deadlock.
func CaptureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	runErr := fn()

	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(out), runErr
}
