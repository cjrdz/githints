package update

import (
	"regexp"
	"strconv"
	"strings"
)

// tagRe matches a release tag as goreleaser names it: vMAJOR.MINOR.PATCH with
// an optional pre-release. It is also what keeps a tag from the network out of
// paths and URLs: nothing else gets through.
var tagRe = regexp.MustCompile(`^v?(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-([0-9A-Za-z.-]{1,64}))?$`)

// Version is a parsed release version. A goreleaser snapshot is stamped
// "<last tag>-next", built after that tag; Next marks it, and it sorts after
// its base rather than before it as a semver pre-release would.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
	Next                bool
}

// ParseVersion reads "0.4.0", "v0.4.0", "v0.5.0-rc.1" or "v0.4.0-next". A dev
// build ("dev", "") does not parse.
func ParseVersion(s string) (Version, bool) {
	m := tagRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, false
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1]) // the pattern admits digits only
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	if m[4] == "next" {
		v.Next = true
	} else {
		v.Pre = m[4]
	}
	return v, true
}

func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	switch {
	case v.Next:
		s += "-next"
	case v.Pre != "":
		s += "-" + v.Pre
	}
	return s
}

// Compare orders a before b (-1), equal (0) or after (1). A pre-release sorts
// before its release, a snapshot after it, and pre-releases compare by their
// dot-separated identifiers as semver does.
func Compare(a, b Version) int {
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	rank := func(v Version) int {
		switch {
		case v.Pre != "":
			return 0
		case v.Next:
			return 2
		}
		return 1
	}
	if d := rank(a) - rank(b); d != 0 {
		return sign(d)
	}
	return comparePre(a.Pre, b.Pre)
}

func comparePre(a, b string) int {
	if a == b {
		return 0
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aErr == nil:
			return -1 // numeric identifiers sort before alphanumeric ones
		case bErr == nil:
			return 1
		case as[i] != bs[i]:
			return strings.Compare(as[i], bs[i])
		}
	}
	return sign(len(as) - len(bs))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
