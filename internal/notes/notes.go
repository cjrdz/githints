// Package notes holds the markdown pages agents and people keep in
// .githints/notes/: context that is not a change ("why we chose X", "the plan
// for the auth refactor") and would otherwise be rediscovered every session.
//
// The files are the source of truth; index.db holds a search index over them
// that can be rebuilt from the directory at any time. Notes are NOT part of
// the integrity-verified change log: nothing signs them, so whoever can write
// the directory can write a note, and everything that shows one says so.
//
// A note is untrusted text in a second sense too. It is written by other
// agents, by people, and by git operations, and it reaches the model through
// MCP. So the whole path is defensive: names are restricted to a safe
// alphabet, every read goes through an os.Root and refuses links, size and
// count are capped, text is stripped of terminal escapes before the secret
// scan, a note matching a credential pattern is refused, and nothing a note
// says is ever treated as an instruction.
package notes

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/secrets"
	"github.com/cjrdz/githints/internal/textsafe"
)

// Dir is where notes live, under .githints/.
const Dir = "notes"

// Caps. Each is enforced in Go: an MCP schema is advice, and a hand-edited file
// never saw one.
const (
	MaxBodyBytes      = 32 << 10
	MaxFileBytes      = 128 << 10 // a hand-edited file larger than this is skipped
	MaxTitleLen       = 200
	MaxTags           = 20
	MaxTagLen         = 40
	MaxRelatedFiles   = 50
	MaxRelatedChanges = 50
	MaxLinks          = 100
	MaxNotes          = 500

	maxFrontMatterBytes = 8 << 10
	maxFrontMatterLines = 64
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	tagRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)
	// A wikilink target, with an optional |alias or #heading that is ignored.
	linkRe = regexp.MustCompile(`\[\[([^\[\]\n|#]{1,200})(?:[|#][^\[\]\n]{0,200})?\]\]`)
)

// ValidName reports whether s is a legal note name (no extension): lowercase
// letters, digits and hyphens, starting with a letter or digit. The alphabet is
// the defense against path tricks: there is no dot, slash or backslash in it.
func ValidName(s string) bool { return nameRe.MatchString(s) && !deviceName[s] }

// deviceName holds the names Windows reserves for devices, with or without an
// extension. A note called con.md cannot be created there, and one that arrived
// in a clone cannot be opened, so the name is refused everywhere.
var deviceName = func() map[string]bool {
	m := map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}
	for i := '1'; i <= '9'; i++ {
		m["com"+string(i)] = true
		m["lpt"+string(i)] = true
	}
	return m
}()

// NormalizeName accepts a note name with or without its .md suffix.
func NormalizeName(s string) (string, error) {
	n := strings.TrimSuffix(s, ".md")
	if !ValidName(n) {
		return "", fmt.Errorf("invalid note name %q: use lowercase letters, digits and hyphens (at most 64)", s)
	}
	return n, nil
}

// Slug derives a note name from a title: ASCII letters and digits, hyphen
// separated. It returns "" when nothing usable is left.
func Slug(title string) string {
	var b strings.Builder
	dash := true // swallow leading separators
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}

// Doc is a parsed note.
type Doc struct {
	Title   string
	Tags    []string
	Files   []string
	Changes []int64
	Body    string
}

// DocOf rebuilds the document an indexed note was parsed from.
func DocOf(n index.Note) Doc {
	return Doc{Title: n.Title, Tags: n.Tags, Files: n.Files, Changes: n.Changes, Body: n.Body}
}

// Recheck re-applies every rule to a note read back from the index. The index
// is a cache in a file anyone who can write it can edit, and Sync trusts a row
// whose size and mtime still match its file, so a note is checked again before it
// is shown in full or exported rather than taken on the index's word.
func Recheck(n index.Note) (Doc, error) {
	if !ValidName(n.Name) {
		return Doc{}, fmt.Errorf("invalid note name %q", n.Name)
	}
	d := DocOf(n)
	if err := d.Check(""); err != nil {
		return Doc{}, err
	}
	return d, nil
}

