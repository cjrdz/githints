package main

import (
	"path/filepath"
	"testing"

	"github.com/cjrdz/githints/internal/index/lang"
)

func TestIndexDBPath(t *testing.T) {
	got := lang.IndexDBPath("/repo")
	want := filepath.Join("/repo", ".githints", "index.db")
	if got != want {
		t.Errorf("IndexDBPath = %q, want %q", got, want)
	}
}

func TestIndexNotePath(t *testing.T) {
	root := "/repo"
	got, collision, err := lang.IndexNotePath(root, "cmd/api/main.go")
	if err != nil {
		t.Fatalf("IndexNotePath: %v", err)
	}
	if collision {
		t.Fatal("expected no collision")
	}
	want := filepath.Join("/repo", ".githints", "index", "cmd", "api", "main.go.md")
	if got != want {
		t.Errorf("IndexNotePath = %q, want %q", got, want)
	}
}

func TestIndexNotePathCollision(t *testing.T) {
	_, collision, err := lang.IndexNotePath("/repo", "index/sneaky.go")
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !collision {
		t.Fatal("expected collision=true")
	}
}

func TestIndexNotePathAbsoluteRejected(t *testing.T) {
	_, collision, err := lang.IndexNotePath("/repo", "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
	if collision {
		t.Fatal("expected collision=false for absolute path")
	}
}
