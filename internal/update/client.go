// Package update finds, downloads, verifies and installs githints releases.
// It is the only package that opens network connections, and only when a
// person runs `githints update`; TestOnlyUpdateImportsNetHTTP holds that.
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "cjrdz/githints"

// Hosts a release can be served from: github.com, which redirects release
// downloads to GitHub's asset hosts.
var releaseHosts = map[string]bool{
	"github.com":                            true,
	"objects.githubusercontent.com":         true,
	"release-assets.githubusercontent.com":  true,
	"github-releases.githubusercontent.com": true,
}

const (
	maxRedirects   = 5
	requestTimeout = 2 * time.Minute
)

// Client talks to GitHub releases over https only, to releaseHosts only.
// base and hosts exist for tests, which point them at an httptest server;
// nothing outside this package can change them.
type Client struct {
	http      *http.Client
	base      string
	hosts     map[string]bool
	userAgent string
}

// NewClient returns a client for the real release host.
func NewClient(version string) *Client {
	return newClient(&http.Client{}, "https://github.com", releaseHosts, version)
}

func newClient(hc *http.Client, base string, hosts map[string]bool, version string) *Client {
	c := &Client{base: base, hosts: hosts, userAgent: "githints/" + version}
	// A copy, so the redirect policy and timeout never leak into a client the
	// caller shares.
	cp := *hc
	cp.Timeout = requestTimeout
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		return c.allowed(req.URL)
	}
	c.http = &cp
	return c
}

func (c *Client) allowed(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("refusing %s: only https is allowed", u.Redacted())
	}
	if !c.hosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("refusing %s: %s is not a GitHub release host", u.Redacted(), u.Hostname())
	}
	return nil
}

// LatestTag asks github.com which release is the latest. Like install.sh, it
// reads the redirect from /releases/latest instead of calling the API, which
// avoids both a JSON dependency and the unauthenticated API rate limit.
func (c *Client) LatestTag(ctx context.Context) (string, error) {
	req, err := c.request(ctx, http.MethodHead, c.base+"/"+Repo+"/releases/latest")
	if err != nil {
		return "", err
	}
	noFollow := *c.http
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach GitHub to find the latest release: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("could not find the latest release: GitHub answered %s", resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("could not find the latest release: %w", err)
	}
	if err := c.allowed(loc); err != nil {
		return "", err
	}
	tag, ok := strings.CutPrefix(loc.Path, "/"+Repo+"/releases/tag/")
	if !ok || !tagRe.MatchString(tag) {
		return "", fmt.Errorf("could not find the latest release: unexpected redirect to %s", loc.Redacted())
	}
	return tag, nil
}

// download fetches one release asset, refusing anything over max bytes.
func (c *Client) download(ctx context.Context, tag, name string, max int64) ([]byte, error) {
	if !tagRe.MatchString(tag) {
		return nil, fmt.Errorf("invalid release tag %q", tag)
	}
	req, err := c.request(ctx, http.MethodGet, c.base+"/"+Repo+"/releases/download/"+tag+"/"+name)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("release %s has no %s", tag, name)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("download %s: %s", name, resp.Status)
	case resp.ContentLength > max:
		return nil, fmt.Errorf("download %s: %d bytes is over the %d MiB limit", name, resp.ContentLength, max>>20)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("download %s: over the %d MiB limit", name, max>>20)
	}
	return data, nil
}

func (c *Client) request(ctx context.Context, method, raw string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, raw, nil)
	if err != nil {
		return nil, err
	}
	if err := c.allowed(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	return req, nil
}