// Links returns the wikilink targets in the body that name a note or a
// repository file, in order and without repeats. Anything else in double
// brackets is just text.
func (d Doc) Links() []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range linkRe.FindAllStringSubmatch(d.Body, -1) {
		t := strings.TrimSpace(m[1])
		if t == "" || seen[t] {
			continue
		}
		if _, err := NormalizeName(t); err != nil && recorder.ValidateFilePath(t) != nil {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// canonBody gives the body one canonical form (no leading or trailing blank
// lines, one final newline) so a note survives being written and parsed again.
func canonBody(s string) string {
	s = strings.Trim(s, "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

// Check normalizes d in place and enforces every rule a note must meet. It is
// what Parse and Record both end with, so a hand-written file and one an agent
// recorded are held to the same standard.
func (d *Doc) Check(fallbackTitle string) error {
	// Strip invisible and control characters first: a zero-width space inside
	// a key would otherwise hide it from the secret scan below.
	oneLine := func(s string) string { return strings.Join(strings.Fields(textsafe.StripControls(s)), " ") }
	d.Title = oneLine(d.Title)
	d.Body = canonBody(textsafe.StripControls(strings.ReplaceAll(d.Body, "\r\n", "\n")))
	if d.Title == "" {
		d.Title = oneLine(fallbackTitle) // a fallback is untrusted text too
	}
	if d.Title == "" {
		return errors.New("a note needs a title")
	}
	if utf8.RuneCountInString(d.Title) > MaxTitleLen {
		return fmt.Errorf("title too long (max %d characters)", MaxTitleLen)
	}
	if len(d.Body) > MaxBodyBytes {
		return fmt.Errorf("body too long: %d bytes (max %d)", len(d.Body), MaxBodyBytes)
	}

	// Tags are lowercased below, which would hide an upper-case key from the
	// scan, so the raw values are scanned first.
	if len(d.Tags) > MaxTags {
		return fmt.Errorf("too many tags: %d (max %d)", len(d.Tags), MaxTags)
	}
	rawTags := make([]string, len(d.Tags))
	for i, t := range d.Tags {
		rawTags[i] = textsafe.Remove(t)
	}
	if pattern, ok := secrets.Scan(strings.Join(rawTags, " ")); ok {
		return fmt.Errorf("refusing: the note matches a known secret pattern (%s)", pattern)
	}
	tags, err := cleanTags(d.Tags)
	if err != nil {
		return err
	}
	d.Tags = tags
	if d.Files, err = cleanFiles(d.Files); err != nil {
		return err
	}
	if d.Changes, err = cleanChanges(d.Changes); err != nil {
		return err
	}
	if n := len(d.Links()); n > MaxLinks {
		return fmt.Errorf("too many wikilinks: %d (max %d)", n, MaxLinks)
	}

	for _, text := range []string{d.Title, strings.Join(d.Tags, " "), strings.Join(d.Files, " "), d.Body} {
		if pattern, ok := secrets.Scan(text); ok {
			return fmt.Errorf("refusing: the note matches a known secret pattern (%s)", pattern)
		}
	}
	return nil
}

func cleanTags(in []string) ([]string, error) {
	if len(in) > MaxTags {
		return nil, fmt.Errorf("too many tags: %d (max %d)", len(in), MaxTags)
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(textsafe.Remove(t)))
		if !tagRe.MatchString(t) {
			return nil, fmt.Errorf("invalid tag %q: letters, digits, '_', '.' and '-', at most %d characters", t, MaxTagLen)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("too many tags: %d (max %d)", len(out), MaxTags)
	}
	return out, nil
}

func cleanFiles(in []string) ([]string, error) {
	if len(in) > MaxRelatedFiles {
		return nil, fmt.Errorf("too many related files: %d (max %d)", len(in), MaxRelatedFiles)
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range in {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if err := recorder.ValidateFilePath(f); err != nil {
			return nil, fmt.Errorf("related file: %w", err)
		}
		// The front matter is a flat list split on commas.
		if strings.ContainsAny(f, ",[]") {
			return nil, fmt.Errorf("related file %q: ',', '[' and ']' cannot be used in a related path", f)
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	if len(out) > MaxRelatedFiles {
		return nil, fmt.Errorf("too many related files: %d (max %d)", len(out), MaxRelatedFiles)
	}
	return out, nil
}

func cleanChanges(in []int64) ([]int64, error) {
	if len(in) > MaxRelatedChanges {
		return nil, fmt.Errorf("too many related changes: %d (max %d)", len(in), MaxRelatedChanges)
	}
	var out []int64
	seen := map[int64]bool{}
	for _, c := range in {
		if c < 1 {
			return nil, fmt.Errorf("related change id must be positive, got %d", c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) > MaxRelatedChanges {
		return nil, fmt.Errorf("too many related changes: %d (max %d)", len(out), MaxRelatedChanges)
	}
	return out, nil
}

// Parse reads a note file. The front matter is a deliberately small subset of
// YAML, parsed by hand: no anchors, aliases, tags, nesting or multi-line
// values, so a hostile file has nothing to exploit.
//
//	---
//	title: Auth Refactor Plan
//	tags: [auth, refactor]
//	related_files: [internal/auth/token.go]
//	related_changes: [142, 143]
//	---
//
// Other keys are ignored (Obsidian adds some). A file with no front matter is
// a note whose title is its first "# " heading, or its name.
func Parse(name string, data []byte) (Doc, error) {
	if !utf8.Valid(data) {
		return Doc{}, errors.New("not valid UTF-8")
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\xef\xbb\xbf"), "\r\n", "\n")

	var d Doc
	body := text
	if front, rest, ok := splitFrontMatter(text); ok {
		if len(front) > maxFrontMatterBytes {
			return Doc{}, fmt.Errorf("front matter too large (max %d bytes)", maxFrontMatterBytes)
		}
		if err := parseFront(front, &d); err != nil {
			return Doc{}, fmt.Errorf("front matter: %w", err)
		}
		body = rest
	} else if strings.HasPrefix(text, "---\n") {
		return Doc{}, errors.New("front matter is not closed with a line of ---")
	}
	d.Body = body

	fallback := name
	if d.Title == "" {
		for _, line := range strings.Split(body, "\n") {
			if t, ok := strings.CutPrefix(line, "# "); ok {
				fallback = t
				break
			}
		}
	}
	if err := d.Check(fallback); err != nil {
		return Doc{}, err
	}
	return d, nil
}

// splitFrontMatter returns the text between a leading "---" line and the next
// "---" line, and what follows.
func splitFrontMatter(text string) (front, rest string, ok bool) {
	after, found := strings.CutPrefix(text, "---\n")
	if !found {
		return "", "", false
	}
	if strings.HasPrefix(after, "---\n") || after == "---" { // empty front matter
		return "", strings.TrimPrefix(after, "---"), true
	}
	if i := strings.Index(after, "\n---\n"); i >= 0 {
		return after[:i], after[i+len("\n---\n"):], true
	}
	if front, found := strings.CutSuffix(after, "\n---"); found {
		return front, "", true
	}
	return "", "", false
}

func parseFront(front string, d *Doc) error {
	lines := strings.Split(front, "\n")
	if len(lines) > maxFrontMatterLines {
		return fmt.Errorf("too many lines (max %d)", maxFrontMatterLines)
	}
	seen := map[string]bool{}
	known := false // is the key above one we read?
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if raw[0] == ' ' || raw[0] == '\t' {
			// A continuation of the key above: a block list or a folded
			// string. Ignored under a key we do not read (Obsidian writes
			// these); refused under one we do, since half-reading a list
			// would be worse than saying so.
			if known {
				return errors.New("multi-line values are not supported: write the list on one line, like [a, b]")
			}
			continue
		}
		known = false
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("expected key: value, got %q", clip(line))
		}
		key = strings.ToLower(strings.TrimSpace(key))
		switch key {
		case "title", "tags", "related_files", "related_changes":
			known = true
		default:
			continue // unknown keys are ignored, not an error
		}
		if seen[key] {
			return fmt.Errorf("%s given twice", key)
		}
		seen[key] = true
		val = strings.TrimSpace(val)
		switch key {
		case "title":
			s, err := scalar(val)
			if err != nil {
				return fmt.Errorf("title: %w", err)
			}
			d.Title = s
		case "tags", "related_files":
			items, err := list(val)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			if key == "tags" {
				d.Tags = items
			} else {
				d.Files = items
			}
		case "related_changes":
			items, err := list(val)
			if err != nil {
				return fmt.Errorf("related_changes: %w", err)
			}
			for _, it := range items {
				n, err := strconv.ParseInt(it, 10, 64)
				if err != nil {
					return fmt.Errorf("related_changes: %q is not a number", clip(it))
				}
				d.Changes = append(d.Changes, n)
			}
		}
	}
	return nil
}

// scalar reads one value: bare text, or text in double or single quotes. Inside
// double quotes \" and \\ are the only escapes.
func scalar(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	switch v[0] {
	case '"':
		if len(v) < 2 || v[len(v)-1] != '"' {
			return "", errors.New("unterminated quote")
		}
		inner := v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) && (inner[i+1] == '"' || inner[i+1] == '\\') {
				i++
			}
			b.WriteByte(inner[i])
		}
		return b.String(), nil
	case '\'':
		if len(v) < 2 || v[len(v)-1] != '\'' {
			return "", errors.New("unterminated quote")
		}
		return v[1 : len(v)-1], nil
	}
	return v, nil
}

// list reads "[a, b, c]", or a single bare value as a one-item list.
func list(v string) ([]string, error) {
	if v == "" {
		return nil, nil
	}
	if v[0] != '[' {
		s, err := scalar(v)
		if err != nil {
			return nil, err
		}
		return []string{s}, nil
	}
	if v[len(v)-1] != ']' {
		return nil, errors.New("list is not closed with ]")
	}
	inner := strings.TrimSpace(v[1 : len(v)-1])
	if inner == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(inner, ",") {
		s, err := scalar(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		if s == "" {
			return nil, errors.New("empty list item")
		}
		out = append(out, s)
	}
	return out, nil
}

func clip(s string) string {
	if utf8.RuneCountInString(s) > 40 {
		return string([]rune(s)[:40]) + "..."
	}
	return s
}

// Marshal writes d as a note file. d must already satisfy Check.
func (d Doc) Marshal() []byte {
	var b strings.Builder
	b.WriteString("---\n")
	// Check already made the title one line; a Doc that skipped it must not be
	// able to start another front matter line.
	fmt.Fprintf(&b, "title: %s\n", quoteScalar(strings.Join(strings.Fields(d.Title), " ")))
	if len(d.Tags) > 0 {
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(d.Tags, ", "))
	}
	if len(d.Files) > 0 {
		fmt.Fprintf(&b, "related_files: [%s]\n", strings.Join(d.Files, ", "))
	}
	if len(d.Changes) > 0 {
		ids := make([]string, len(d.Changes))
		for i, c := range d.Changes {
			ids[i] = strconv.FormatInt(c, 10)
		}
		fmt.Fprintf(&b, "related_changes: [%s]\n", strings.Join(ids, ", "))
	}
	b.WriteString("---\n")
	if d.Body != "" {
		b.WriteString("\n" + d.Body)
	}
	return []byte(b.String())
}

// quoteScalar quotes a value only when a bare one would be read back
// differently.
func quoteScalar(s string) string {
	if s != "" && s == strings.TrimSpace(s) && !strings.ContainsAny(s[:1], `"'[`) {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
