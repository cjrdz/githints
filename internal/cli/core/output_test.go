package core

import (
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
)

// The pre-commit hook is a warning by design. An internal failure -- here an
// unopenable store -- must not abort the commit, even in blocking mode.
func TestKeyChangedHint(t *testing.T) {
	rows := []store.Change{{ID: 1, HMAC: "a"}, {ID: 2, HMAC: "b"}}
	all := []integrity.IntegrityError{{ID: 1, Problem: integrity.ProblemHMACMismatch}, {ID: 2, Problem: integrity.ProblemHMACMismatch}}
	if !strings.Contains(KeyChangedHint(rows, all), "user.email") {
		t.Error("no hint when every signed row fails")
	}
	if KeyChangedHint(rows, all[:1]) != "" {
		t.Error("hint shown for a partial failure, which does look like tampering")
	}
}

// Every command but init used to create .githints/store.db as a side effect,
// so `githints serve` in a fresh repository came up with no hooks, no salt,
// and no explanation.
