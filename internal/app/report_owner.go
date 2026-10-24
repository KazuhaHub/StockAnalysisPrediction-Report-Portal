package app

import (
	"log"
	"sort"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// Report ownership (ADR 0022 R1). A report is stamped with the OU that generated it, and its
// prediction set is attributed to that OU (ADR 0039). The stamp is attribution and audit only; who
// may READ a report is decided by version grants and report_viewers (ADR 0024). Ownership is stamped
// server-side from a signed token (mint/verify live on Server), never from a client field.

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
// run. The instrumented workflow passes it straight through into its /api/v1/reports payload as
// owner_token. It is injected into every run that has a submitter, internal or external (P39b).
const ownerTokenInput = "_rp_owner_token"

// runInputs returns the Dify inputs to send for a job's item. Every run with a submitter carries a
// signed owner-attribution token (mintOwnerToken) under ownerTokenInput (ADR 0022 R1, amended):
// internal and external alike, so the produced report is stamped to the submitter's OU at ingest and
// every prediction set can be attributed to the OU its run belonged to (ADR 0039). A run with no
// submitter is unattributed and carries no token. The key is added whatever the provider: a manifest
// plugin target's backend receives it among the inputs too, and must accept it. It never forwards a
// caller-supplied reserved input, and it never mutates the caller's map.
func (s *Server) runInputs(job BatchJob, inputs map[string]string) map[string]string {
	// Reserved inputs are the portal's, never the caller's: a value submitted under _rp_* does not
	// survive into the run, whoever submits it (P39a).
	out := make(map[string]string, len(inputs)+1)
	for k, v := range inputs {
		if !isReservedInput(k) {
			out[k] = v
		}
	}
	if job.CreatedBy == "" {
		return out // no submitter: nobody to attribute the run to
	}
	// A refused mint (no signing key) leaves the run without a token: it still starts, and its
	// report is simply unattributed.
	if tok := s.mintOwnerToken(s.st.OwnerGroupOf(job.CreatedBy), job.CreatedBy); tok != "" {
		out[ownerTokenInput] = tok
	}
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
	// denyAll is set when the restriction itself could not be read (P31): the scope must match
	// nothing. It is a scope rather than nil because nil means "unrestricted".
	denyAll bool
	// principals identify this reader for the ownership test: always "u:<name>", plus the Default OU
	// (which encodes the "all" audience), plus "g:<id>" for each EFFECTIVELY RESTRICTED OU on their
	// chain. An unrestricted OU is not a principal (P30).
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
	if sc.denyAll {
		// Unreadable restriction: nothing, spelled so it cannot be mistaken for "no filter".
		return "1=0", nil
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
	restricted, err := s.isRestrictedStrict(user)
	if err != nil {
		// The restriction could not be read (or the chain is broken): deny everything for this
		// request. Reading it as "not restricted" is how D3 failed open.
		return &ownerScope{denyAll: true}
	}
	if !restricted {
		return nil
	}
	sc := &ownerScope{self: userPrincipal(user)}
	sc.principals = append(sc.principals, sc.self)
	// Group principals are the Default (the "all" audience is stored as its principal) and, on the
	// user's chain, only the effectively restricted OUs (D4, P30). An unrestricted container is
	// therefore not a principal, so a legacy g-row written for one never matches again. The Default
	// is the exception the encoding forces: its principal is also the "all" audience, and a legacy
	// ingest row for a Default-attributed run is byte-identical to it, so those rows remain readable
	// (recorded as a known limitation in the log).
	ix, err := s.st.loadOUIndex()
	if err != nil {
		return &ownerScope{denyAll: true}
	}
	if ix.defaultID != 0 {
		sc.principals = append(sc.principals, groupPrincipal(ix.defaultID))
	}
	gid := s.st.PrimaryGroupOf(user)
	if gid == 0 {
		gid = ix.defaultID
	}
	if gid != 0 {
		chain, cerr := ix.chain(gid)
		if cerr != nil {
			// A dangling or cyclic chain would drop a restricted ancestor: deny rather than share.
			return &ownerScope{denyAll: true}
		}
		for _, g := range chain {
			if g == ix.defaultID {
				continue
			}
			restricted, rerr := ix.restrictedEffective(g)
			if rerr != nil {
				return &ownerScope{denyAll: true}
			}
			if restricted {
				sc.principals = append(sc.principals, groupPrincipal(g))
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

// userCreatedLayout is how users.created_at is written (nowStr): local civil time, whole seconds.
const userCreatedLayout = "2006-01-02 15:04:05"

// recordRunRequester puts the person a run was submitted for on the viewer list of the report the
// run produced (ADR 0024), under the claims of the token the workflow echoed. Attribution of the
// report and of its prediction set is decided by the token alone and is not touched here; this is
// only the question of who may READ the report, and it errs toward less sharing in three ways.
//
//   - The token must still name the person it named. A run outlives its submitter's account (the
//     token lasts days and deleting an account cannot stop a workflow that is executing), and a
//     late ingest must not write the deleted name back onto a viewer list, where the next holder of
//     the username would inherit the report. An account created after the token was minted is a
//     different person, however it is spelt.
//   - The OU's group principal is offered only for a submitter whose own reads are scoped. An
//     unrestricted account, in practice an admin, can be placed in a restricted tenant's OU; the OU
//     alone would then share that account's reports with the whole tenant, which on a run that
//     carried no token never happened. Whether the OU may appear as a group principal at all is
//     AddReportViewer's rule (P30); this only withholds the offer.
//   - A token that names no person records nobody: there is no principal for no one.
func (s *Server) recordRunRequester(reportID int64, rdate string, c ownerClaims) error {
	if !s.tokenNamesCurrentAccount(c) {
		if c.User != "" {
			log.Printf("v1 ingest id=%d: no viewer row, %q is no longer the account the run was submitted by", reportID, c.User)
		}
		return nil
	}
	ou := c.OU
	if scoped, err := s.isRestrictedStrict(c.User); err != nil || !scoped {
		ou = 0
	}
	return s.st.AddReportViewer(reportID, rdate, c.User, ou)
}

// tokenNamesCurrentAccount reports whether the person a token names is still the same account: it
// exists, and it was not created after the token was minted. A row whose creation time was never
// stamped predates the stamp and is trusted as it always was; one that cannot be read is not.
func (s *Server) tokenNamesCurrentAccount(c ownerClaims) bool {
	if c.User == "" {
		return false
	}
	u := s.st.GetUser(c.User)
	if u == nil {
		return false
	}
	if u.CreatedAt == "" {
		return true
	}
	created, err := time.ParseInLocation(userCreatedLayout, u.CreatedAt, time.Local)
	return err == nil && !created.After(c.IssuedAt)
}
