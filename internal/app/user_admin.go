package app

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User-admin persistence: extended profile attributes (display name / email / active /
// last login) and the single primary group are columns on the `users` table (folded from
// the former user_profiles + user_primary_group side tables, ADR 0013); organizational-group
// settings live in user_groups. Groups are labels only — permissions still come from the role.

// UserGroup is an organizational group whose settings decide its members' run
// behavior (group model B, docs/adr/0010-group-model.md). Every user has at most one
// primary group; users without one inherit the Default group. A non-default group can
// override each field or inherit it from the Default group (the *Inherit flags below
// say which); the Default group always holds concrete baselines.
type UserGroup struct {
	ID          int64
	Name        string
	Description string
	Created     string
	IsDefault   bool
	Weight      int  // urgent tickets granted per period (see docs/adr/0005-priority-tickets.md); 0 when inherited
	UrgentFree  bool // members may run urgent without spending tickets; false when inherited
	// Inherit flags: true means the field is unset on this group and resolves to the
	// Default group's value. Always false on the Default group itself.
	WeightInherit bool
	UrgentInherit bool
	Priority      string // base priority override ("" = inherit the system default)
	Members       int    // primary-member count, filled by ListUserGroups
	// Per-group governance (group model B). Value fields carry the effective/permissive
	// value when inherited; the *Inherit flags say whether this group sets them.
	AllowUrgent        bool // may members use the urgent lane
	AllowUrgentInherit bool
	MaxQueued          int // active-run cap; 0 = unlimited
	MaxQueuedInherit   bool
	RunWindow          string // "" = any hour, else "H1-H2" (panel timezone)
	RunWindowInherit   bool
	// External-user tenancy (ADR 0022). Restricted marks an external OU (owner-scoped reads +
	// run allow-list); it is sticky down the OU tree, so RestrictedEffective reports whether an
	// ancestor already restricts this group even when its own flag is off.
	Restricted          bool
	RestrictedEffective bool
	ParentID            int64  // 0 = a root OU; the tree the inherited settings resolve along
	DailyRunQuota       int    // run cap for members over QuotaPeriod; 0 = unlimited
	QuotaPeriod         string // day | week | month | total; "" = day
	DailyQuotaInherit   bool
	// Login-protection overrides (security_policy.go): may this OU's members add a second factor.
	// Same value-plus-inherit shape as the governance columns above.
	TOTPEnroll           bool
	TOTPEnrollInherit    bool
	PasskeyEnroll        bool
	PasskeyEnrollInherit bool
	// The mandate: this OU's members must have a second factor. Off unless set.
	Require2FA        bool
	Require2FAInherit bool
}

// ---------- profile ----------

// SetUserProfile sets a user's display name and email (leaving active/last_login/group_id).
// The user row always pre-exists (created by UpsertUser before any profile edit).
func (s *Store) SetUserProfile(username, displayName, email string) error {
	_, err := s.exec(`UPDATE users SET display_name=?, email=? WHERE username=?`,
		displayName, email, username)
	return err
}

// UsersByEmail returns EVERY account carrying an email, case-insensitively. Every, not the first:
// users.email has no unique index, so an SSO login that matched on it has to be able to tell an
// unambiguous match from a choice it is not entitled to make.
func (s *Store) UsersByEmail(email string) []string {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	rows, err := s.query(`SELECT username FROM users
		WHERE email IS NOT NULL AND email<>'' AND LOWER(email)=LOWER(?) ORDER BY username`, email)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil
		}
		out = append(out, u)
	}
	return out
}

// SetUserActive enables or disables a user (disabled accounts cannot log in).
func (s *Store) SetUserActive(username string, active bool) error {
	_, err := s.exec(`UPDATE users SET active=? WHERE username=?`, boolInt(active), username)
	return err
}

// SetUserExpiry sets a user's account validity cutoff (a panel-tz civil date "YYYY-MM-DD"); an
// empty string stores NULL = never expires. The caller validates/normalizes the date format.
func (s *Store) SetUserExpiry(username, expiresAt string) error {
	var v any = expiresAt
	if expiresAt == "" {
		v = nil // NULL = never
	}
	_, err := s.exec(`UPDATE users SET expires_at=? WHERE username=?`, v, username)
	return err
}

