package teardown

import (
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/gitutil"
)

// initManagedHooks installs githints hooks in the current temp repo and
// returns the hooks directory.
func initManagedHooks(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("initialize.Init: %v", err)
	}
	hooksDir, err := gitutil.HooksDir(dir)
	if err != nil {
		t.Fatalf("HooksDir: %v", err)
	}
	return hooksDir
}
