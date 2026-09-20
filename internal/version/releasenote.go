package version

import (
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
)

// Release notes (ADR 0034).
//
// The update prompt offers to show what changed. Fetching that from GitHub would put the primary
// reading experience behind a third party's availability, the reader's GitHub credentials and an
// unauthenticated rate limit — none of which the portal can promise. So the note a build ships is
// compiled into it: the release pipeline copies the tagged commit's own committed note,
// `docs/releases/<YYYY>/<tag>.md`, to releaseNoteFile before `go build`, and a release whose note is
// missing fails in the setup job rather than shipping without one.
//
// The in-app body is that committed note, and the GitHub link is the published release page: the two
// are deliberately not claimed to be identical, because GitHub's page also carries its generated PR
// list and any edits made at publication time.

// ReleaseNotesBaseURL is the public repository whose release pages the notes link to.
const ReleaseNotesBaseURL = "https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal"

const (
	// releaseNoteFile is where the pipeline injects the tagged commit's note. It is gitignored, and
	// it has to be: `go build` stamps a binary built from a dirty worktree with `vcs.modified=true`,
	// and the release check refuses that. The same reasoning already governs internal/web/dist.
	releaseNoteFile = "notes/release.md"
	// placeholderFile is tracked, only so that the embed pattern always matches at least one file
	// and a fresh clone compiles. Its text is never served — a build that has not had a note
	// injected reports no note at all.
	placeholderFile = "notes/placeholder.md"
	historyFile     = "notes/history.json"
)

//go:embed notes/*.md notes/*.json
var notes embed.FS

// ReleaseNote is one committed entry in the offline history shipped with a release build.
type ReleaseNote struct {
	Tag      string `json:"tag"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

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

// PackagedNote returns the release note compiled into this binary, and whether there is one. A
// diagnostic build, a retired tag, and a release build whose note step did not run all answer "no
// note" — the update prompt says so rather than showing an empty success state or the placeholder.
func PackagedNote() (string, bool) {
	if !IsReleaseTag(Version) {
		return "", false
	}
	body, err := fs.ReadFile(notes, releaseNoteFile)
	if err != nil {
		return "", false
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "", false
	}
	return text, true
}

// PackagedHistory returns the CalVer notes committed no later than this build. The release pipeline
// generates the archive from docs/releases before compiling; diagnostic builds have no archive.
func PackagedHistory() []ReleaseNote {
	body, err := fs.ReadFile(notes, historyFile)
	if err != nil {
		return nil
	}
	return parseReleaseHistory(body)
}

func parseReleaseHistory(body []byte) []ReleaseNote {
	var entries []ReleaseNote
	if json.Unmarshal(body, &entries) != nil {
		return nil
	}
	out := make([]ReleaseNote, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		entry.Tag = strings.TrimSpace(entry.Tag)
		entry.Title = strings.TrimSpace(entry.Title)
		entry.Markdown = strings.TrimSpace(entry.Markdown)
		if !IsReleaseTag(entry.Tag) || entry.Markdown == "" || seen[entry.Tag] {
			continue
		}
		seen[entry.Tag] = true
		out = append(out, entry)
	}
	return out
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
