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

func releaseNotesResp(reqTag, serverTag string, releases []publishedRelease, stale bool) map[string]any {
	tag := strings.TrimSpace(reqTag)
	if tag == "" {
		tag = serverTag
	}
	out := map[string]any{"tag": tag, "available": false, "markdown": "", "url": version.ReleaseURL(tag), "maturity": "", "stale": stale}
	if !version.IsReleaseTag(tag) {
		out["maturity"] = "dev"
		return out
	}
	entry, ok := findPublishedRelease(releases, tag)
	if ok {
		out["maturity"] = entry.Maturity
		if entry.Markdown != "" {
			out["available"] = true
			out["markdown"] = entry.Markdown
		}
	}
	return out
}

func releaseHistoryResp(serverTag string, releases []publishedRelease) []map[string]string {
	history := visibleReleaseHistory(releases, serverTag, 10)
	out := make([]map[string]string, 0, len(history))
	for _, entry := range history {
		out = append(out, map[string]string{"tag": entry.Tag, "title": entry.Title, "url": version.ReleaseURL(entry.Tag), "maturity": entry.Maturity})
	}
	return out
}

// handleReleaseNotes serves GitHub's current published Release body and maturity. Session-gated for
// the same reason /api/version is: the note is not a secret, but the endpoint is infrastructure a
// reader reaches through the app, and leaving it open would expose a stable path to probe.
func (s *Server) handleReleaseNotes(w http.ResponseWriter, r *http.Request, user string) {
	releases, stale, err := s.releases.list(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, "release catalog unavailable")
		return
	}
	writeJSON(w, releaseNotesResp(r.URL.Query().Get("tag"), version.Version, releases, stale))
}

func (s *Server) handleReleaseHistory(w http.ResponseWriter, r *http.Request, user string) {
	releases, stale, err := s.releases.list(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, "release catalog unavailable")
		return
	}
	writeJSON(w, map[string]any{"items": releaseHistoryResp(version.Version, releases), "stale": stale})
}
