package teardown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
)

func TestRemoveStopsTrackingButKeepsStore(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	hooksDir := initManagedHooks(t, dir)

	if err := Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, name := range []string{"post-commit", "pre-commit"} {
		if _, err := os.Stat(filepath.Join(hooksDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still exists", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints", "store.db")); err != nil {
		t.Errorf("store.db missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md missing: %v", err)
	}
}

func TestRemoveRestoresChainedHooks(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	hooksDir := initManagedHooks(t, dir)

	// Overwrite with a user hook, then chain-init so githints takes it over.
	userHook := filepath.Join(hooksDir, "post-commit")
	if err := os.WriteFile(userHook, []byte("#!/bin/sh\necho user\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := initialize.Init([]string{"-chain"}); err != nil {
		t.Fatalf("Init -chain: %v", err)
	}

	if err := Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	data, err := os.ReadFile(userHook)
	if err != nil {
		t.Fatalf("user hook not restored: %v", err)
	}
	if !strings.Contains(string(data), "echo user") {
		t.Errorf("restored hook is not the user one: %s", data)
	}
	if _, err := os.Stat(userHook + ".pre-githints"); !os.IsNotExist(err) {
		t.Error("chained backup left behind")
	}
}

func TestRemoveLeavesForeignHooksAlone(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	hooksDir := initManagedHooks(t, dir)

	// Replace with a non-githints hook.
	for _, name := range []string{"post-commit", "pre-commit"} {
		path := filepath.Join(hooksDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho foreign\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, name := range []string{"post-commit", "pre-commit"} {
		data, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil || !strings.Contains(string(data), "echo foreign") {
			t.Errorf("%s was removed or changed: %v %s", name, err, data)
		}
	}
}
