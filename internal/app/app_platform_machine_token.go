package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Machine tokens (doc 183 section 4.3): the rows of app_platform_machine_tokens and the operations an
// administrator performs on them. A token is stored as its handle and the SHA-256 of its secret part;
// the secret is returned once, by the call that created it, and is in no row, log or audit record.

const (
	platformMachineTokenCreatedAudit = "app_platform.machine_token_created"
	platformMachineTokenRotatedAudit = "app_platform.machine_token_rotated"
	platformMachineTokenRevokedAudit = "app_platform.machine_token_revoked"
	platformMachineTokenExpiredAudit = "app_platform.machine_token_expired"
	platformMachineTokenSourceAudit  = "app_platform.machine_token_source"
	platformMachineDay               = 24 * time.Hour
)

var (
	errPlatformMachineGrantOff   = errors.New("machine read is not enabled for this installation")
	errPlatformMachineTokenLimit = errors.New("too many active machine tokens")
	errPlatformMachineTokenGone  = errors.New("machine token is not active")
)

type platformMachineToken struct {
	ID, Installation, Handle, Label, SecretSHA256 string
	Routes, CIDRs                                 []string
	PerMinute, PolicyRevision, Revision           int64
	State, CreatedBy                              string
	ExpiresMS, CreatedMS, UpdatedMS               int64
	RevokedMS, LastUsedMS                         sql.NullInt64
}

const platformMachineTokenColumns = `id,installation_id,handle,label,secret_sha256,routes_json,per_minute,source_cidrs_json,state,policy_revision,created_by,revision,expires_ms,created_ms,updated_ms,revoked_ms,last_used_ms`

type platformMachineRowScanner interface{ Scan(dest ...any) error }

func scanPlatformMachineToken(row platformMachineRowScanner) (platformMachineToken, error) {
	var t platformMachineToken
	var routes, cidrs string
	if err := row.Scan(&t.ID, &t.Installation, &t.Handle, &t.Label, &t.SecretSHA256, &routes, &t.PerMinute, &cidrs, &t.State, &t.PolicyRevision, &t.CreatedBy, &t.Revision, &t.ExpiresMS, &t.CreatedMS, &t.UpdatedMS, &t.RevokedMS, &t.LastUsedMS); err != nil {
		return t, err
	}
	if json.Unmarshal([]byte(routes), &t.Routes) != nil || json.Unmarshal([]byte(cidrs), &t.CIDRs) != nil || t.Routes == nil {
		return t, errPlatformMachineTokenGone
	}
	if t.CIDRs == nil {
		t.CIDRs = []string{}
	}
	return t, nil
}

// lookupPlatformMachineToken is the one indexed read of a request: by handle.
func (s *Server) lookupPlatformMachineToken(ctx context.Context, handle string) (platformMachineToken, error) {
	return scanPlatformMachineToken(s.st.queryRowContext(ctx, `SELECT `+platformMachineTokenColumns+` FROM app_platform_machine_tokens WHERE handle=?`, handle))
}

// platformMachineDummyDigest stands in for the stored digest of an unknown handle, so a miss costs the
// same comparison a hit does.
var platformMachineDummyDigest = hex.EncodeToString(make([]byte, sha256.Size))

// valid reports whether the presented secret digest and the row admit a call for installation now from
// source (empty source matches only a token without a source list). The comparison is constant time and
// runs on every call, found or not.
func (t platformMachineToken) admits(digest, installation string, now time.Time, source string) (string, bool) {
	stored := t.SecretSHA256
	if t.ID == "" {
		stored = platformMachineDummyDigest
	}
	match := subtle.ConstantTimeCompare([]byte(digest), []byte(stored)) == 1
	switch {
	case !match || t.ID == "":
		return "unauthorized", false
	case t.State != "active":
		return "revoked", false
	case t.ExpiresMS <= now.UnixMilli():
		return "expired", false
	case t.Installation != installation:
		return "installation", false
	case !t.sourceAllowed(source):
		return "source", false
	}
	return "", true
}

func (t platformMachineToken) sourceAllowed(source string) bool {
	if len(t.CIDRs) == 0 {
		return true
	}
	addr, err := netip.ParseAddr(source)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, c := range t.CIDRs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}

// effectiveRoutes is the intersection of the token's list and the reviewed policy, so a route dropped
// from the policy stops working at once without rewriting the token.
func (t platformMachineToken) effectiveRoutes(policy appplatform.MachinePolicy) []string {
	var out []string
	for _, id := range t.Routes {
		if slices.Contains(policy.Routes, id) {
			out = append(out, id)
		}
	}
	return out
}

// platformMachineTokenInput is the administrator's choice for one token.
type platformMachineTokenInput struct {
	Label        string
	Routes       []string
	PerMinute    int64
	SourceCIDRs  []string
	ExpiresInDay int
}

