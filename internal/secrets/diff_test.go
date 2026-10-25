package secrets

import (
	"strings"
	"testing"
)

func TestScrubDiffRedactsSecretFileContent(t *testing.T) {
	diff := `diff --git a/.env b/.env
--- a/.env
+++ b/.env
@@ -1,3 +1,3 @@
 DB_HOST=localhost
-DB_PASS=old
+DB_PASS=new_secret_value
 PORT=5432
`
	got := ScrubDiff(diff)
	if strings.Contains(got, "new_secret_value") {
		t.Errorf("secret value leaked in scrubbed diff:\n%s", got)
	}
	if !strings.Contains(got, secretLineMarker) {
		t.Errorf("expected redaction marker, got:\n%s", got)
	}
}

func TestScrubDiffRedactsPEMFile(t *testing.T) {
	diff := `diff --git a/certs/server.pem b/certs/server.pem
--- a/certs/server.pem
+++ b/certs/server.pem
@@ -1 +1 @@
-OLDKEY
+MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA...
`
	got := ScrubDiff(diff)
	if strings.Contains(got, "MIIBIj") {
		t.Errorf("PEM content leaked in scrubbed diff:\n%s", got)
	}
	if !strings.Contains(got, secretLineMarker) {
		t.Errorf("expected redaction marker, got:\n%s", got)
	}
}

func TestScrubDiffRedactsSSHKey(t *testing.T) {
	diff := `diff --git a/ssh/id_rsa b/ssh/id_rsa
--- a/ssh/id_rsa
+++ b/ssh/id_rsa
@@ -1 +1 @@
-ssh-rsa AAAA...
+ssh-rsa BBBB...
`
	got := ScrubDiff(diff)
	if strings.Contains(got, "ssh-rsa") {
		t.Errorf("SSH key content leaked in scrubbed diff:\n%s", got)
	}
}

func TestScrubDiffPreservesNormalFile(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+
 func main() {}
`
	got := ScrubDiff(diff)
	if got != diff {
		t.Errorf("normal diff was altered:\n%s", got)
	}
}

func TestScrubDiffRedactsEmbeddedSecretValue(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1 +1 @@
-const token = "old"
+const token = "AKIAIOSFODNN7EXAMPLE"
`
	got := ScrubDiff(diff)
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key leaked in scrubbed diff:\n%s", got)
	}
	if !strings.Contains(got, secretLineMarker) {
		t.Errorf("expected redaction marker, got:\n%s", got)
	}
}

// git does not quote paths containing spaces in diff headers. Splitting the
// header on whitespace classified "config/private data.txt" as "data.txt",
// which matched no secret glob and cleared protection for the whole hunk.
func TestScrubDiffHandlesPathsWithSpaces(t *testing.T) {
	diff := `diff --git a/config/private data.txt b/config/private data.txt
--- a/config/private data.txt
+++ b/config/private data.txt
@@ -1 +1 @@
-OLD
+DB_PASS=super_secret
`
	got := ScrubDiff(diff)
	if strings.Contains(got, "super_secret") {
		t.Errorf("secret leaked from a path containing a space:\n%s", got)
	}
	if !strings.Contains(got, secretLineMarker) {
		t.Errorf("expected redaction marker, got:\n%s", got)
	}
}

// A header we cannot parse must fail closed rather than emitting content we
// were unable to classify.
func TestScrubDiffFailsClosedOnMalformedHeader(t *testing.T) {
	diff := `diff --git mangled
@@ -1 +1 @@
+DB_PASS=super_secret
`
	got := ScrubDiff(diff)
	if strings.Contains(got, "super_secret") {
		t.Errorf("content leaked past an unparseable header:\n%s", got)
	}
}

// A deleted line whose text starts with "--" renders as "--- ..." in the diff.
// Parsing that as a file header re-classified the file mid-hunk and cleared
// protection while still inside a secret file.
func TestScrubDiffIgnoresHeaderLookalikesInsideHunks(t *testing.T) {
	diff := `diff --git a/.env b/.env
--- a/.env
+++ b/.env
@@ -1,4 +1,4 @@
--- not a header, just a deleted line
+++ not a header either
-DB_PASS=old
+DB_PASS=super_secret
`
	got := ScrubDiff(diff)
	if strings.Contains(got, "super_secret") {
		t.Errorf("secret leaked after a header lookalike inside the hunk:\n%s", got)
	}
}

