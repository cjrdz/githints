package notes

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const sample = `---
title: Auth Refactor Plan
tags: [auth, Refactor]
related_files: [internal/auth/token.go, internal/auth/session.go]
related_changes: [142, 143]
---

Move token rotation out of the handler. See [[token-notes]] and [[internal/auth/token.go]].
`

func TestParseFullFrontMatter(t *testing.T) {
	d, err := Parse("auth-plan", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	want := Doc{
		Title:   "Auth Refactor Plan",
		Tags:    []string{"auth", "refactor"},
		Files:   []string{"internal/auth/token.go", "internal/auth/session.go"},
		Changes: []int64{142, 143},
		Body:    "Move token rotation out of the handler. See [[token-notes]] and [[internal/auth/token.go]].\n",
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("parsed %+v\nwant   %+v", d, want)
	}
	if got := d.Links(); !reflect.DeepEqual(got, []string{"token-notes", "internal/auth/token.go"}) {
		t.Errorf("links = %v", got)
	}
}

func TestParseWithoutFrontMatter(t *testing.T) {
	d, err := Parse("my-note", []byte("# The Heading\n\ntext\n"))
	if err != nil || d.Title != "The Heading" {
		t.Errorf("title from heading: %+v, %v", d, err)
	}
	d, err = Parse("my-note", []byte("just text\n"))
	if err != nil || d.Title != "my-note" {
		t.Errorf("title from name: %+v, %v", d, err)
	}
}

func TestParseTolerates(t *testing.T) {
	for name, in := range map[string]string{
		"crlf":         "---\r\ntitle: T\r\n---\r\nbody\r\n",
		"bom":          "\xef\xbb\xbf---\ntitle: T\n---\nbody\n",
		"unknown keys": "---\ntitle: T\ndate: 2026-01-01\nauthor: someone\naliases: [x]\n---\nbody\n",
		"comments":     "---\n# a comment\ntitle: T\n\n---\nbody\n",
		"no body":      "---\ntitle: T\n---",
		"empty list":   "---\ntitle: T\ntags: []\n---\nbody\n",
		"bare scalar":  "---\ntitle: T\ntags: solo\n---\nbody\n",
		"quoted":       "---\ntitle: \"T\"\ntags: [\"a\", 'b']\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			d, err := Parse("n", []byte(in))
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if d.Title != "T" {
				t.Errorf("title = %q", d.Title)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	big := strings.Repeat("x", MaxBodyBytes+1)
	many := func(n int, f string) string {
		var items []string
		for i := 0; i < n; i++ {
			items = append(items, fmt.Sprintf(f, i))
		}
		return strings.Join(items, ", ")
	}
	for name, in := range map[string]string{
		"not closed":       "---\ntitle: T\nbody\n",
		"duplicate key":    "---\ntitle: A\ntitle: B\n---\n",
		"no colon":         "---\njust text\n---\n",
		"unterminated \"":  "---\ntitle: \"oops\n---\n",
		"unclosed list":    "---\ntags: [a, b\n---\n",
		"empty item":       "---\ntags: [a, , b]\n---\n",
		"bad tag":          "---\ntags: [has space]\n---\n",
		"tag with slash":   "---\ntags: [a/b]\n---\n",
		"traversal":        "---\nrelated_files: [../outside]\n---\n",
		"absolute":         "---\nrelated_files: [/etc/passwd]\n---\n",
		"control in path":  "---\nrelated_files: [a\x07b]\n---\n",
		"block list":       "---\ntags:\n  - a\n  - b\n---\n",
		"changes not num":  "---\nrelated_changes: [abc]\n---\n",
		"change zero":      "---\nrelated_changes: [0]\n---\n",
		"change negative":  "---\nrelated_changes: [-3]\n---\n",
		"too many tags":    "---\ntags: [" + many(MaxTags+1, "t%d") + "]\n---\n",
		"too many files":   "---\nrelated_files: [" + many(MaxRelatedFiles+1, "f%d.go") + "]\n---\n",
		"too many changes": "---\nrelated_changes: [" + many(MaxRelatedChanges+1, "%d") + "]\n---\n",
		"body too big":     "---\ntitle: T\n---\n" + big,
		"title too long":   "---\ntitle: " + strings.Repeat("t", MaxTitleLen+1) + "\n---\n",
		"too many links":   "---\ntitle: T\n---\n" + many(MaxLinks+1, "[[note-%d]] "),
		"front too large":  "---\ntitle: T\n" + strings.Repeat("# pad\n", maxFrontMatterBytes/6+10) + "---\n",
		"front too long":   "---\n" + strings.Repeat("# c\n", maxFrontMatterLines+1) + "---\n",
		"invalid utf8":     "---\ntitle: T\n---\n\xff\xfe",
		"secret in body":   "---\ntitle: T\n---\nkey AKIAIOSFODNN7EXAMPLE\n",
		"secret in title":  "---\ntitle: AKIAIOSFODNN7EXAMPLE\n---\n",
		"hidden secret":    "---\ntitle: T\n---\nkey AKIA\u200bIOSFODNN7EXAMPLE\n",
		"bidi hidden":      "---\ntitle: T\n---\nkey AKIA\u202eIOSFODNN7EXAMPLE\n",
		"secret in a tag":  "---\ntags: [AKIAIOSFODNN7EXAMPLE]\n---\n",
		"empty everything": "---\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			if name == "empty everything" {
				// A title falls back to the name, so this one is legal.
				if _, err := Parse("n", []byte(in)); err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			if _, err := Parse("n", []byte(in)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// The front matter is not YAML. Constructs that mean something to a YAML
// parser are plain characters here, so a hostile file has nothing to trigger.
func TestFrontMatterIsNotYAML(t *testing.T) {
	d, err := Parse("n", []byte("---\ntitle: &a !!python/object:os.system *a\ntags: [x]\n<<: *a\nbig: |\n  multi\n  line\naliases:\n  - x\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "&a !!python/object:os.system *a" {
		t.Errorf("title = %q, want the text taken literally", d.Title)
	}
}

func TestTextIsStrippedOfControls(t *testing.T) {
	d, err := Parse("n", []byte("---\ntitle: Hi\x1b[31m there\u202e\n---\nbody\x1b]0;x\x07 text\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(d.Title+d.Body, "\x1b\x07\u202e") {
		t.Errorf("controls survived: %q %q", d.Title, d.Body)
	}
}

func TestNames(t *testing.T) {
	for _, ok := range []string{"a", "auth-plan", "0day", "a-b-c", strings.Repeat("a", 64)} {
		if !ValidName(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "A", "-a", "a_b", "a.b", "a/b", "a\\b", "../x", "..", ".", "a b", "é", "a\x00b", "a\nb", strings.Repeat("a", 65), "con.md.md"} {
		if ValidName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if n, err := NormalizeName("auth-plan.md"); err != nil || n != "auth-plan" {
		t.Errorf("NormalizeName = %q, %v", n, err)
	}
	if _, err := NormalizeName("../../etc/passwd"); err == nil {
		t.Error("path accepted as a name")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Auth Refactor Plan":     "auth-refactor-plan",
		"  --Why we chose X!  ":  "why-we-chose-x",
		"a/b\\c..d":              "a-b-c-d",
		"日本語":                    "",
		"!!!":                    "",
		"Ünïcode mix 2":          "n-code-mix-2",
		strings.Repeat("a", 100): strings.Repeat("a", 64),
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinks(t *testing.T) {
	d := Doc{Body: "[[a-note]] [[a-note]] [[b-note.md|alias]] [[internal/x.go#heading]] " +
		"[[Weird Name]] [[../etc/passwd]] [[/abs]] [[]] [[ spaced ]] text [not a link]"}
	// Anything that is a legal note name or repo-relative path is a link; a
	// file may well be called "Weird Name". Traversal and absolute paths are not.
	want := []string{"a-note", "b-note.md", "internal/x.go", "Weird Name", "spaced"}
	if got := d.Links(); !reflect.DeepEqual(got, want) {
		t.Errorf("links = %v, want %v", got, want)
	}
}

func TestMarshalRoundTrips(t *testing.T) {
	for _, title := range []string{"Plain", `"Quoted" start`, "[bracket] start", "'single' start", ` padded `, `back\slash "and" quote`, "colon: inside"} {
		d := Doc{Title: title, Tags: []string{"a", "b"}, Files: []string{"x/y.go"}, Changes: []int64{9, 10, 8}, Body: "\n\nhello\n\n\n"}
		if err := d.Check(""); err != nil {
			t.Fatalf("%q: %v", title, err)
		}
		got, err := Parse("n", d.Marshal())
		if err != nil {
			t.Fatalf("%q did not read back: %v\n%s", title, err, d.Marshal())
		}
		if !reflect.DeepEqual(got, d) {
			t.Errorf("%q round trip:\n got %+v\nwant %+v", title, got, d)
		}
	}
}

// Whatever a file holds, Parse neither panics nor returns something that
// violates the caps, and anything it accepts survives being written and read
// again.
func FuzzParse(f *testing.F) {
	for _, s := range []string{sample, "", "---\n---\n", "---\ntitle: \"\n---\n", "no front matter", "---\ntags: [,]\n---", "[[[[a]]]]", "---\r\ntitle: x\r\n---\r\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := Parse("fuzz", data)
		if err != nil {
			return
		}
		if len(d.Body) > MaxBodyBytes || utf8.RuneCountInString(d.Title) > MaxTitleLen || d.Title == "" {
			t.Fatalf("caps violated: %+v", d)
		}
		if len(d.Tags) > MaxTags || len(d.Files) > MaxRelatedFiles || len(d.Changes) > MaxRelatedChanges || len(d.Links()) > MaxLinks {
			t.Fatalf("list caps violated: %+v", d)
		}
		if strings.ContainsAny(d.Title, "\n\r\x1b") || strings.ContainsRune(d.Body, 0x1b) {
			t.Fatalf("control characters survived: %+v", d)
		}
		again, err := Parse("fuzz", d.Marshal())
		if err != nil {
			t.Fatalf("accepted note does not read back: %v\n%s", err, d.Marshal())
		}
		if !reflect.DeepEqual(again, d) {
			t.Fatalf("round trip changed the note:\n got %+v\nwant %+v", again, d)
		}
	})
}
