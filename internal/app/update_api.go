package app

import (
	"net/http"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

// Update prompt policy.
//
// When a deploy lands under an open tab the reader is told, and the ONE thing that decides how hard
// the portal insists is this setting. It is a single enum rather than a pair of switches because
// "may the reader defer" and "must the page be refreshed" are one decision read two ways, and two
// booleans can spell a state nobody chose.
//
//	dismissible  a banner the reader may hide for this target, for this tab session
//	persistent   a banner with no way to hide it that still leaves the page usable
//	required     a closable release-note dialog, followed by a persistent banner
//	automatic    refresh immediately, confirm success after loading, and block if handover fails
//
// Stored in `meta` like every other site setting — no table, no column, no schema change. An absent
// or unreadable value degrades to `dismissible`: a corrupt row must not start blocking every reader.
const (
	updatePromptPolicySetting = "update_prompt_policy"
	policyDismissible         = "dismissible"
	policyPersistent          = "persistent"
	policyRequired            = "required"
	policyAutomatic           = "automatic"
)

// validUpdatePromptPolicy reports whether raw is one of the four policies. Anything else is
// refused at the write, never coerced — silently storing the default would tell an admin their
// choice was kept when it was not.
func validUpdatePromptPolicy(raw string) bool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case policyDismissible, policyPersistent, policyRequired, policyAutomatic:
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
// The current note and committed CalVer history are packaged into the binary at release time, so
// browsing old versions does not depend on GitHub availability or browser credentials.
//
// `note`/`hasNote` are parameters rather than read here so the rules are testable without a release
// build; the handler passes the packaged values.
func releaseNotesResp(reqTag, serverTag, note string, hasNote bool, history []version.ReleaseNote) map[string]any {
	tag := strings.TrimSpace(reqTag)
	if tag == "" {
		tag = serverTag
	}
	maturity := ""
	for _, entry := range history {
		if entry.Tag == tag {
			maturity = entry.Maturity
			break
		}
	}
	out := map[string]any{"tag": tag, "available": false, "markdown": "", "url": version.ReleaseURL(tag), "maturity": maturity}
	if tag == serverTag && hasNote && version.IsReleaseTag(tag) {
		out["available"] = true
		out["markdown"] = note
		return out
	}
	for _, entry := range history {
		if entry.Tag == tag {
			out["available"] = true
			out["markdown"] = entry.Markdown
			break
		}
	}
	return out
}

func releaseHistoryResp(serverTag string, hasNote bool, history []version.ReleaseNote) []map[string]string {
	out := make([]map[string]string, 0, len(history)+1)
	seen := make(map[string]bool, len(history)+1)
	for _, entry := range history {
		if seen[entry.Tag] {
			continue
		}
		seen[entry.Tag] = true
		out = append(out, map[string]string{"tag": entry.Tag, "title": entry.Title, "url": version.ReleaseURL(entry.Tag), "maturity": entry.Maturity})
	}
	if hasNote && version.IsReleaseTag(serverTag) && !seen[serverTag] {
		out = append([]map[string]string{{"tag": serverTag, "title": "", "url": version.ReleaseURL(serverTag), "maturity": ""}}, out...)
	}
	return out
}

// handleReleaseNotes serves the deployed build's committed release note. Session-gated for the same
// reason /api/version is: the note is not a secret, but the endpoint is infrastructure a reader
// reaches through the app, and leaving it open would expose a stable path to probe.
func (s *Server) handleReleaseNotes(w http.ResponseWriter, r *http.Request, user string) {
	note, ok := version.PackagedNote()
	writeJSON(w, releaseNotesResp(r.URL.Query().Get("tag"), version.Version, note, ok, version.PackagedHistory()))
}

func (s *Server) handleReleaseHistory(w http.ResponseWriter, r *http.Request, user string) {
	_, ok := version.PackagedNote()
	writeJSON(w, map[string]any{"items": releaseHistoryResp(version.Version, ok, version.PackagedHistory())})
}
