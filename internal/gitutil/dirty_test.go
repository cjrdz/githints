package gitutil

import (
	"context"
	"reflect"
	"testing"
)

func TestDirtyFiles(t *testing.T) {
	dir := makeRepo(t)
	writeFile(t, dir, ".gitignore", "build/\n")
	writeFile(t, dir, "kept.go", "package a\n")
	writeFile(t, dir, "edited.go", "package a\n")
	writeFile(t, dir, "moved.go", "package a\n")
	writeFile(t, dir, "gone.go", "package a\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	if got, err := DirtyFiles(context.Background(), dir); err != nil || len(got) != 0 {
		t.Fatalf("clean tree: %v, %v", got, err)
	}

	writeFile(t, dir, "edited.go", "package a\n\nvar x = 1\n")
	writeFile(t, dir, "sub dir/new file.go", "package b\n")
	writeFile(t, dir, "staged.go", "package a\n")
	runGit(t, dir, "add", "staged.go")
	runGit(t, dir, "mv", "moved.go", "renamed.go")
	runGit(t, dir, "rm", "-q", "gone.go")
	writeFile(t, dir, "build/out.bin", "ignored")

	got, err := DirtyFiles(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"edited.go", "gone.go", "moved.go", "renamed.go", "staged.go", "sub dir/new file.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DirtyFiles = %q, want %q", got, want)
	}
}

func TestDirtyFilesOutsideARepo(t *testing.T) {
	if _, err := DirtyFiles(context.Background(), t.TempDir()); err == nil {
		t.Error("expected an error outside a repository")
	}
}
