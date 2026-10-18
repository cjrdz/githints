package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cjrdz/githints/internal/gitutil"
)

// ignoreTree is what a full walk needs to know about ignores: the
// directories to prune and the files to leave out.
type ignoreTree struct {
	dirs  map[string]bool
	files ignoreSet // nil: not known up front; ask resolveIgnored per file
}

// scanIgnores decides ignores for a full walk. It asks git for every ignored
// path at once (listIgnored): one process per pass, where the per-depth
// checks (ignoredDirs, then resolveIgnored for the files) started two per
// level of the tree plus two, and took four times as long on this
// repository. When that fails -- an old git, output over the cap -- the
// per-depth checks answer instead, as they always did.
func scanIgnores(root string) ignoreTree {
	t, err := listIgnored(root)
	if err == nil {
		return t
	}
	fmt.Fprintf(os.Stderr, "githints: listing ignored paths failed (%v); checking per directory\n", err)
	return ignoreTree{dirs: ignoredDirs(root)}
}

// listIgnored runs `git ls-files -c -o -i --exclude-standard --directory`:
// every tracked and untracked path the ignore rules match, with a directory
// the rules exclude entirely given once, as "dir/". That is the question
// check-ignore --no-index answers per path, asked of the whole tree.
//
// .githintsignore is a second pass with it as core.excludesFile, exactly as
// in resolveIgnored, and for the same reason: run separately, it can only
// add ignores, never re-include what the repository's rules exclude. The two
// run concurrently and their answers are unioned.
func listIgnored(root string) (ignoreTree, error) {
	hintsIgnore := filepath.Join(root, ".githintsignore")
	var extra ignoreTree
	var extraErr error
	var wg sync.WaitGroup
	if fileExists(hintsIgnore) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			extra, extraErr = lsIgnored(root, hintsIgnore)
		}()
	}
	tree, err := lsIgnored(root, "")
	wg.Wait()
	if err != nil {
		return ignoreTree{}, err
	}
	if extraErr != nil {
		return ignoreTree{}, extraErr
	}
	for d := range extra.dirs {
		tree.dirs[d] = true
	}
	for f := range extra.files {
		tree.files[f] = true
	}
	return tree, nil
}

func lsIgnored(root, excludesFile string) (ignoreTree, error) {
	gitIgnoreCalls.Add(1)
	var args []string
	if excludesFile != "" {
		args = append(args, "-c", "core.excludesFile="+excludesFile)
	}
	// -z: a path may contain a newline, and a mis-split line would silently
	// mark the wrong file ignored.
	args = append(args, "ls-files", "-z", "--cached", "--others", "--ignored", "--exclude-standard", "--directory")
	stdout, err := gitutil.RunIn(context.Background(), root, nil, gitutil.MaxOutputBytes, args...)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return ignoreTree{}, fmt.Errorf("git ls-files: exit %d", exit.ExitCode())
		}
		return ignoreTree{}, err
	}
	t := ignoreTree{dirs: map[string]bool{}, files: ignoreSet{}}
	for _, p := range strings.Split(string(stdout), "\x00") {
		switch {
		case p == "":
		case strings.HasSuffix(p, "/"):
			t.dirs[strings.TrimSuffix(p, "/")] = true
		default:
			t.files[p] = true
		}
	}
	return t, nil
}
