package app

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// requestAccount is the account's policy snapshot for one request: the account row and the organisation tree,
// read once in one read-only snapshot, from which the enrolment gate, the viewer scope and the forecast access of
// that request are all decided. Before it, a page load decided each of them from a snapshot of its own (the stock
// page five times, four statements each on PostgreSQL), which on a busy database is most of the page's latency.
//
// It is attached only to safe requests (GET and HEAD), which change no policy, so no request can read a snapshot
// older than its own writes, and it never outlives the request. It answers only for the account it was made for.
// One snapshot per request is also the more consistent rule: the gate and the handler can no longer decide from
// two different states of the tree.
type requestAccount struct {
	user string

	once sync.Once
	sub  ouSubject
	snap ouPolicySnapshot
	err  error

	scopeOnce sync.Once
	scope     *ownerScope
}

type requestAccountKey struct{}

// withRequestAccount gives a safe request an empty account snapshot for user, loaded on first use.
func withRequestAccount(r *http.Request, user string) *http.Request {
	if user == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), requestAccountKey{}, &requestAccount{user: user}))
}

// requestAccountOf is the request's snapshot for user, or nil when there is none for that account.
func requestAccountOf(ctx context.Context, user string) *requestAccount {
	if ctx == nil || user == "" {
		return nil
	}
	if ra, ok := ctx.Value(requestAccountKey{}).(*requestAccount); ok && ra.user == user {
		return ra
	}
	return nil
}

// accountPolicy is the account row and the organisation tree for user: the request's snapshot when it has one,
// otherwise a snapshot of its own.
func (s *Server) accountPolicy(ctx context.Context, user string) (ouSubject, ouPolicySnapshot, error) {
	if ra := requestAccountOf(ctx, user); ra != nil {
		ra.once.Do(func() { ra.sub, ra.snap, ra.err = s.st.loadOUPolicySubject(user) })
		return ra.sub, ra.snap, ra.err
	}
	return s.st.loadOUPolicySubject(user)
}

// securitySettingsFor is Store.SecuritySettings decided from the request's snapshot.
func (s *Server) securitySettingsFor(ctx context.Context, user string) (SecuritySettings, error) {
	sub, snap, err := s.accountPolicy(ctx, user)
	if err != nil {
		return SecuritySettings{}, err
	}
	return securitySettingsOf(sub, snap)
}

// viewerScopeFor is viewerScope decided once per request. An account the tree alone settles (an administrator,
// or one that no restriction reaches) is decided from the request's snapshot with no further statement. A
// restricted account's scope also needs its granted versions, so it is read as before, in a snapshot of its
// own that includes the tree, and remembered for the rest of the request.
func (s *Server) viewerScopeFor(ctx context.Context, user string) *ownerScope {
	ra := requestAccountOf(ctx, user)
	if ra == nil {
		return s.viewerScope(user)
	}
	ra.scopeOnce.Do(func() {
		sub, snap, err := s.accountPolicy(ctx, user)
		if err != nil {
			ra.scope = &ownerScope{denyAll: true}
			return
		}
		settled, err := unrestrictedViewer(sub, snap)
		switch {
		case err != nil:
			ra.scope = &ownerScope{denyAll: true}
		case settled:
			ra.scope = nil
		default:
			ra.scope = s.viewerScope(user)
		}
	})
	return ra.scope
}

// forecastAccessFor is forecastAccess decided from the request's snapshot.
func (s *Server) forecastAccessFor(ctx context.Context, user string) forecastDecision {
	if user == "" {
		return forecastDecision{Scope: scopeNone, Reason: "anonymous"}
	}
	if requestAccountOf(ctx, user) == nil {
		return s.forecastAccess(user)
	}
	sub, snap, err := s.accountPolicy(ctx, user)
	if err != nil {
		return forecastDecision{Scope: scopeNone, Reason: "error"}
	}
	fs, err := forecastSubjectOf(user, sub)
	if err != nil {
		return forecastDecision{Scope: scopeNone, Reason: "error"}
	}
	return resolveForecastScope(fs, ouIndexOf(snap))
}

// forecastSubjectOf is the forecast subject of user from its account row, refusing an inactive account as the
// forecast loader does.
func forecastSubjectOf(user string, sub ouSubject) (forecastSubject, error) {
	if !sub.Active {
		return forecastSubject{}, fmt.Errorf("forecast subject is inactive")
	}
	return forecastSubject{User: user, Admin: can(sub.Role, PermManage), RestrictedAccount: sub.Restricted, GroupID: sub.AssignedOU}, nil
}
