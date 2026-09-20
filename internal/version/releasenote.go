package version

import "strings"

// Release metadata (ADR 0034). GitHub Release is the sole authority for mutable publication state:
// its body and pre-release flag may change after artifacts are built. This package only validates
// immutable build tags and constructs their canonical public URLs.

// ReleaseNotesBaseURL is the public repository whose release pages the notes link to.
const ReleaseNotesBaseURL = "https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal"

// IsReleaseTag reports whether tag is a well-formed CalVer release tag, vYYYY.W[.R]. The rules
// mirror scripts/lib/calver.sh — the component that actually cuts the tag — so a link is only ever
// built for a tag that can exist. ISO week 53 is accepted without checking that the named year
// carries one: a wrong week would be a link to a release page that simply does not exist, which is
// the same outcome as any other unbuilt tag, and reimplementing the ISO calendar here would be a
// second copy of a rule the release scripts already own.
func IsReleaseTag(tag string) bool {
	if len(tag) < 7 || tag[0] != 'v' { // the shortest well-formed tag is vYYYY.W
		return false
	}
	parts := strings.Split(tag[1:], ".")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	if len(parts[0]) != 4 || !allDigits(parts[0]) || parts[0][0] == '0' {
		return false
	}
	year := atoi(parts[0])
	if year < 2000 {
		return false
	}
	if len(parts[1]) < 1 || len(parts[1]) > 2 || !allDigits(parts[1]) || parts[1][0] == '0' {
		return false
	}
	week := atoi(parts[1])
	if week < 1 || week > 53 {
		return false
	}
	if len(parts) == 3 {
		if len(parts[2]) < 1 || !allDigits(parts[2]) || parts[2][0] == '0' {
			return false
		}
		if atoi(parts[2]) < 1 {
			return false
		}
	}
	return true
}

// ReleaseURL is the GitHub release page for tag, or "" when tag is not a release the portal can
// point at. A diagnostic build ("dev", "ci") and the retired v0.x line both answer "": inventing a
// link for either would send a reader to a page that does not exist.
func ReleaseURL(tag string) string {
	if !IsReleaseTag(tag) {
		return ""
	}
	return ReleaseNotesBaseURL + "/releases/tag/" + tag
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}
