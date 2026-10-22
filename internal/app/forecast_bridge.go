package app

import (
	"net/http"
	"time"
)

// lookup shares the app-token registry's identity binding, never a forecast-only identity map.
// Unbound tokens remain distinguishable from persistent machine credentials and fail closed.
func (a *appTokens) lookup(token string, now time.Time) (appTokenEntry, bool) {
	if a == nil {
		return appTokenEntry{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.m[token]
	return entry, ok && now.Before(entry.expires) && entry.scopes["query"] && entry.username != ""
}

func (s *Server) forecastBridgePredictionView(w http.ResponseWriter, r *http.Request, token string) (forecastPredictionView, bool) {
	deny := func() (forecastPredictionView, bool) {
		jsonErrorCode(w, http.StatusForbidden, "forecast_unavailable", "forecast access is unavailable")
		return forecastPredictionView{}, false
	}
	entry, ok := s.appTok.lookup(token, time.Now())
	if !ok {
		return deny()
	}
	// Identity freshness and the forecast decision use the same strict account/tree snapshot.
	// A token does not survive account disable/delete/recreation or a password/session revision.
	sub, ix, err := s.loadForecastSubject(entry.username)
	if err != nil || sub.SessionRev != entry.sessionRev || sub.CreatedAt != entry.accountCreated || s.accountExpired(&User{ExpiresAt: sub.ExpiresAt}) {
		return deny()
	}
	if !s.gateEnrolment(w, r, entry.username) {
		return forecastPredictionView{}, false
	}
	d := resolveForecastScope(sub, ix)
	if d.Scope == scopeNone {
		return deny()
	}
	view := forecastPredictionViewFor(d, s.viewerScope(entry.username))
	// Even an admin-minted bridge token gets only accepted, report-backed sets and never raw
	// diagnostics or membership. Its user binding gates access, not an elevation to machine shape.
	view.protected, view.reader, view.noMembership = true, true, true
	return view, true
}
