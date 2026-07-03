package llm

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
