package textsafe

import "testing"

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                  "plain",
		"a\nb\r\nc":              "a b  c",
		"bell\x07 esc\x1b[2J":    "bell  esc [2J",
		"c1\u009b31m":            "c1 31m",
		"rtl\u202eevil":          "rtl evil",
		"zero\u200bwidth":        "zero width",
		"line\u2028sep":          "line sep",
		"tab\there":              "tab here",
		"unicode ok: héllo 世界 🙂": "unicode ok: héllo 世界 🙂",
	} {
		if got := OneLine(in); got != want {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripControls(t *testing.T) {
	for in, want := range map[string]string{
		"keep\nlines\tand tabs": "keep\nlines\tand tabs",
		"drop\r\x1b[31mred":     "drop[31mred",
		"bidi\u202e\u2066x":     "bidix",
	} {
		if got := StripControls(in); got != want {
			t.Errorf("StripControls(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodeSpanKeepsBackticks(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                       "`plain`",
		"Name string `json:\"name\"`": "`` Name string `json:\"name\"` ``",
		"a``b":                        "``` a``b ```",
		"line\nbreak":                 "`linebreak`",
	} {
		if got := CodeSpan(in); got != want {
			t.Errorf("CodeSpan(%q) = %q, want %q", in, got, want)
		}
	}
}