// canonicalPlatformMachineCIDRs accepts up to eight prefixes or single addresses and returns their
// masked canonical text, sorted and without duplicates.
func canonicalPlatformMachineCIDRs(in []string) ([]string, error) {
	if len(in) > appplatform.MaxMachineTokenCIDRs {
		return nil, errPlatformManagementInput
	}
	out := []string{}
	for _, text := range in {
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			addr, aerr := netip.ParseAddr(text)
			if aerr != nil || addr.Zone() != "" {
				return nil, errPlatformManagementInput
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		prefix = prefix.Masked()
		if !prefix.IsValid() || prefix.Addr().Is4In6() || prefix.Addr().Zone() != "" {
			return nil, errPlatformManagementInput
		}
		if s := prefix.String(); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, nil
}

// checkPlatformMachineTokenInput validates a choice against the reviewed policy and returns the
// canonical routes and CIDR lists.
func checkPlatformMachineTokenInput(in platformMachineTokenInput, policy appplatform.MachinePolicy) (routes, cidrs []string, err error) {
	if !appplatform.ValidMachineLabel(in.Label) || in.PerMinute < 1 || in.PerMinute > appplatform.MaxMachinePerMinute ||
		in.ExpiresInDay < 1 || in.ExpiresInDay > appplatform.MaxMachineExpiryDays || len(in.Routes) == 0 || len(in.Routes) > len(policy.Routes) {
		return nil, nil, errPlatformManagementInput
	}
	routes = slices.Clone(in.Routes)
	slices.Sort(routes)
	for i, id := range routes {
		if (i > 0 && routes[i-1] == id) || !slices.Contains(policy.Routes, id) {
			return nil, nil, errPlatformManagementInput
		}
	}
	if cidrs, err = canonicalPlatformMachineCIDRs(in.SourceCIDRs); err != nil {
		return nil, nil, err
	}
	return routes, cidrs, nil
}

func platformMachineAudit(e migExec, actor platformActorFacts, nowMS int64, action, installation string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`,
		time.UnixMilli(nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, action, installation, string(raw))
	return err
}

// readPlatformMachinePolicy returns the policy of the installation's granted machine.read row.
func readPlatformMachinePolicy(e migExec, installation string, policyRevision int64, lock string) (appplatform.MachinePolicy, error) {
	var granted int
	var stored string
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='machine.read' AND policy_revision=?`+lock, installation, policyRevision).Scan(&granted, &stored); err != nil || granted != 1 {
		return appplatform.MachinePolicy{}, errPlatformMachineGrantOff
	}
	var policy appplatform.MachinePolicy
	if !appplatform.ValidMachinePolicyShape(stored) || json.Unmarshal([]byte(stored), &policy) != nil {
		return policy, errPlatformMachineGrantOff
	}
	return policy, nil
}

type platformMachineCreated struct {
	Token string
	Row   platformMachineToken
}

func insertPlatformMachineToken(e migExec, actor platformActorFacts, nowMS int64, installation string, policyRevision int64, label string, routes, cidrs []string, perMinute int64, expiresMS int64) (platformMachineCreated, error) {
	token, handle, digest, err := appplatform.NewMachineToken()
	if err != nil {
		return platformMachineCreated{}, err
	}
	id, err := platformNewUUID()
	if err != nil {
		return platformMachineCreated{}, err
	}
	routesJSON, _ := json.Marshal(routes)
	cidrsJSON, _ := json.Marshal(cidrs)
	if _, err := e.exec(`INSERT INTO app_platform_machine_tokens(`+platformMachineTokenColumns+`) VALUES(?,?,?,?,?,?,?,?,'active',?,?,1,?,?,?,NULL,NULL)`,
		id, installation, handle, label, digest, string(routesJSON), perMinute, string(cidrsJSON), policyRevision, actor.username, expiresMS, nowMS, nowMS); err != nil {
		return platformMachineCreated{}, err
	}
	row, err := scanPlatformMachineToken(e.queryRow(`SELECT `+platformMachineTokenColumns+` FROM app_platform_machine_tokens WHERE id=?`, id))
	return platformMachineCreated{Token: token, Row: row}, err
}

func countPlatformActiveMachineTokens(e migExec, installation string, nowMS int64) (int, error) {
	var n int
	err := e.queryRow(`SELECT COUNT(*) FROM app_platform_machine_tokens WHERE installation_id=? AND state='active' AND expires_ms>?`, installation, nowMS).Scan(&n)
	return n, err
}

func lockPlatformMachineInstallation(e migExec, installation string) (policyRevision int64, err error) {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	if err := e.queryRow(`SELECT policy_revision FROM app_platform_installations WHERE id=?`+lock, installation).Scan(&policyRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errPlatformInstallationMissing
		}
		return 0, err
	}
	return policyRevision, nil
}