// TouchLastSeen stamps the user's last authenticated request at the given instant. Throttled by the
// caller — this is the write, not the decision to make it.
//
// The instant is a parameter rather than nowStr() so that the value STORED is the one the throttle
// recorded. Two clocks for one fact drift apart, and the drift is invisible until something has to
// reason about both.
func (s *Store) TouchLastSeen(username string, at time.Time) error {
	_, err := s.exec(`UPDATE users SET last_seen=? WHERE username=?`, at.Format("2006-01-02 15:04:05"), username)
	return err
}

// TouchLastLogin stamps the user's last successful login time.
func (s *Store) TouchLastLogin(username string) error {
	_, err := s.exec(`UPDATE users SET last_login=? WHERE username=?`, nowStr(), username)
	return err
}

// ---------- groups ----------

// nullWeight/nullUrgent build the override arguments for an insert/update: a nil
// pointer stores NULL (inherit from the Default group), a value stores the override.
func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullBoolInt(p *bool) any {
	if p == nil {
		return nil
	}
	return boolInt(*p)
}

// CreateUserGroup adds a group and returns its id. The variadic urgentFree keeps the
// old concrete-value call sites working; weight is stored as a concrete override.
func (s *Store) CreateUserGroup(name, description string, weight int, urgentFree ...bool) (int64, error) {
	uf := len(urgentFree) > 0 && urgentFree[0]
	return s.insertID(`INSERT INTO user_groups(name,description,created_at,weight,urgent_unlimited,is_default) VALUES(?,?,?,?,?,0)`,
		name, description, nowStr(), weight, boolInt(uf))
}

// UpdateGroup renames / re-describes a group and sets its per-field overrides. A nil
// weight/urgent stores NULL (inherit the Default group's value); a value overrides it.
func (s *Store) UpdateGroup(id int64, name, description string, weight *int, urgent *bool) error {
	_, err := s.exec("UPDATE user_groups SET name=?, description=?, weight=?, urgent_unlimited=? WHERE id=?",
		name, description, nullInt(weight), nullBoolInt(urgent), id)
	return err
}

