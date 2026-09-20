package version

import (
	"encoding/json"
	"io/fs"
	"testing"
)

func TestIsReleaseTag(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"v2026.38", true},    // week-only: the first release of that week
		{"v2026.38.1", true},  // three-component
		{"v2026.38.12", true}, // revision is not limited to one digit
		{"v2026.1", true},
		{"v2026.7", true}, // a single-digit week is well formed
		// The one-digit boundary. A week carries no leading zero, so v2026.9 is a string prefix of
		// nothing that follows it — v2026.10 is week ten, and a matcher that tested one tag as a
		// prefix of another would confuse the two for a third of the year.
		{"v2026.9", true},
		{"v2026.10", true},
		{"v2026.9.9", true},
		{"v2026.9.10", true},
		{"v2026.10.1", true},
		// Week 53 is accepted for any year: checking that the named year carries one would mean a
		// second copy of the ISO calendar rules the release scripts already own, and the only
		// consequence of a wrong week is a link to a release page that does not exist.
		{"v2026.53", true},
		{"v2026.07", false},   // no leading zeroes
		{"v2026.38.0", false}, // a revision starts at 1
		{"v2026.38.01", false},
		{"2026.38", false}, // the display label, not the tag
		{"v0.4.72", false}, // the retired SemVer line is not CalVer
		{"v2026.38.1-rc", false},
		{"v2026.38.1.2", false},
		{"dev", false},
		{"ci", false},
		{"", false},
		{"v1999.1", false}, // the four-digit year must be a real CalVer year
	}
	for _, c := range cases {
		if got := IsReleaseTag(c.in); got != c.want {
			t.Errorf("IsReleaseTag(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestReleaseURL(t *testing.T) {
	if got, want := ReleaseURL("v2026.38.1"), ReleaseNotesBaseURL+"/releases/tag/v2026.38.1"; got != want {
		t.Errorf("ReleaseURL(v2026.38.1) = %q, want %q", got, want)
	}
	// A diagnostic build has no release behind it, and neither does the retired v0.x line: a link
	// invented for either would send a user to a 404.
	for _, v := range []string{"dev", "ci", "none", "", "v0.4.72", "2026.38"} {
		if got := ReleaseURL(v); got != "" {
			t.Errorf("ReleaseURL(%q) = %q, want empty", v, got)
		}
	}
}

// ReleaseURL builds a link from the tag verbatim, so the note for one number can never be presented
// as another's — the boundary the numbering is most likely to get wrong is the one-digit one.
func TestReleaseURLDoesNotConfuseOneDigitNeighbours(t *testing.T) {
	nine := ReleaseURL("v2026.9")
	ten := ReleaseURL("v2026.10")
	if nine == ten {
		t.Fatalf("v2026.9 and v2026.10 resolve to the same release page: %q", nine)
	}
	if nine != ReleaseNotesBaseURL+"/releases/tag/v2026.9" {
		t.Errorf("ReleaseURL(v2026.9) = %q; the tag must reach the URL verbatim", nine)
	}
	if ReleaseURL("v2026.9.9") == ReleaseURL("v2026.9.10") {
		t.Error("v2026.9.9 and v2026.9.10 resolve to the same release page")
	}
}

// A build that has not had a note injected reports no note. The placeholder is embedded — it has to
// be, or the pattern would match nothing and a fresh clone would not compile — and it must never be
// served: only the injected path is read, so the placeholder is unreachable by construction rather
// than by a sentinel check that a stray edit could defeat.
func TestPackagedNoteIsAbsentWithoutAnInjectedNote(t *testing.T) {
	if Version != "dev" {
		t.Skipf("built with version %q; this asserts the un-stamped default", Version)
	}
	if body, ok := PackagedNote(); ok {
		t.Errorf("a diagnostic build reported release notes: %q", body)
	}
	if _, err := fs.ReadFile(notes, placeholderFile); err != nil {
		t.Fatalf("the placeholder must be embedded for the pattern to match: %v", err)
	}
	if got := ReleaseURL(Version); got != "" {
		t.Errorf("ReleaseURL(%q) = %q, want empty for a diagnostic build", Version, got)
	}
}

// A release tag with no injected note is unavailable, not a link-only success — a binary built
// without the pipeline's copy step must say so instead of presenting the placeholder.
func TestPackagedNoteNeedsTheInjectedFileNotJustAReleaseTag(t *testing.T) {
	if Version != "dev" {
		t.Skipf("built with version %q; a release build has the file and cannot exercise this", Version)
	}
	// Same binary, reported as a release tag: still no note, because notes/release.md is absent.
	orig := Version
	Version = "v2026.38.3"
	defer func() { Version = orig }()
	if body, ok := PackagedNote(); ok {
		t.Errorf("a release tag without the injected file reported notes: %q", body)
	}
	if got := ReleaseURL(Version); got == "" {
		t.Error("a well-formed release tag must still have a release link")
	}
}

func TestPackagedHistoryFiltersInvalidAndDuplicateEntries(t *testing.T) {
	body, err := json.Marshal([]ReleaseNote{
		{Tag: "v2026.38.2", Title: " New ", Markdown: " # v2026.38.2 ", Maturity: " beta "},
		{Tag: "v2026.38.2", Title: "duplicate", Markdown: "duplicate", Maturity: "beta"},
		{Tag: "v0.4.72", Markdown: "retired line", Maturity: "release"},
		{Tag: "v2026.38.1", Markdown: "", Maturity: "release"},
		{Tag: "v2026.38", Markdown: "missing maturity"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := parseReleaseHistory(body)
	if len(got) != 1 || got[0].Tag != "v2026.38.2" || got[0].Title != "New" || got[0].Markdown != "# v2026.38.2" || got[0].Maturity != "beta" {
		t.Fatalf("PackagedHistory() = %#v", got)
	}
}
