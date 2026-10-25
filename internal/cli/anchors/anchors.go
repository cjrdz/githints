// Package anchors implements `githints anchors push|fetch`: carrying the
// Merkle anchors in refs/notes/githints to and from a remote. git does not push
// notes by default, so until they are pushed the anchors protect history only
// on the machine that wrote them.
package anchors

import (
	"errors"
	"fmt"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/store"
)

const usage = "usage: githints anchors push|fetch [REMOTE]  (default remote: origin)"

// Run dispatches `githints anchors`.
func Run(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return errors.New(usage)
	}
	remote := "origin"
	if len(args) == 2 {
		remote = args[1]
	}
	_, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	switch args[0] {
	case "push":
		local := gitutil.RefHash(gitutil.AnchorsRef)
		if local == "" {
			fmt.Println("no anchors yet: the post-commit hook writes one per commit")
			return nil
		}
		if err := gitutil.PushAnchors(remote); err != nil {
			return fmt.Errorf("%w\nif the remote has anchors this clone lacks, run `githints anchors fetch %s` first", err, remote)
		}
		if err := st.MetaSet(store.MetaAnchorsPushed, local); err != nil {
			return err
		}
		fmt.Printf("pushed %s to %s\n", gitutil.AnchorsRef, remote)
		return nil
	case "fetch":
		if err := gitutil.FetchAnchors(remote); err != nil {
			return fmt.Errorf("%w\nif this clone and the remote both have anchors the other lacks, fetch the remote's into another ref and combine them with `git notes --ref=githints merge`; fetch never overwrites local anchors", err)
		}
		fmt.Printf("fetched %s from %s; `githints verify` checks the log against them\n", gitutil.AnchorsRef, remote)
		return nil
	}
	return errors.New(usage)
}
