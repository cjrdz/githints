package notes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func pageOf(t *testing.T) string {
	t.Helper()
	root, db := fixture(t)
	writeNote(t, root, "plan.md", "---\ntitle: Plan </script><script>alert(1)</script>\ntags: [a]\n---\n"+
		"</script><script>alert(2)</script> <!-- <img src=x onerror=alert(3)> -->\nbody & more\n")
	if _, err := Sync(db, root); err != nil {
		t.Fatal(err)
	}
	page, err := RenderPage(db)
	if err != nil {
		t.Fatal(err)
	}
	return string(page)
}

// Nothing in a note can close the data block and start a script of its own.
func TestPageCannotBeBrokenOutOf(t *testing.T) {
	out := pageOf(t)
	if n := strings.Count(strings.ToLower(out), "</script"); n != 2 {
		t.Fatalf("expected 2 </script> closers, found %d", n)
	}
	if strings.Count(out, "<script") != 2 {
		t.Fatal("unexpected script elements")
	}
	if strings.Contains(out, "alert(") && !strings.Contains(out, `<`) {
		t.Error("note text reached the page unescaped")
	}
	m := regexp.MustCompile(`(?s)<script type="application/json" id="notes-data">(.*?)</script>`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("no data block")
	}
	var back pageData
	if err := json.Unmarshal([]byte(m[1]), &back); err != nil {
		t.Fatalf("data block is not JSON: %v", err)
	}
	if len(back.Notes) != 1 || !strings.Contains(back.Notes[0].Body, "</script><script>alert(2)</script>") {
		t.Errorf("the note did not survive the round trip: %+v", back.Notes)
	}
	if back.Notes[0].Tags == nil || back.Notes[0].Files == nil || back.Notes[0].Changes == nil || back.Notes[0].Links == nil {
		t.Errorf("a list came out null, which the viewer cannot iterate: %+v", back.Notes[0])
	}
}

// The CSP pins the one script and one style by hash; a mismatch would stop the
// viewer running without any error on the page.
func TestPageCSP(t *testing.T) {
	out := pageOf(t)
	for _, want := range []string{"default-src 'none'", "base-uri 'none'", "form-action 'none'"} {
		if !strings.Contains(out, want) {
			t.Errorf("CSP lacks %s", want)
		}
	}
	script := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(out)[1]
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(out)[1]
	for name, body := range map[string]string{"script": script, "style": style} {
		if !strings.Contains(out, sha256CSP(body)) {
			t.Errorf("CSP does not pin the inline %s", name)
		}
	}
	if strings.Contains(out, "connect-src") || strings.Contains(out, "unsafe-") {
		t.Error("the CSP is looser than the graph viewer's")
	}
}

// The viewer's own code may not build markup from text, run text, or reach the
// network: that is the whole reason a note can be shown at all.
func TestViewerCodeNeverTurnsTextIntoMarkupOrRequests(t *testing.T) {
	js, err := asset("viewer.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "createContextualFragment",
		"eval(", "new Function", "setTimeout(\"", "fetch(", "XMLHttpRequest", "WebSocket", "sendBeacon",
		"importScripts", "window.open", "location.href", "location.assign", ".src =", "setAttribute(\"on", "javascript:",
	} {
		if strings.Contains(js, banned) {
			t.Errorf("viewer.js contains %q", banned)
		}
	}
	css, _ := asset("viewer.css")
	for _, s := range []string{"@import", "url(", "http://", "https://"} {
		if strings.Contains(css, s) {
			t.Errorf("viewer.css contains %q", s)
		}
	}
	page := pageOf(t)
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("the page references a URL")
	}
}

func TestPageLeavesBodiesOutPastTheSizeLimit(t *testing.T) {
	root, db := fixture(t)
	chunk := strings.Repeat("word ", MaxBodyBytes/5-10)
	for i := 0; i < 160; i++ { // 160 * ~32 KiB is past the 4 MiB budget
		writeNote(t, root, strings.Repeat("a", 1)+string(rune('a'+i%26))+string(rune('a'+i/26))+".md", "---\ntitle: N\n---\n"+chunk)
	}
	if _, err := Sync(db, root); err != nil {
		t.Fatal(err)
	}
	page, err := RenderPage(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) > maxPageBytes+256<<10 {
		t.Errorf("page is %d bytes, past the budget", len(page))
	}
	if !strings.Contains(string(page), `"omitted":true`) {
		t.Error("no note was marked as left out")
	}
}

func TestWritePageGoesThroughState(t *testing.T) {
	root, db := fixture(t)
	if err := WritePage(db, root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".githints", PageName))
	if err != nil || !strings.Contains(string(b), "githints notes") {
		t.Fatalf("page not written: %v", err)
	}
}

// notes.html is rewritten after every note; a link in its place must not turn
// that into an overwrite of the store.
func TestWritePageRefusesToWriteThroughALink(t *testing.T) {
	root, db := fixture(t)
	victim := filepath.Join(root, ".githints", "victim.db")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, "victim.db", filepath.Join(root, ".githints", PageName))
	if err := WritePage(db, root); err == nil {
		t.Error("WritePage wrote through a link")
	}
	if b, _ := os.ReadFile(victim); string(b) != "ORIGINAL" {
		t.Errorf("the file behind the link was overwritten: %q", b)
	}
}

// sha256CSP is the CSP source expression for body, computed here rather than
// with the function under test.
func sha256CSP(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}