// createPlatformMachineToken runs in the administrator's transaction.
func createPlatformMachineToken(e migExec, actor platformActorFacts, control platformActorControl, installation string, in platformMachineTokenInput) (platformMachineCreated, error) {
	revision, err := lockPlatformMachineInstallation(e, installation)
	if err != nil {
		return platformMachineCreated{}, err
	}
	policy, err := readPlatformMachinePolicy(e, installation, revision, "")
	if err != nil {
		return platformMachineCreated{}, err
	}
	routes, cidrs, err := checkPlatformMachineTokenInput(in, policy)
	if err != nil {
		return platformMachineCreated{}, err
	}
	active, err := countPlatformActiveMachineTokens(e, installation, control.nowMS)
	if err != nil {
		return platformMachineCreated{}, err
	}
	if active >= appplatform.MaxMachineActiveTokens {
		return platformMachineCreated{}, errPlatformMachineTokenLimit
	}
	expires := control.nowMS + int64(in.ExpiresInDay)*platformMachineDay.Milliseconds()
	created, err := insertPlatformMachineToken(e, actor, control.nowMS, installation, revision, in.Label, routes, cidrs, in.PerMinute, expires)
	if err != nil {
		return created, err
	}
	err = platformMachineAudit(e, actor, control.nowMS, platformMachineTokenCreatedAudit, installation, map[string]any{
		"handle": created.Row.Handle, "label": in.Label, "routes": routes, "per_minute": in.PerMinute, "source_cidrs": len(cidrs), "expires_ms": expires})
	return created, err
}

// rotatePlatformMachineToken creates a successor with the same binding and shortens the predecessor
// to at most graceDays more (doc 183 section 4.3). The successor's own expiry is the administrator's
// choice, as for a new token.
func rotatePlatformMachineToken(e migExec, actor platformActorFacts, control platformActorControl, installation, handle string, graceDays, expiresInDays int) (platformMachineCreated, error) {
	if graceDays < 0 || graceDays > appplatform.MaxMachineRotationGraceDays || expiresInDays < 1 || expiresInDays > appplatform.MaxMachineExpiryDays || !appplatform.ValidMachineHandle(handle) {
		return platformMachineCreated{}, errPlatformManagementInput
	}
	revision, err := lockPlatformMachineInstallation(e, installation)
	if err != nil {
		return platformMachineCreated{}, err
	}
	old, err := scanPlatformMachineToken(e.queryRow(`SELECT `+platformMachineTokenColumns+` FROM app_platform_machine_tokens WHERE handle=? AND installation_id=?`, handle, installation))
	if err != nil || old.State != "active" || old.ExpiresMS <= control.nowMS {
		return platformMachineCreated{}, errPlatformMachineTokenGone
	}
	policy, err := readPlatformMachinePolicy(e, installation, revision, "")
	if err != nil {
		return platformMachineCreated{}, err
	}
	routes := old.effectiveRoutes(policy)
	if len(routes) == 0 {
		return platformMachineCreated{}, errPlatformManagementInput
	}
	newExpiry := min(old.ExpiresMS, max(control.nowMS+int64(graceDays)*platformMachineDay.Milliseconds(), old.CreatedMS+1))
	changed, err := e.exec(`UPDATE app_platform_machine_tokens SET expires_ms=?,revision=revision+1,updated_ms=? WHERE id=? AND revision=? AND state='active'`, newExpiry, max(control.nowMS, old.CreatedMS), old.ID, old.Revision)
	if err := platformLeaseCAS(changed, err); err != nil {
		return platformMachineCreated{}, errAppPlatformPlanChanged
	}
	active, err := countPlatformActiveMachineTokens(e, installation, control.nowMS)
	if err != nil {
		return platformMachineCreated{}, err
	}
	if active >= appplatform.MaxMachineActiveTokens+1 {
		return platformMachineCreated{}, errPlatformMachineTokenLimit
	}
	expires := control.nowMS + int64(expiresInDays)*platformMachineDay.Milliseconds()
	created, err := insertPlatformMachineToken(e, actor, control.nowMS, installation, revision, old.Label, routes, old.CIDRs, old.PerMinute, expires)
	if err != nil {
		return created, err
	}
	err = platformMachineAudit(e, actor, control.nowMS, platformMachineTokenRotatedAudit, installation, map[string]any{
		"handle": old.Handle, "successor": created.Row.Handle, "label": old.Label, "grace_days": graceDays, "predecessor_expires_ms": newExpiry, "expires_ms": expires})
	return created, err
}

