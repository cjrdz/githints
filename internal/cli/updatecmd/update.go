// Package updatecmd implements `githints update`, the one command that uses
// the network.
package updatecmd

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/update"
)

// The network calls, as variables so tests can answer them.
var (
	latestTag = func(ctx context.Context, version string) (string, error) {
		return update.NewClient(version).LatestTag(ctx)
	}
	install = func(ctx context.Context, version string, o update.Options) (update.Result, error) {
		return update.NewClient(version).Install(ctx, o)
	}
	// runUpgrade runs `upgrade` with the new binary, not this process: the
	// code that knows what the new release writes is in the new binary.
	runUpgrade = func(bin string) error {
		cmd := exec.Command(bin, "upgrade")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	// runningBinary is the binary -check reports on; tests point it at a
	// package-manager path.
	runningBinary           = update.RunningBinary
	stdin         io.Reader = os.Stdin
	interactive             = func() bool {
		fi, err := os.Stdin.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
)

// Run updates the running binary to the latest release (or -version).
//
// Exit codes for -check: 0 up to date, 10 an update is available
// (core.ErrUpdateAvailable), 1 anything else, offline included; 2 stays a
// usage error, so a mistyped flag cannot read as "update available".
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "only report the running and latest versions (exit 0 current, 10 update available, 1 error)")
	tag := fs.String("version", "", "install this release tag (e.g. v0.5.0) instead of the latest")
	force := fs.Bool("force", false, "reinstall the same release, replace a development build, or (with -version) downgrade")
	yes := fs.Bool("yes", false, "after updating, run `githints upgrade` here without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("update takes no arguments, got %q", rest[0])
	}
	if *tag != "" {
		if _, ok := update.ParseVersion(*tag); !ok {
			return fmt.Errorf("-version=%q is not a release tag like v0.5.0", *tag)
		}
		if !strings.HasPrefix(*tag, "v") {
			*tag = "v" + *tag
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if *check {
		return runCheck(ctx, version, *tag)
	}

	res, err := install(ctx, version, update.Options{Current: version, Tag: *tag, Force: *force})
	if err != nil {
		return err
	}
	if !res.Install {
		fmt.Printf("githints %s is %s (latest: %s)\n", version, res.Reason, res.Target)
		return nil
	}
	fmt.Printf("updated githints %s -> %s at %s (%s)\n", version, strings.TrimPrefix(res.Target, "v"), res.Path, res.Reason)
	fmt.Printf("checksum ok. To also check build provenance with the GitHub CLI:\n"+
		"  gh release download %s -R %s -p %s && gh attestation verify %s --repo %s\n",
		res.Target, update.Repo, res.Archive, res.Archive, update.Repo)
	fmt.Println()
	fmt.Println("Next:")
	fmt.Println("  restart your MCP servers; a running server keeps the binary it started with")
	fmt.Println("  run `githints upgrade` in each repository githints tracks")

	if _, err := core.InitializedRoot(); err != nil {
		return nil // not in a tracked repository: nothing to offer
	}
	if !*yes {
		if !interactive() {
			return nil
		}
		fmt.Print("\nThis repository is tracked by githints. Run `githints upgrade` here now? [y/N] ")
		answer, _ := bufio.NewReader(stdin).ReadString('\n') // EOF reads as no
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return nil
		}
	}
	fmt.Println()
	return runUpgrade(res.Path)
}

func runCheck(ctx context.Context, version, tag string) error {
	if tag == "" {
		var err error
		if tag, err = latestTag(ctx, version); err != nil {
			return err
		}
	}
	d, err := update.Decide(version, tag, false, false)
	switch {
	case err == nil && d.Install:
		fmt.Printf("githints %s: %s is available; %s\n", version, tag, howToUpdate())
		return core.ErrUpdateAvailable
	case err == nil:
		fmt.Printf("githints %s is up to date (latest: %s)\n", version, tag)
		return nil
	case errors.Is(err, update.ErrNotNewer):
		fmt.Printf("githints %s is newer than the latest release (%s)\n", version, tag)
		return nil
	}
	if _, ok := update.ParseVersion(version); !ok {
		fmt.Printf("githints %s is a development build; the latest release is %s\n", version, tag)
		return core.ErrUpdateAvailable
	}
	return err
}

// howToUpdate is the command that updates this binary: `githints update`, or
// the package manager's own command when one owns it, since update would
// refuse.
func howToUpdate() string {
	bin, err := runningBinary()
	if err != nil {
		return "run `githints update`"
	}
	if owner, cmd := update.Owner(bin); owner != "" {
		return fmt.Sprintf("it was installed by %s, so update it there: %s", owner, cmd)
	}
	return "run `githints update`"
}
