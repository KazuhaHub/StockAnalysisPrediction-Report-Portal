package app

import (
	"net/http"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

// Update prompt policy (docs/version-display-and-update-experience-plan.md).
//
// When a deploy lands under an open tab the reader is told, and the ONE thing that decides how hard
// the portal insists is this setting. It is a single enum rather than a pair of switches because
// "may the reader defer" and "must the page be refreshed" are one decision read two ways, and two
// booleans can spell a state nobody chose.
//
//	 dismissible  a banner the reader may hide for this target, for this tab session
//	 persistent   a banner with no way to hide it that still leaves the page usable
//	 required     the release-note dialog, opened automatically and impossible to dismiss
//
// Stored in `meta` like every other site setting — no table, no column, no schema change. An absent
// or unreadable value degrades to `dismissible`: a corrupt row must not start blocking every reader.
const (
	updatePromptPolicySetting = "update_prompt_policy"
	policyDismissible         = "dismissible"
	policyPersistent          = "persistent"
	policyRequired            = "required"
)

// validUpdatePromptPolicy reports whether raw is one of the three policies. Anything else is
// refused at the write, never coerced — silently storing the default would tell an admin their
// choice was kept when it was not.
func validUpdatePromptPolicy(raw string) bool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case policyDismissible, policyPersistent, policyRequired:
		return true
	default:
		return false
	}
}

// normalizeUpdatePromptPolicy maps a stored value onto a policy, defaulting anything unrecognized.
func normalizeUpdatePromptPolicy(raw string) string {
	v := strings.TrimSpace(strings.ToLower(raw))
	if validUpdatePromptPolicy(v) {
		return v
	}
	return policyDismissible
}

// updatePromptPolicy is the effective policy, read the same way by the settings payload and by the
// poll every open page makes.
func (s *Server) updatePromptPolicy() string {
	return normalizeUpdatePromptPolicy(s.st.GetSetting(updatePromptPolicySetting, ""))
}

// releaseNotesResp is the /api/release-notes body.
//
// The note is packaged into the binary at release time, so the server can only ever describe the
// build it IS. A page still running the previous build asks for its own version's note, which this
// binary has not got: it answers "unavailable" and hands over the exact release link, rather than
// showing the new build's note under the old build's version label.
//
// `note`/`hasNote` are parameters rather than read here so the rules are testable without a release
// build; the handler passes the packaged values.
func releaseNotesResp(reqTag, serverTag, note string, hasNote bool) map[string]any {
	tag := strings.TrimSpace(reqTag)
	if tag == "" {
		tag = serverTag
	}
	out := map[string]any{"tag": tag, "available": false, "markdown": "", "url": version.ReleaseURL(tag)}
	if tag == serverTag && hasNote && version.IsReleaseTag(tag) {
		out["available"] = true
		out["markdown"] = note
	}
	return out
}

// handleReleaseNotes serves the deployed build's committed release note. Session-gated for the same
// reason /api/version is: the note is not a secret, but the endpoint is infrastructure a reader
// reaches through the app, and leaving it open would expose a stable path to probe.
func (s *Server) handleReleaseNotes(w http.ResponseWriter, r *http.Request, user string) {
	note, ok := version.PackagedNote()
	writeJSON(w, releaseNotesResp(r.URL.Query().Get("tag"), version.Version, note, ok))
}
