package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndCompareVersions(t *testing.T) {
	for _, bad := range []string{"", "dev", "1.2", "v1.2.3.4", "v01.2.3", "1.2.3-", "1.2.3/../x", "1.2.3-" + strings.Repeat("a", 65)} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
	order := []string{"0.3.9", "v0.4.0-rc.1", "0.4.0-rc.2", "0.4.0-rc.10", "0.4.0", "v0.4.0-next", "0.4.1", "0.10.0", "1.0.0"}
	for i := range order {
		for j := range order {
			a, _ := ParseVersion(order[i])
			b, ok := ParseVersion(order[j])
			if !ok {
				t.Fatalf("ParseVersion(%q) failed", order[j])
			}
			if got, want := Compare(a, b), sign(i-j); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestDecide(t *testing.T) {
	for _, tc := range []struct {
		current, target  string
		explicit, force  bool
		install, wantErr bool
	}{
		{"0.4.0", "v0.5.0", false, false, true, false},
		{"0.4.0", "v0.4.0", false, false, false, false},
		{"0.4.0", "v0.4.0", false, true, true, false},
		{"0.5.0", "v0.4.0", false, false, false, true},
		{"0.5.0", "v0.4.0", true, false, false, true},
		{"0.5.0", "v0.4.0", false, true, false, true},
		{"0.5.0", "v0.4.0", true, true, true, false},
		{"v0.4.0-next", "v0.4.0", false, false, false, true},
		{"v0.4.0-next", "v0.4.1", false, false, true, false},
		{"dev", "v0.5.0", false, false, false, true},
		{"dev", "v0.5.0", false, true, true, false},
		{"0.4.0", "latest", false, false, false, true},
	} {
		d, err := Decide(tc.current, tc.target, tc.explicit, tc.force)
		if (err != nil) != tc.wantErr || d.Install != tc.install {
			t.Errorf("Decide(%s -> %s, explicit=%v force=%v) = install %v, err %v", tc.current, tc.target, tc.explicit, tc.force, d.Install, err)
		}
	}
}

func TestManagedBy(t *testing.T) {
	for _, tc := range []struct {
		exe, gopath, gobin string
		managed            bool
	}{
		{"/usr/bin/githints", "", "", true},
		{"/usr/local/bin/githints", "", "", false},
		{"/home/me/.local/bin/githints", "/home/me/go", "", false},
		{"/home/me/go/bin/githints", "/home/me/go", "", true},
		{"/custom/gobin/githints", "/home/me/go", "/custom/gobin", true},
		{"/home/me/gopher/bin/githints", "/home/me/go", "", false},
		{"/opt/homebrew/Cellar/githints/0.4.0/bin/githints", "", "", true},
		{"/home/linuxbrew/.linuxbrew/bin/githints", "", "", true},
		{"/nix/store/abc-githints/bin/githints", "", "", true},
		{"/snap/githints/12/bin/githints", "", "", true},
		{`C:\Users\me\scoop\apps\githints\current\githints.exe`, "", "", true},
		{`C:\Users\me\AppData\Local\Programs\githints\githints.exe`, "", "", false},
		{`C:\ProgramData\chocolatey\lib\githints\tools\githints.exe`, "", "", true},
	} {
		owner, cmd := ManagedBy(tc.exe, tc.gopath, tc.gobin)
		if (owner != "") != tc.managed || (owner != "" && cmd == "") {
			t.Errorf("ManagedBy(%s) = %q, %q; managed want %v", tc.exe, owner, cmd, tc.managed)
		}
	}
}

func tarGz(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		body := h.Linkname
		if h.Typeflag == tar.TypeSymlink {
			body = ""
		} else {
			h.Linkname = ""
		}
		h.Size = int64(len(body))
		h.Mode = 0o755
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// file is a regular tar entry; its content rides in Linkname for brevity.
func file(name, content string) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeReg, Linkname: content}
}

func TestExtractBinaryTakesOnlyTheTopLevelBinary(t *testing.T) {
	archive := tarGz(t,
		file("../githints", "escape"),
		file("/githints", "absolute"),
		file("sub/githints", "nested"),
		tar.Header{Name: "githints", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		file("README.md", "readme"),
		file("./githints", "the binary"),
	)
	got, err := extractBinary(archive, "linux")
	if err != nil || string(got) != "the binary" {
		t.Fatalf("extractBinary = %q, %v", got, err)
	}
	if _, err := extractBinary(tarGz(t, file("../githints", "x"), file("bin/githints", "y")), "linux"); err == nil {
		t.Error("an archive with no top-level binary was accepted")
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for name, body := range map[string]string{"..\\githints.exe": "escape", "githints.exe": "win binary"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	if got, err := extractBinary(zbuf.Bytes(), "windows"); err != nil || string(got) != "win binary" {
		t.Errorf("zip: %q, %v", got, err)
	}
}

func TestArchiveNameMatchesGoreleaser(t *testing.T) {
	for _, tc := range []struct{ tag, goos, goarch, want string }{
		{"v0.4.0", "linux", "amd64", "githints_0.4.0_linux_amd64.tar.gz"},
		{"v0.4.0", "darwin", "arm64", "githints_0.4.0_darwin_arm64.tar.gz"},
		{"v0.4.0", "windows", "arm64", "githints_0.4.0_windows_arm64.zip"},
	} {
		if got := ArchiveName(tc.tag, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("ArchiveName = %s, want %s", got, tc.want)
		}
	}
	data, err := os.ReadFile("../../.goreleaser.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}`) ||
		!strings.Contains(string(data), `name_template: "checksums.txt"`) {
		t.Error(".goreleaser.yml names archives or checksums differently from update; change both together")
	}
	for _, script := range []string{"../../install.sh", "../../install.ps1"} {
		data, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "githints_${number}_") {
			t.Errorf("%s names archives differently from update", script)
		}
	}
}

// fakeRelease is a GitHub stand-in serving one release over TLS.
type fakeRelease struct {
	tag       string
	assets    map[string][]byte
	latestLoc string // overrides the /releases/latest redirect
	assetLoc  string // when set, every download redirects here
	t         *testing.T
}

func newFakeRelease(t *testing.T, tag, binary string) *fakeRelease {
	archive := tarGz(t, file("githints", binary), file("LICENSE", "x"))
	name := ArchiveName(tag, "linux", "amd64")
	sum := sha256.Sum256(archive)
	return &fakeRelease{tag: tag, t: t, assets: map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(fmt.Sprintf("%s  %s\n%s  other.zip\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64))),
	}}
}

func (f *fakeRelease) serve() (*Client, *httptest.Server) {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		loc := f.latestLoc
		if loc == "" {
			loc = srv.URL + "/" + Repo + "/releases/tag/" + f.tag
		}
		http.Redirect(w, r, loc, http.StatusFound)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if f.assetLoc != "" {
			http.Redirect(w, r, f.assetLoc, http.StatusFound)
			return
		}
		http.Redirect(w, r, "/assets/"+name, http.StatusFound)
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := f.assets[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	srv = httptest.NewTLSServer(mux)
	f.t.Cleanup(srv.Close)
	return newClient(srv.Client(), srv.URL, map[string]bool{"127.0.0.1": true}, "test"), srv
}

func target(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "githints")
	if err := os.WriteFile(p, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func opts(path string) Options {
	return Options{
		Current: "0.4.0", Target: path, GOOS: "linux", GOARCH: "amd64",
		CheckRun: func(p string) (string, error) {
			data, err := os.ReadFile(p)
			if err != nil || !strings.HasPrefix(string(data), "binary ") {
				return "", errors.New("does not run")
			}
			return strings.TrimPrefix(string(data), "binary ") + "\n", nil
		},
	}
}

func contents(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInstall(t *testing.T) {
	ctx := context.Background()

	t.Run("latest release installs", func(t *testing.T) {
		c, _ := newFakeRelease(t, "v0.5.0", "binary 0.5.0").serve()
		p := target(t)
		res, err := c.Install(ctx, opts(p))
		if err != nil || !res.Install || res.Target != "v0.5.0" {
			t.Fatalf("Install = %+v, %v", res, err)
		}
		if got := contents(t, p); got != "binary 0.5.0" {
			t.Errorf("target holds %q", got)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".githints-update-*")); len(leftovers) != 0 {
			t.Errorf("temporary files left: %v", leftovers)
		}
	})

	t.Run("already up to date does nothing", func(t *testing.T) {
		c, _ := newFakeRelease(t, "v0.4.0", "binary 0.4.0").serve()
		p := target(t)
		res, err := c.Install(ctx, opts(p))
		if err != nil || res.Install || contents(t, p) != "old binary" {
			t.Fatalf("Install = %+v, %v", res, err)
		}
	})

	refusals := []struct {
		name    string
		setup   func(f *fakeRelease, srvURL string)
		o       func(o Options) Options
		wantErr string
	}{
		{"checksum mismatch", func(f *fakeRelease, _ string) {
			f.assets["checksums.txt"] = []byte(strings.Repeat("ab", 32) + "  githints_0.5.0_linux_amd64.tar.gz\n")
		}, nil, "checksum mismatch"},
		{"archive missing from checksums.txt", func(f *fakeRelease, _ string) {
			f.assets["checksums.txt"] = []byte(strings.Repeat("ab", 32) + "  other.tar.gz\n")
		}, nil, "not listed in checksums.txt"},
		{"no checksums.txt", func(f *fakeRelease, _ string) { delete(f.assets, "checksums.txt") }, nil, "unverified"},
		{"no asset for this architecture", nil, func(o Options) Options { o.GOARCH = "riscv64"; return o }, "has no githints_0.5.0_linux_riscv64.tar.gz"},
		{"redirect to a host that is not GitHub", func(f *fakeRelease, _ string) { f.assetLoc = "https://example.com/githints.tar.gz" }, nil, "not a GitHub release host"},
		{"redirect to plain http", func(f *fakeRelease, srvURL string) {
			f.assetLoc = strings.Replace(srvURL, "https://", "http://", 1) + "/assets/x"
		}, nil, "only https"},
		{"latest pointing elsewhere", func(f *fakeRelease, _ string) { f.latestLoc = "https://example.com/cjrdz/githints/releases/tag/v9.9.9" }, nil, "not a GitHub release host"},
		{"latest with a malformed tag", func(f *fakeRelease, srvURL string) {
			f.latestLoc = srvURL + "/" + Repo + "/releases/tag/v1.0.0%2F..%2Fx"
		}, nil, "unexpected redirect"},
		{"oversized archive", func(f *fakeRelease, _ string) {
			f.assets["githints_0.5.0_linux_amd64.tar.gz"] = make([]byte, maxArchiveBytes+1)
		}, nil, "limit"},
		{"binary that does not run", func(f *fakeRelease, _ string) {
			*f = *newFakeRelease(f.t, "v0.5.0", "garbage")
		}, nil, "did not run"},
		{"binary reporting another version", func(f *fakeRelease, _ string) {
			*f = *newFakeRelease(f.t, "v0.5.0", "binary 0.4.9")
		}, nil, "did not run as version 0.5.0"},
		{"downgrade", nil, func(o Options) Options { o.Current = "0.6.0"; return o }, "downgrading needs"},
		{"package-managed binary", nil, func(o Options) Options { o.Target = "/usr/bin/githints"; return o }, "apt"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRelease(t, "v0.5.0", "binary 0.5.0")
			c, srv := f.serve()
			if tc.setup != nil {
				tc.setup(f, srv.URL)
			}
			p := target(t)
			o := opts(p)
			if tc.o != nil {
				o = tc.o(o)
			}
			_, err := c.Install(ctx, o)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if got := contents(t, p); got != "old binary" {
				t.Errorf("target changed to %q", got)
			}
		})
	}

	t.Run("an explicit older release installs with -force", func(t *testing.T) {
		c, _ := newFakeRelease(t, "v0.3.0", "binary 0.3.0").serve()
		p := target(t)
		o := opts(p)
		o.Tag, o.Force = "v0.3.0", true
		if _, err := c.Install(ctx, o); err != nil || contents(t, p) != "binary 0.3.0" {
			t.Fatalf("err %v, target %q", err, contents(t, p))
		}
	})

	t.Run("offline", func(t *testing.T) {
		c, srv := newFakeRelease(t, "v0.5.0", "binary 0.5.0").serve()
		srv.Close()
		if _, err := c.Install(ctx, opts(target(t))); err == nil || !strings.Contains(err.Error(), "could not reach GitHub") {
			t.Fatalf("err = %v", err)
		}
	})
}

// failingRename fails renames onto one path and records the sequence.
type failingRename struct {
	failOnto string
	calls    []string
}

func (f *failingRename) Rename(from, to string) error {
	f.calls = append(f.calls, filepath.Base(from)+"->"+filepath.Base(to))
	if to == f.failOnto {
		f.failOnto = "" // fail once, so the restore can succeed
		return errors.New("access denied")
	}
	return os.Rename(from, to)
}
func (f *failingRename) Remove(p string) error { return os.Remove(p) }

func TestReplaceBinaryOnWindows(t *testing.T) {
	ok := func(string) (string, error) { return "0.5.0", nil }

	p := target(t)
	fs := &failingRename{}
	if err := replaceBinary(p, []byte("new"), "0.5.0", "windows", ok, fs); err != nil {
		t.Fatal(err)
	}
	if contents(t, p) != "new" || contents(t, p+".old") != "old binary" {
		t.Errorf("after replace: %q and .old %q", contents(t, p), contents(t, p+".old"))
	}
	if len(fs.calls) != 2 || fs.calls[0] != "githints->githints.old" {
		t.Errorf("rename sequence %v", fs.calls)
	}

	p = target(t)
	fs = &failingRename{failOnto: p}
	if err := replaceBinary(p, []byte("new"), "0.5.0", "windows", ok, fs); err == nil || !strings.Contains(err.Error(), "previous binary is back") {
		t.Fatalf("err = %v", err)
	}
	if contents(t, p) != "old binary" {
		t.Errorf("the old binary was not restored: %q", contents(t, p))
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".githints-update-*")); len(leftovers) != 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}
}
