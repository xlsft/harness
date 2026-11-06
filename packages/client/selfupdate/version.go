package selfupdate

import (
	"strconv"
	"strings"
)

type version struct {
	core [3]int
	pre  string
}

// parseVersion reads vX.Y.Z or X.Y.Z with an optional -prerelease.
func parseVersion(s string) (version, bool) {
	s, pre, hasPre := strings.Cut(strings.TrimPrefix(s, "v"), "-")
	if hasPre && pre == "" {
		return version{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	v.pre = pre
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		v.core[i] = n
	}
	return v, true
}

// Newer reports whether release tag is newer than the current version. A pre-release sorts before its own final version.
func Newer(tag, current string) bool {
	a, ok := parseVersion(tag)
	if !ok {
		return false
	}
	b, ok := parseVersion(current)
	if !ok {
		return true
	}
	for i := range 3 {
		if a.core[i] != b.core[i] {
			return a.core[i] > b.core[i]
		}
	}
	if a.pre == "" || b.pre == "" {
		return a.pre == "" && b.pre != ""
	}
	return comparePre(a.pre, b.pre) > 0
}

// comparePre orders pre-release identifiers by semver's rules: dot-separated, numbers numerically and below words.
func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return an - bn
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// IsDevBuild reports a binary not built by the release workflow (config.Version keeps its default).
func IsDevBuild(current string) bool {
	return strings.HasSuffix(current, "-dev")
}