// DeleteUserGroup removes a group and reassigns its former primary members to the Default
// group (users.group_id back to NULL), moves its child OUs to the deleted OU's parent, and
// re-attributes its prediction sets (P38, ADR 0039).
//
// It is ONE transaction on the tx handle only: a failure between the set re-attribution and the row
// delete must not leave the sets pointing at an OU that still exists under a different meaning, or at
// one that is gone. The transaction also removes the id-reuse risk: an id the database may reassign
// later is never referenced by a set, and neither is it referenced by purge evidence, which moves to
// the same destination as the sets in this transaction (ADR 0039).
//
// The ownership lock is the first thing the transaction does, before it reads the OU or any set. A
// retention pass takes the same lock, so the two serialize and neither can act on a state the other
// is about to change.
//
// Returns how many sets moved and the OU they moved to (the parent, or the Default OU for a root or a
// dangling parent), for the handler's audit row.
func (s *Store) DeleteUserGroup(id int64) (reattributed, target int64, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	if s.driver == "postgres" {
		if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(780716)`); err != nil {
			return 0, 0, err
		}
	}

	var parent sql.NullInt64
	var isDefault int
	// Taken before the first read or write of any row; nothing above this line touches data.
	owned := migExec{s: s, ex: tx}
	if err := lockPredictionOwnership(owned); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRow(s.bind(`SELECT parent_id, COALESCE(is_default,0) FROM user_groups WHERE id=?`), id).
		Scan(&parent, &isDefault); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, fmt.Errorf("no such group")
		}
		return 0, 0, err
	}
	// Any group flagged is_default is never deletable — the resolution depends on it. We check the
	// row's own flag (not just DefaultGroupID) so even a stray duplicate default cannot be removed.
	if isDefault != 0 {
		return 0, 0, fmt.Errorf("the Default group cannot be deleted")
	}
	if err := validateOUDeletionReferences(owned, id); err != nil {
		return 0, 0, err
	}
	var defaultID int64
	if err := tx.QueryRow(s.bind(`SELECT id FROM user_groups WHERE is_default=1 ORDER BY id LIMIT 1`)).
		Scan(&defaultID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	target = parent.Int64
	parentExists := false
	if target != 0 {
		var one int
		if err := tx.QueryRow(s.bind(`SELECT 1 FROM user_groups WHERE id=?`), target).Scan(&one); err == nil {
			parentExists = true
		} else {
			// A parent row that is gone: fail closed to the Default OU rather than leave a set (or a
			// child) pointing at a missing id.
			target = defaultID
		}
	} else {
		target = defaultID
	}
	platformImpacts, err := appPlatformPrincipalImpacts(owned, "", id)
	if err != nil {
		return 0, 0, err
	}

	if target != 0 {
		res, err := tx.Exec(s.bind(`UPDATE prediction_sets SET attributed_ou=? WHERE attributed_ou=?`), target, id)
		if err != nil {
			return 0, 0, err
		}
		if n, e := res.RowsAffected(); e == nil {
			reattributed = n
		}
	} else if defaultID == 0 {
		// No Default OU to move them to (a corrupt database): unattributed is the fail-closed answer.
		res, err := tx.Exec(s.bind(`UPDATE prediction_sets SET attributed_ou=NULL WHERE attributed_ou=?`), id)
		if err != nil {
			return 0, 0, err
		}
		if n, e := res.RowsAffected(); e == nil {
			reattributed = n
		}
	}
	// Evidence follows the sets whether or not any set is left: retention may already have deleted
	// them all. With no destination the evidence is dropped rather than left on an id that could be
	// reused, the same fail-closed direction as the NULL attribution above.
	if err := reattributePredictionPurgeOwners(owned, id, target); err != nil {
		return 0, 0, err
	}

	if _, err := tx.Exec(s.bind(`UPDATE users SET group_id=NULL WHERE group_id=?`), id); err != nil {
		return 0, 0, err
	}
	for _, q := range []string{
		`DELETE FROM report_viewers WHERE principal=?`,
		`DELETE FROM version_grants WHERE principal=?`,
		`DELETE FROM announcement_grants WHERE principal=?`,
		`DELETE FROM notification_route_grants WHERE principal=?`,
	} {
		if _, err := tx.Exec(s.bind(q), groupPrincipal(id)); err != nil {
			return 0, 0, err
		}
	}
	if _, err := tx.Exec(s.bind(`DELETE FROM group_targets WHERE group_id=?`), id); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(s.bind(`DELETE FROM group_target_policies WHERE group_id=?`), id); err != nil {
		return 0, 0, err
	}
	// Children keep inheriting along the line they were in: to the deleted OU's parent, or as roots
	// when the deleted OU was a root (§16 item 9). A parent row that is gone would leave them
	// dangling, so they inherit from the Default instead — the same fail-closed direction as the sets.
	var childParent any
	if parent.Valid && parentExists {
		childParent = parent.Int64
	}
	// A dangling grandparent makes the children roots, not children of the Default: a root is not in
	// the Default's subtree, so its sets cannot join the Default's 'own' population. That is the
	// fail-closed direction. (A valid parent keeps them on the line they were on.)
	if _, err := tx.Exec(s.bind(`UPDATE user_groups SET parent_id=? WHERE parent_id=?`), childParent, id); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(s.bind(`DELETE FROM user_groups WHERE id=?`), id); err != nil {
		return 0, 0, err
	}
	if err := invalidateAppPlatformPrincipal(owned, platformImpacts); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return reattributed, target, nil
}

// userGroupExists reports whether an OU row is present.
func (s *Store) userGroupExists(id int64) bool {
	var n int
	s.queryRow(`SELECT COUNT(*) FROM user_groups WHERE id=?`, id).Scan(&n)
	return n > 0
}

// EnsureDefaultForecastScope normalises the Default OU's forecast scope to none (P38, ADR 0039).
//
// The step cannot do this itself: a migration that writes data cannot accept a dump written before
// it (olderDumpOK), and a restored older dump must come back with every scope NULL. Running the
// UPDATE here at boot and at the end of every restore keeps the rule without changing the step.
// Idempotent, and the resolver reads a NULL Default as none even between the step and this call.
func (s *Store) EnsureDefaultForecastScope() error {
	return ensureDefaultForecastScope(s.poolExec())
}

func ensureDefaultForecastScope(e migExec) error {
	_, err := e.exec(`UPDATE user_groups SET forecast_scope='none' WHERE is_default=1 AND forecast_scope IS NULL`)
	return err
}

// SetGroupPriority sets a group's base priority override (ADR 0007). An empty priority stores
// NULL, so a non-default group then inherits the system default. The group row always exists.
func (s *Store) SetGroupPriority(groupID int64, priority string) error {
	var val any // nil -> NULL (inherit), matching the old delete-the-side-row semantics
	if priority != "" {
		val = priority
	}
	_, err := s.exec("UPDATE user_groups SET priority=? WHERE id=?", val, groupID)
	return err
}

// GroupPriority returns a group's base-priority override, or "" if it inherits.
func (s *Store) GroupPriority(groupID int64) string {
	var p sql.NullString
	s.queryRow("SELECT priority FROM user_groups WHERE id=?", groupID).Scan(&p)
	return p.String
}

// DefaultGroupID returns the id of the Default (fallback) group, or 0 if none exists.
func (s *Store) DefaultGroupID() int64 {
	var id sql.NullInt64
	s.queryRow("SELECT id FROM user_groups WHERE is_default=1 ORDER BY id LIMIT 1").Scan(&id)
	return id.Int64
}

// EnsureDefaultGroup creates the Default group if none exists and returns its id. It is
// idempotent and safe to call on every boot. The Default group holds concrete baselines
// (weight 0, urgent-unlimited off) that every other group inherits from.
func (s *Store) EnsureDefaultGroup() int64 {
	if id := s.DefaultGroupID(); id != 0 {
		return id
	}
	// Pick a free name (the column is UNIQUE): "Default", then "Default (1)", ...
	base := "Default"
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s (%d)", base, i)
		}
		id, err := s.insertID(`INSERT INTO user_groups(name,description,created_at,weight,urgent_unlimited,is_default) VALUES(?,?,?,0,0,1)`,
			name, "Fallback group — users without an assigned group inherit these settings.", nowStr())
		if err == nil {
			return id
		}
	}
	return s.DefaultGroupID()
}

// ListUserGroups returns all groups (Default first, then by name) with their primary-
// member counts, per-field override values, and inherit flags.
func (s *Store) ListUserGroups() []UserGroup {
	groups, _ := s.ListUserGroupsErr()
	return groups
}

func (s *Store) ListUserGroupsErr() ([]UserGroup, error) {
	snapshot, members, err := s.loadOUGroupListing()
	if err != nil {
		return nil, err
	}
	return snapshot.groupRows(members), nil
}

// ---------- primary group (membership) ----------

// SetPrimaryGroup validates both ends under the same writer boundary as OU deletion. Zero is
// an explicit clear; an invalid destination or failed read is never interpreted as that choice.
func (s *Store) SetPrimaryGroup(username string, groupID int64) error {
	_, err := s.SetPrimaryGroups([]string{username}, groupID)
	return err
}

// PrimaryGroupOf returns a user's primary group id, or 0 if they inherit the Default (a NULL
// group_id, or a missing user, reads back as 0).
func (s *Store) PrimaryGroupOf(username string) int64 {
	var id sql.NullInt64
	s.queryRow("SELECT group_id FROM users WHERE username=?", username).Scan(&id)
	return id.Int64
}

// AllPrimaryGroups returns username → primary group id for every assigned user, so a
// user list can be enriched with one query instead of N.
func (s *Store) AllPrimaryGroups() map[string]int64 {
	m := map[string]int64{}
	rows, err := s.query("SELECT username, group_id FROM users WHERE group_id IS NOT NULL")
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			continue
		}
		m[name] = id
	}
	return m
}

// GroupSettings is a user's fully-resolved run governance (group model B / OU tree, ADR 0022).
type GroupSettings struct {
	Weight          int    // urgent tickets granted per period
	UrgentUnlimited bool   // may run urgent without spending tickets
	AllowUrgent     bool   // may use the urgent lane at all
	MaxQueued       int    // cap on active (queued+running) jobs per user; 0 = unlimited
	RunWindow       string // "" = any hour, else "startHour-endHour" (panel timezone)
	Restricted      bool   // external OU: owner-scoped reads + run allow-list apply (sticky down the tree)
	DailyRunQuota   int    // cap on runs for a restricted user in QuotaPeriod; 0 = unlimited
	QuotaPeriod     string // day | week | month | total; "" = day
}

// EffectiveGroupSettings is the compatibility value-only view. Enforcement must use the
// error-aware form; an unreadable policy must never become an unscoped permissive baseline.
func (s *Store) EffectiveGroupSettings(username string) GroupSettings {
	out, err := s.EffectiveGroupSettingsErr(username)
	if err != nil {
		return GroupSettings{Restricted: true}
	}
	return out
}

func (s *Store) EffectiveGroupSettingsErr(username string) (GroupSettings, error) {
	sub, snapshot, err := s.loadOUPolicySubject(username)
	if err != nil {
		return GroupSettings{}, err
	}
	policies, err := snapshot.resolve(sub.EffectiveOU)
	if err != nil {
		return GroupSettings{}, err
	}
	return policies.governance(), nil
}

// groupParent returns a group's parent OU id, or 0 for a root (NULL parent_id) or missing group.
func (s *Store) groupParent(id int64) int64 {
	var p sql.NullInt64
	s.queryRow("SELECT parent_id FROM user_groups WHERE id=?", id).Scan(&p)
	return p.Int64
}

// groupChain is the compatibility view for existing audience callers. A broken tree yields
// no OU principals. Enforcement callers use groupChainErr to retain the failure itself.
func (s *Store) groupChain(username string) []int64 {
	chain, _ := s.groupChainErr(username)
	return chain
}

func (s *Store) groupChainErr(username string) ([]int64, error) {
	sub, snapshot, err := s.loadOUPolicySubject(username)
	if err != nil {
		return nil, err
	}
	chain, err := snapshot.graph.policyChain(sub.EffectiveOU)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// groupAncestry is a display compatibility view of the same validated policy graph.
func (s *Store) groupAncestry(leaf, _ int64) []int64 {
	snapshot, err := s.loadOUPolicySnapshotFrom(s.db)
	if err != nil {
		return nil
	}
	chain, err := snapshot.graph.policyChain(leaf)
	if err != nil {
		return nil
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// SetGroupParent moves an OU under a parent (0 = make it a root; NULL parent_id). No cycle check
// here — the admin API guards that before calling; groupChain is itself cycle-guarded at read time.
func (s *Store) SetGroupParent(id, parent int64) error {
	var v any = parent
	if parent == 0 {
		v = nil
	}
	_, err := s.exec(`UPDATE user_groups SET parent_id=? WHERE id=?`, v, id)
	return err
}

// SetGroupRestricted flags (or clears) an OU as external/restricted. Restriction is sticky down the
// tree at resolution time, so clearing it here only affects OUs with no restricted ancestor.
func (s *Store) SetGroupRestricted(id int64, restricted bool) error {
	_, err := s.exec(`UPDATE user_groups SET restricted=? WHERE id=?`, boolInt(restricted), id)
	return err
}

// SetGroupDailyQuota sets an OU's run cap and the window it is measured over; nil quota stores NULL
// (inherit the parent), 0 = unlimited.
//
// The two are written together because they are one setting. Storing a period without a number
// configures nothing, and letting them be set separately would allow a cap of "20" from one OU to
// be inherited alongside a period of "month" from another — a limit neither of them chose.
func (s *Store) SetGroupDailyQuota(id int64, quota *int, period string) error {
	if !validQuotaPeriod(period) {
		period = QuotaDay
	}
	// The period is NULL exactly when the quota is, so the pair inherits or overrides as one.
	var p any
	if quota != nil {
		p = period
	}
	_, err := s.exec(`UPDATE user_groups SET daily_run_quota=?, run_quota_period=? WHERE id=?`,
		nullInt(quota), p, id)
	return err
}

// SetGroupGovernance writes a group's allow-urgent / max-queued / run-window overrides.
// A nil pointer stores NULL (inherit the Default group); the Default group is coerced to
// concrete by the API before calling this.
func (s *Store) SetGroupGovernance(id int64, allowUrgent *bool, maxQueued *int, runWindow *string) error {
	var rw any
	if runWindow != nil {
		rw = *runWindow
	}
	_, err := s.exec("UPDATE user_groups SET allow_urgent=?, max_queued=?, run_window=? WHERE id=?",
		nullBoolInt(allowUrgent), nullInt(maxQueued), rw, id)
	return err
}

// GroupRestrictedEffective reports whether an OU is external once inheritance is applied — its own
// flag, or any ancestor's, since restriction is sticky down the tree.
func (s *Store) GroupRestrictedEffective(id int64) bool {
	for _, g := range s.ListUserGroups() {
		if g.ID == id {
			return g.RestrictedEffective
		}
	}
	return false
}