func TestFilePathFromHeader(t *testing.T) {
	tests := []struct {
		line     string
		wantPath string
		wantOK   bool
	}{
		// The git header is never trusted for classification; the ---/+++
		// lines that follow it are.
		{"diff --git a/main.go b/main.go", unparseableHeader, true},
		{"diff --git a/private data.txt b/private data.txt", unparseableHeader, true},
		{"diff --git mangled", unparseableHeader, true},
		{"--- a/main.go", "main.go", true},
		{"+++ b/config/.env", "config/.env", true},
		{"+++ /dev/null", "", false},
		{" some content line", "", false},
		{"@@ -1 +1 @@", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, ok := filePathFromHeader(tt.line)
			if got != tt.wantPath || ok != tt.wantOK {
				t.Errorf("filePathFromHeader(%q) = (%q, %v), want (%q, %v)",
					tt.line, got, ok, tt.wantPath, tt.wantOK)
			}
		})
	}
}

func TestScrubDiffHeadersPreserved(t *testing.T) {
	diff := `diff --git a/.env b/.env
--- a/.env
+++ b/.env
@@ -1 +1 @@
-OLD
+NEW
`
	got := ScrubDiff(diff)
	if !strings.Contains(got, "diff --git a/.env b/.env") {
		t.Error("diff header missing")
	}
	if !strings.Contains(got, "@@ -1 +1 @@") {
		t.Error("hunk header missing")
	}
}

func TestTruncateDiff(t *testing.T) {
	short := "diff --git a/a b/a\n+one line\n"
	if got := TruncateDiff(short); got != short {
		t.Errorf("short diff was altered: %q", got)
	}

	long := strings.Repeat("+padding line to make this long\n", MaxDiffResultBytes/8)
	got := TruncateDiff(long)
	if len(got) >= len(long) {
		t.Fatalf("long diff not truncated: %d >= %d", len(got), len(long))
	}
	if !strings.Contains(got, "[truncated:") {
		t.Error("expected a truncation marker")
	}
	// The kept portion must be a prefix of the original that ends on a line
	// boundary, not mid-line.
	body := got[:strings.Index(got, "\n[truncated:")]
	if !strings.HasPrefix(long, body) {
		t.Error("retained text is not a prefix of the original diff")
	}
	if long[len(body)] != '\n' {
		t.Errorf("truncation cut mid-line: next byte is %q", long[len(body)])
	}
}

func TestScanDiffFindsAddedSecretsOnly(t *testing.T) {
	key := "AKIA" + "ABCDEFGHIJKLMNOP"
	diff := "diff --git a/cfg.go b/cfg.go\n" +
		"--- a/cfg.go\n" +
		"+++ b/cfg.go\n" +
		"@@ -10,1 +10,3 @@\n" +
		"-old := \"" + key + "\"\n" + // removed: not what this change adds
		"+a := 1\n" +
		"+k := \"AKIA\u200b" + "ABCDEFGHIJKLMNOP\"\n" + // a zero-width space must not hide it
		"++++ \"" + key + "\"\n" + // an added line starting "+++" inside a hunk
		"diff --git a/b.md b/b.md\n" +
		"--- a/b.md\n" +
		"+++ b/b.md\n" +
		"@@ -0,0 +1 @@\n" +
		"+nothing here\n"
	got := ScanDiff(diff)
	if len(got) != 2 {
		t.Fatalf("findings = %+v, want two in cfg.go", got)
	}
	for i, wantLine := range []int{11, 12} {
		if got[i].File != "cfg.go" || got[i].Line != wantLine {
			t.Errorf("finding %d = %+v, want cfg.go:%d", i, got[i], wantLine)
		}
		if strings.Contains(got[i].Pattern, "ABCDEFGH") {
			t.Errorf("a finding carries the value: %q", got[i].Pattern)
		}
	}
}