// revokePlatformMachineToken is immediate. Revoking a revoked token is not an error.
func revokePlatformMachineToken(e migExec, actor platformActorFacts, control platformActorControl, installation, handle string) error {
	if !appplatform.ValidMachineHandle(handle) {
		return errPlatformManagementInput
	}
	if _, err := lockPlatformMachineInstallation(e, installation); err != nil {
		return err
	}
	row, err := scanPlatformMachineToken(e.queryRow(`SELECT `+platformMachineTokenColumns+` FROM app_platform_machine_tokens WHERE handle=? AND installation_id=?`, handle, installation))
	if err != nil {
		return errPlatformMachineTokenGone
	}
	if row.State == "revoked" {
		return nil
	}
	changed, err := e.exec(`UPDATE app_platform_machine_tokens SET state='revoked',revision=revision+1,revoked_ms=?,updated_ms=? WHERE id=? AND revision=? AND state='active'`, max(control.nowMS, row.CreatedMS), max(control.nowMS, row.CreatedMS), row.ID, row.Revision)
	if err := platformLeaseCAS(changed, err); err != nil {
		return errAppPlatformPlanChanged
	}
	return platformMachineAudit(e, actor, control.nowMS, platformMachineTokenRevokedAudit, installation, map[string]any{"handle": row.Handle, "label": row.Label, "reason": "administrator"})
}

// revokePlatformMachineTokens revokes every active token of an installation in the caller's
// transaction: the capability was switched off, or the installation is being uninstalled.
func revokePlatformMachineTokens(e migExec, actor platformActorFacts, nowMS int64, installation, reason string) (int, error) {
	rows, err := e.query(`SELECT `+platformMachineTokenColumns+` FROM app_platform_machine_tokens WHERE installation_id=? AND state='active' ORDER BY id`, installation)
	if err != nil {
		return 0, err
	}
	var live []platformMachineToken
	for rows.Next() {
		t, err := scanPlatformMachineToken(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		live = append(live, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, t := range live {
		at := max(nowMS, t.CreatedMS)
		changed, err := e.exec(`UPDATE app_platform_machine_tokens SET state='revoked',revision=revision+1,revoked_ms=?,updated_ms=? WHERE id=? AND revision=? AND state='active'`, at, at, t.ID, t.Revision)
		if err := platformLeaseCAS(changed, err); err != nil {
			return 0, errAppPlatformPlanChanged
		}
		if err := platformMachineAudit(e, actor, nowMS, platformMachineTokenRevokedAudit, installation, map[string]any{"handle": t.Handle, "label": t.Label, "reason": reason}); err != nil {
			return 0, err
		}
	}
	return len(live), nil
}

// platformMachineTokenView is what an administrator sees of a token: never the secret or its digest.
type platformMachineTokenView struct {
	Handle         string   `json:"handle"`
	Label          string   `json:"label"`
	Routes         []string `json:"routes"`
	PerMinute      int64    `json:"perMinute"`
	SourceCIDRs    []string `json:"sourceCidrs"`
	State          string   `json:"state"`
	CreatedBy      string   `json:"createdBy"`
	PolicyRevision string   `json:"policyRevision"`
	CreatedAt      string   `json:"createdAt"`
	ExpiresAt      string   `json:"expiresAt"`
	LastUsedAt     *string  `json:"lastUsedAt"`
	RevokedAt      *string  `json:"revokedAt"`
}

func platformMachineTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

func (t platformMachineToken) view(nowMS int64) platformMachineTokenView {
	state := t.State
	if state == "active" && t.ExpiresMS <= nowMS {
		state = "expired"
	}
	v := platformMachineTokenView{Handle: t.Handle, Label: t.Label, Routes: t.Routes, PerMinute: t.PerMinute, SourceCIDRs: t.CIDRs, State: state, CreatedBy: t.CreatedBy,
		PolicyRevision: strconv.FormatInt(t.PolicyRevision, 10), CreatedAt: platformMachineTime(t.CreatedMS), ExpiresAt: platformMachineTime(t.ExpiresMS)}
	if t.LastUsedMS.Valid {
		s := platformMachineTime(t.LastUsedMS.Int64)
		v.LastUsedAt = &s
	}
	if t.RevokedMS.Valid {
		s := platformMachineTime(t.RevokedMS.Int64)
		v.RevokedAt = &s
	}
	return v
}

func listPlatformMachineTokens(e migExec, installation string) ([]platformMachineToken, error) {
	rows, err := e.query(`SELECT `+platformMachineTokenColumns+` FROM app_platform_machine_tokens WHERE installation_id=? ORDER BY created_ms DESC,id LIMIT 200`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []platformMachineToken
	for rows.Next() {
		t, err := scanPlatformMachineToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
