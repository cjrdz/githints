// Package textsafe neutralizes characters in recorded text that can change
// how it is displayed rather than what it says.
//
// Summaries, reasons, agent ids, file paths and symbol names all come from
// agents, other people, or a cloned repository. Rendered into markdown, a
// newline lets them forge headings and entries; printed to a terminal, an
// escape sequence can rewrite the screen; and a bidi override or zero-width
// character can make text read differently from what it contains.
package textsafe

import (
	"strings"
	"unicode"
)

// unsafeRune reports a rune that never belongs in single-line display text:
// C0 and C1 controls, DEL, line and paragraph separators, and Unicode format
// characters (Cf), which include the bidi overrides and zero-width characters.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) ||
		unicode.Is(unicode.Cf, r) ||
		r == '\u2028' || r == '\u2029'
}

// OneLine replaces each unsafe rune, newlines included, with a space.
func OneLine(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return ' '
		}
		return r
	}, s)
}

// StripControls removes unsafe runes but keeps newline and tab, for text
// that is stored and may legitimately span lines (a reason, say). A carriage
// return is dropped: on a terminal it rewinds the line.
func StripControls(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unsafeRune(r) {
			return -1
		}
		return r
	}, s)
}

// Remove deletes every unsafe rune, newlines included. For short identifiers
// (a commit hash, a branch, a code span) where a space would be wrong too.
func Remove(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return -1
		}
		return r
	}, s)
}

// mdReplacer escapes characters markdown (CommonMark, GFM, and Obsidian's
// extensions) would interpret. Multi-character tokens come first so they win
// over their single-character prefixes.
var mdReplacer = strings.NewReplacer(
	"==", "\\=\\=", // Obsidian highlight
	"%%", "\\%\\%", // Obsidian comment
	"$$", "\\$\\$", // math block
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\\", "\\\\",
	"`", "\\`",
	"*", "\\*",
	"_", "\\_",
	"[", "\\[",
	"]", "\\]",
	"(", "\\(",
	")", "\\)",
	"#", "\\#", // heading, or an Obsidian tag mid-line
	"|", "\\|", // table cell
	"~", "\\~", // GFM strikethrough
)

// Markdown renders a plain-text string safe for inclusion in markdown. It
// prevents HTML injection (<script>, onclick, etc.) and markdown injection
// (links, images, headings) that could be exploited when the hint markdown
// is later rendered in an MCP client's webview. The escaped text remains
// human-readable; markdown formatting from the agent is intentionally lost
// in favor of safety.
//
// Newlines and other control characters become spaces first: the output
// always lands on one line, and a summary that could start a new line could
// forge a heading or a fake entry. What remains to neutralize is a block
// marker at the start of that line (a list item or an ordered list number).
func Markdown(s string) string {
	// Leading spaces would turn four or more into an indented code block.
	s = mdReplacer.Replace(strings.TrimLeft(OneLine(s), " "))
	if s != "" && (s[0] == '-' || s[0] == '+') {
		return "\\" + s
	}
	// "1." opens an ordered list. ("1)" is already escaped by mdReplacer.)
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits < len(s) && s[digits] == '.' {
		return s[:digits] + "\\" + s[digits:]
	}
	return s
}

// CodeSpanText renders a string safe for inclusion between the backticks of
// a `code span` the caller writes. Markdown() is wrong here: its backslashes would render literally inside a code
// span. A backtick or newline is what would break out of the span, so those
// are dropped, along with every other control or format character.
func CodeSpanText(s string) string {
	return strings.ReplaceAll(Remove(s), "`", "")
}

// CodeSpan returns s as a complete inline code span, backticks included.
// Unlike CodeSpanText it keeps backticks in the value -- a Go struct tag in a
// signature, say -- by choosing a fence one longer than the longest backtick
// run inside, as CommonMark allows. Control characters are still removed.
func CodeSpan(s string) string {
	s = Remove(s)
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if longest > 0 {
		// A value that starts or ends with a backtick would merge with the
		// fence; CommonMark strips one space of padding on each side.
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}
