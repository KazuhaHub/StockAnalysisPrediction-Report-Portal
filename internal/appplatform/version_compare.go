package appplatform

import (
	"strings"
)

// CompareVersions orders two valid versions by SemVer 2.0 precedence: -1 when a is older than b, 0 when equal,
// 1 when newer. A prerelease sorts below its release, prerelease identifiers compare dot by dot (numeric below
// alphanumeric, a shorter prefix below a longer list), and build metadata is ignored. Inputs are expected to have
// passed validVersion; an unparsable number compares as zero rather than panicking.
func CompareVersions(a, b string) int {
	coreA, preA, hasPreA := splitVersion(a)
	coreB, preB, hasPreB := splitVersion(b)
	for i := range 3 {
		if c := compareNumeric(coreA[i], coreB[i]); c != 0 {
			return c
		}
	}
	switch {
	case !hasPreA && !hasPreB:
		return 0
	case !hasPreA:
		return 1
	case !hasPreB:
		return -1
	}
	for i := 0; i < len(preA) && i < len(preB); i++ {
		if c := comparePrereleaseIdentifier(preA[i], preB[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(preA) < len(preB):
		return -1
	case len(preA) > len(preB):
		return 1
	}
	return 0
}

func splitVersion(v string) (core [3]string, pre []string, hasPre bool) {
	v, _, _ = strings.Cut(v, "+")
	v, p, hasPre := strings.Cut(v, "-")
	parts := strings.SplitN(v, ".", 3)
	for i := range core {
		core[i] = "0"
		if i < len(parts) {
			core[i] = parts[i]
		}
	}
	if hasPre {
		pre = strings.Split(p, ".")
	}
	return
}

func isNumeric(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

// compareNumeric compares decimal strings of any length without overflow.
func compareNumeric(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func comparePrereleaseIdentifier(a, b string) int {
	na, nb := isNumeric(a), isNumeric(b)
	switch {
	case na && nb:
		return compareNumeric(a, b)
	case na:
		return -1
	case nb:
		return 1
	}
	return strings.Compare(a, b)
}
