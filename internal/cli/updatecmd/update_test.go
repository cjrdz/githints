package updatecmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/update"
)

// fake replaces the network calls for one test.
func fake(t *testing.T, latest string, latestErr error) {
	t.Helper()
	savedLatest, savedInstall := latestTag, install
	t.Cleanup(func() { latestTag, install = savedLatest, savedInstall })
	latestTag = func(context.Context, string) (string, error) { return latest, latestErr }
	install = func(_ context.Context, version string, o update.Options) (update.Result, error) {
		t.Fatal("-check must not install")
		return update.Result{}, nil
	}
}

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name, running, latest string
		latestErr             error
		wantErr               error
		wantOut               string
	}{
		{"up to date", "0.5.0", "v0.5.0", nil, nil, "up to date"},
		{"update available", "0.4.0", "v0.5.0", nil, core.ErrUpdateAvailable, "v0.5.0 is available"},
		{"snapshot ahead of the latest", "v0.5.0-next", "v0.5.0", nil, nil, "newer than the latest"},
		{"development build", "dev", "v0.5.0", nil, core.ErrUpdateAvailable, "development build"},
		{"offline", "0.4.0", "", errors.New("could not reach GitHub to find the latest release: dial tcp: no route"), nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake(t, tc.latest, tc.latestErr)
			out, err := clitest.CaptureStdout(t, func() error { return Run([]string{"-check"}, tc.running) })
			switch {
			case tc.latestErr != nil:
				if err == nil || errors.Is(err, core.ErrUpdateAvailable) || !strings.Contains(err.Error(), "could not reach GitHub") {
					t.Errorf("offline: err = %v", err)
				}
			case !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil):
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output %q lacks %q", out, tc.wantOut)
			}
		})
	}
}

func TestRejectsABadVersionFlag(t *testing.T) {
	fake(t, "v0.5.0", nil)
	for _, v := range []string{"latest", "0.5", "v0.5.0/../x"} {
		if err := Run([]string{"-check", "-version=" + v}, "0.4.0"); err == nil || errors.Is(err, core.ErrUpdateAvailable) {
			t.Errorf("-version=%s: err = %v", v, err)
		}
	}
}

// After an install inside a tracked repository, upgrade runs with the new
// binary only when asked to.
func TestUpgradeAfterUpdate(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	savedInstall, savedUpgrade, savedStdin, savedTTY := install, runUpgrade, stdin, interactive
	t.Cleanup(func() { install, runUpgrade, stdin, interactive = savedInstall, savedUpgrade, savedStdin, savedTTY })
	install = func(_ context.Context, _ string, o update.Options) (update.Result, error) {
		return update.Result{Decision: update.Decision{Install: true, Target: "v0.5.0", Reason: "newer release"}, Path: "/tmp/new-githints", Archive: "githints_0.5.0_linux_amd64.tar.gz"}, nil
	}

	for _, tc := range []struct {
		name        string
		args        []string
		tty         bool
		answer      string
		wantUpgrade bool
	}{
		{"-yes", []string{"-yes"}, false, "", true},
		{"asked, yes", nil, true, "y\n", true},
		{"asked, no", nil, true, "n\n", false},
		{"asked, no answer", nil, true, "", false},
		{"not a terminal", nil, false, "y\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := ""
			runUpgrade = func(bin string) error { ran = bin; return nil }
			interactive = func() bool { return tc.tty }
			stdin = strings.NewReader(tc.answer)
			out, err := clitest.CaptureStdout(t, func() error { return Run(tc.args, "0.4.0") })
			if err != nil {
				t.Fatal(err)
			}
			if (ran != "") != tc.wantUpgrade || (ran != "" && ran != "/tmp/new-githints") {
				t.Errorf("upgrade ran with %q, want ran=%v", ran, tc.wantUpgrade)
			}
			for _, want := range []string{"updated githints 0.4.0 -> 0.5.0", "gh attestation verify githints_0.5.0_linux_amd64.tar.gz", "restart your MCP servers", "githints upgrade"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}
