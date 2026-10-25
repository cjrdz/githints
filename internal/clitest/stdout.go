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
	return capture(t, &os.Stdout, fn)
}

// CaptureStderr is CaptureStdout for stderr, where the hooks report.
func CaptureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	return capture(t, &os.Stderr, fn)
}

func capture(t *testing.T, stream **os.File, fn func() error) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := *stream
	*stream = w

	runErr := fn()

	*stream = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured output: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(out), runErr
}
