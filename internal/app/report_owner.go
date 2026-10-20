package app

import (
	"sort"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// Report ownership (ADR 0022 R1). A report is stamped with the OU that generated it so a restricted
// external viewer sees only its own OU's reports (plus the same-day internal pool). Ownership is
// stamped server-side from a signed token (mint/verify live on Server), never from a client field.

// OwnerGroupOf resolves the OU that owns a user's output: their primary group, or the Default group
// when unassigned. Returns 0 only if there is no Default group (which EnsureDefaultGroup prevents).
func (s *Store) OwnerGroupOf(username string) int64 {
	if gid := s.PrimaryGroupOf(username); gid != 0 {
		return gid
	}
	return s.DefaultGroupID()
}

// StampReportOwner sets a report's owning OU first-writer-wins: it writes only while owner_group is
// still NULL, so a re-ingest of the shared identity row — or a second OU racing the same request —
// never reassigns an already-attributed report. Reports whether this call did the stamping. ou 0
// (no resolvable OU) is a no-op.
func (s *Store) StampReportOwner(id, ou int64) (bool, error) {
	if ou == 0 {
		return false, nil
	}
	res, err := s.exec(`UPDATE reports SET owner_group=? WHERE id=? AND owner_group IS NULL`, ou, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FindSameDayReport looks for a report already generated today for a (symbol, subtype, version)
// request. It powers "if it was already generated today, show it instead of running".
//
// Deliberately NOT scoped to the caller (ADR 0024). This is a content lookup — does this analysis
// exist today — and the caller's entitlement is checked by reuseSameDayReport before it asks. Making
// the lookup itself viewer-scoped is what made reuse inert under per-person visibility: the person
// who has not asked before is on no viewer list, so nothing was ever found for exactly the caller
// reuse exists to serve.
//
// The newest match wins (D4) — two same-day reports for one (symbol, subtype, version) differ only
// by title, which is generator output the requester cannot predict.
func (s *Store) FindSameDayReport(symbol, subtype, version, panelToday string) (int64, bool) {
	q := `SELECT id FROM reports WHERE symbol=? AND rtype=? AND rdate=? AND version=?`
	args := []any{symbol, subtype, panelToday, version}
	var id int64
	if err := s.queryRow(q, args...).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// ownerTokenInput is the reserved Dify input key that carries the owner-attribution token into a
// restricted OU's run. The instrumented workflow passes it straight through into its
// /api/v1/reports payload as owner_token. Injected ONLY for restricted OUs, so internal runs and
// their (possibly undeclared-variable-strict) workflows never see it.
const ownerTokenInput = "_rp_owner_token"

// runInputs returns the Dify inputs to send for a job's item. For a restricted OU's run it adds the
// signed owner-attribution token (mintOwnerToken) under ownerTokenInput, so the produced report is
// stamped to that OU at ingest (ADR 0022 R1). For every internal run it returns the caller's inputs
// unchanged — the money-path stays byte-for-byte identical — and it never mutates the caller's map.
func (s *Server) runInputs(job BatchJob, inputs map[string]string) map[string]string {
	// Reserved inputs are the portal's, never the caller's: a value submitted under _rp_* does not
	// survive into the run, whoever submits it (P39a).
	out := make(map[string]string, len(inputs)+1)
	for k, v := range inputs {
		if !isReservedInput(k) {
			out[k] = v
		}
	}
	if !s.isRestricted(job.CreatedBy) {
		return out
	}
	tok := s.mintOwnerToken(s.st.OwnerGroupOf(job.CreatedBy), job.CreatedBy)
	if tok == "" {
		return out
	}
	out[ownerTokenInput] = tok
	return out
}

// isReservedInput reports whether a Dify input name belongs to the portal. Reserved inputs are
// filled by the portal at run time (runInputs) and are never part of a target's user-facing list.
func isReservedInput(name string) bool { return strings.HasPrefix(name, "_rp_") }

// dropReservedInputs removes every reserved input from a workflow's declared list, keeping order.
func dropReservedInputs(in []dify.Input) []dify.Input {
	out := make([]dify.Input, 0, len(in))
	for _, i := range in {
		if !isReservedInput(i.Variable) {
			out = append(out, i)
		}
	}
	return out
}

// cleanReservedInputNames keeps only genuine `_rp_*` names, sorted: a client cannot record an
// ordinary input as "reserved" and change what a later release injects.
func cleanReservedInputNames(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, name := range in {
		if isReservedInput(name) && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// reservedInputNames returns the sorted names of the reserved inputs a workflow declares. The probe
// and the refresh record them so P39b can inject only where the workflow has the key.
func reservedInputNames(in []dify.Input) []string {
	var names []string
	for _, i := range in {
		if isReservedInput(i.Variable) {
			names = append(names, i.Variable)
		}
	}
	return cleanReservedInputNames(names)
}

// ownerScope restricts which reports a scoped (external) viewer may READ (ADR 0024, superseding the
// ADR 0022 R1 predicate). A nil *ownerScope means no restriction — internal users, admins, and
// machine/Bearer callers — so every scoped query appends nothing and stays byte-for-byte identical.
//
// The rule is two conditions, and nothing else. The report's version must be GRANTED to this
// reader, and the version's visibility must admit them: they asked for it (owner), someone in their
// OU did (group), or it does not matter (all). What used to decide this is gone — there is no
// same-day internal pool, and no narrowing by which subtypes you may run. Read permission no longer
// derives from run permission, which is what makes a read-only client possible at all.
//
// report_viewers is the ONLY table consulted for ownership. owner_group survives on the report row
// for attribution and audit, but the security-critical filter must not have two spellings.
type ownerScope struct {
	// principals identify this reader for the ownership test: always "u:<name>", plus "g:<id>" for
	// each OU up their chain when a version shares within the OU.
	self       string
	principals []string
	// The granted versions, split by what their visibility demands, so the predicate is a plain OR
	// of at most three IN-lists rather than a per-row lookup of the registry.
	versAll, versGroup, versOwner []string
}

// where returns an AND-joinable boolean fragment (no WHERE keyword) that scopes reports to the
// viewer, plus its args. prefix qualifies the columns ("r." for aliased queries, "" for a bare
// table). A nil scope returns ("", nil) so callers append nothing and behave exactly as before.
func (sc *ownerScope) where(prefix string) (string, []any) {
	if sc == nil {
		return "", nil
	}
	var ors []string
	var args []any
	in := func(vals []string) (string, []any) {
		ph := make([]string, len(vals))
		out := make([]any, len(vals))
		for i, v := range vals {
			ph[i], out[i] = "?", v
		}
		return prefix + "version IN (" + strings.Join(ph, ",") + ")", out
	}
	// A version whose reports are open to everyone granted it needs no ownership test at all.
	if len(sc.versAll) > 0 {
		frag, a := in(sc.versAll)
		ors = append(ors, frag)
		args = append(args, a...)
	}
	// The other two differ only in WHICH principals count, so they share one shape.
	owned := func(vals []string, principals []string) {
		if len(vals) == 0 || len(principals) == 0 {
			return
		}
		frag, a := in(vals)
		ph := make([]string, len(principals))
		for i, p := range principals {
			ph[i] = "?"
			a = append(a, p)
		}
		// `id IN (uncorrelated subquery)` rather than a correlated EXISTS, and the difference is the
		// whole query plan. EXISTS makes the ownership test a per-row probe, so the planner SCANS
		// every report and asks about each one; the subquery has no outer reference, so it is
		// evaluated once and the outer query becomes a primary-key SEARCH driven from it. Measured at
		// 200k reports: 18.4ms to 11.7ms, and the gap widens with the report count because the scan
		// is what disappears. Identical meaning — report_id is never null, so there is no three-valued
		// IN subtlety to worry about.
		ors = append(ors, "("+frag+" AND "+prefix+"id IN (SELECT report_id FROM report_viewers"+
			" WHERE principal IN ("+strings.Join(ph, ",")+")))")
		args = append(args, a...)
	}
	owned(sc.versGroup, sc.principals)
	owned(sc.versOwner, []string{sc.self})
	if len(ors) == 0 {
		// Granted nothing: default-deny, spelled so it cannot be mistaken for "no filter".
		return "1=0", nil
	}
	return "(" + strings.Join(ors, " OR ") + ")", args
}

// viewerScope resolves the read scope for a browser (cookie-session) user. It returns nil — no
// restriction — for anonymous callers, admins, and any user who is not scoped, so internal behaviour
// is unchanged. A scoped user carries their granted versions, bucketed by visibility.
func (s *Server) viewerScope(user string) *ownerScope {
	if !s.isRestricted(user) {
		return nil
	}
	sc := &ownerScope{self: userPrincipal(user)}
	sc.principals = append(sc.principals, sc.self)
	// Group principals are the Default (the "all" audience is stored as its principal) and, on the
	// user's chain, only the effectively restricted OUs (D4, P30). An unrestricted OU — a container,
	// or the Default beyond its "all" meaning — is what a reader would otherwise share through, and a
	// g-row already written for one of them simply never matches again. An index or chain failure
	// yields no group principals: less sharing, never more (P31 tightens this to a scope of nothing).
	if ix, err := s.st.loadOUIndex(); err == nil {
		if ix.defaultID != 0 {
			sc.principals = append(sc.principals, groupPrincipal(ix.defaultID))
		}
		if gid := s.st.PrimaryGroupOf(user); gid != 0 {
			if chain, cerr := ix.chain(gid); cerr == nil {
				for _, g := range chain {
					if g == ix.defaultID {
						continue
					}
					if restricted, rerr := ix.restrictedEffective(g); rerr == nil && restricted {
						sc.principals = append(sc.principals, groupPrincipal(g))
					}
				}
			}
		}
	}
	for _, name := range s.st.GrantedVersions(user) {
		v, ok := s.st.Version(name)
		if !ok {
			continue // a version nobody registered grants nothing
		}
		switch v.Visibility {
		case VisibilityAll:
			sc.versAll = append(sc.versAll, name)
		case VisibilityGroup:
			sc.versGroup = append(sc.versGroup, name)
		default:
			sc.versOwner = append(sc.versOwner, name)
		}
	}
	return sc
}
