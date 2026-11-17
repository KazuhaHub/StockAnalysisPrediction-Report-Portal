package app

import (
	"database/sql"
)

type ouSubject struct {
	Username    string
	AssignedOU  int64
	EffectiveOU int64
	Role        string
	Restricted  bool
	Active      bool
	Created     string
}

type ouRunQuota struct {
	Limit  int    `json:"limit"`
	Period string `json:"period"`
}

type ouPolicyValues struct {
	weight, maxQueued               *int
	urgent, allowUrgent, restricted *bool
	runWindow, priority, scope      *string
	quota                           *ouRunQuota
	totp, passkey, require2FA       *bool
	// Reader export policy (ADR 0046); nil inherits.
	exportAllowed               *bool
	exportMax, exportDailyQuota *int
}

type ouPolicySnapshot struct {
	graph  ouGraph
	values map[int64]ouPolicyValues
}

type ouResolvedPolicies struct {
	Weight          ouPolicyResult[int]        `json:"weight"`
	UrgentUnlimited ouPolicyResult[bool]       `json:"urgent_unlimited"`
	AllowUrgent     ouPolicyResult[bool]       `json:"allow_urgent"`
	MaxQueued       ouPolicyResult[int]        `json:"max_queued"`
	RunWindow       ouPolicyResult[string]     `json:"run_window"`
	Restricted      ouPolicyResult[bool]       `json:"restricted"`
	Quota           ouPolicyResult[ouRunQuota] `json:"quota"`
	TOTP            ouPolicyResult[bool]       `json:"totp_enroll"`
	Passkey         ouPolicyResult[bool]       `json:"passkey_enroll"`
	Require2FA      ouPolicyResult[bool]       `json:"require_2fa"`
	ForecastScope   ouPolicyResult[string]     `json:"forecast_scope"`
	ExportAllowed   ouPolicyResult[bool]       `json:"export_allowed"`
	ExportMax       ouPolicyResult[int]        `json:"export_max_reports"`
	ExportQuota     ouPolicyResult[int]        `json:"export_daily_quota"`
}

func ouNullInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}
func ouNullBool(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Int64 != 0
	return &b
}
func ouNullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// ouGroupColumns is the column list of one organisation-tree row, with each column qualified by prefix ("" or
// "g."); ouGroupRow scans exactly these columns, so the tree's own query and the account-and-tree query share them.
func ouGroupColumns(p string) string {
	return p + `id,COALESCE(` + p + `parent_id,0),COALESCE(` + p + `is_default,0),
		COALESCE(` + p + `name,''),COALESCE(` + p + `created_at,''),COALESCE(` + p + `description,''),` + p + `weight,` + p + `urgent_unlimited,` + p + `allow_urgent,` + p + `max_queued,
		` + p + `run_window,` + p + `restricted,` + p + `daily_run_quota,` + p + `run_quota_period,` + p + `totp_enroll,` + p + `passkey_enroll,` + p + `require_2fa,
		` + p + `forecast_scope,` + p + `priority,` + p + `export_allowed,` + p + `export_max_reports,` + p + `export_daily_quota`
}

// ouGroupRow is one scanned organisation-tree row. Its id is null only for the account-and-tree query's row of
// an account when the tree is empty.
type ouGroupRow struct {
	id                                                                      sql.NullInt64
	parent                                                                  int64
	isDefault                                                               int
	name, created, description                                              string
	weight, urgent, allow, maxQueued, restricted, quota, totp, passkey, req sql.NullInt64
	exportAllowed, exportMax, exportQuota                                   sql.NullInt64
	window, period, scope, priority                                         sql.NullString
}

func (r *ouGroupRow) dest() []any {
	return []any{&r.id, &r.parent, &r.isDefault, &r.name, &r.created, &r.description, &r.weight, &r.urgent, &r.allow, &r.maxQueued,
		&r.window, &r.restricted, &r.quota, &r.period, &r.totp, &r.passkey, &r.req, &r.scope, &r.priority,
		&r.exportAllowed, &r.exportMax, &r.exportQuota}
}

// ouSnapshotBuilder collects scanned rows into a snapshot.
type ouSnapshotBuilder struct {
	nodes  map[int64]ouNode
	values map[int64]ouPolicyValues
}

func newOUSnapshotBuilder() *ouSnapshotBuilder {
	return &ouSnapshotBuilder{nodes: make(map[int64]ouNode), values: make(map[int64]ouPolicyValues)}
}

func (b *ouSnapshotBuilder) add(r ouGroupRow) {
	if !r.id.Valid {
		return
	}
	id := r.id.Int64
	b.nodes[id] = ouNode{parent: r.parent, isDefault: r.isDefault == 1, name: r.name, description: r.description, created: r.created}
	v := ouPolicyValues{weight: ouNullInt(r.weight), urgent: ouNullBool(r.urgent), allowUrgent: ouNullBool(r.allow),
		maxQueued: ouNullInt(r.maxQueued), runWindow: ouNullString(r.window), restricted: ouNullBool(r.restricted),
		totp: ouNullBool(r.totp), passkey: ouNullBool(r.passkey), require2FA: ouNullBool(r.req),
		scope: ouNullString(r.scope), priority: ouNullString(r.priority),
		exportAllowed: ouNullBool(r.exportAllowed), exportMax: ouNullInt(r.exportMax), exportDailyQuota: ouNullInt(r.exportQuota)}
	if r.quota.Valid {
		p := QuotaDay
		if validQuotaPeriod(r.period.String) {
			p = r.period.String
		}
		v.quota = &ouRunQuota{Limit: int(r.quota.Int64), Period: p}
	}
	b.values[id] = v
}

func (b *ouSnapshotBuilder) snapshot() ouPolicySnapshot {
	return ouPolicySnapshot{graph: newOUGraph(b.nodes), values: b.values}
}

// loadOUPolicySnapshotFrom loads node identity and typed overrides in one statement. It never
// acquires another pool connection, so it is also safe inside SQLite's sole writer transaction.
func (s *Store) loadOUPolicySnapshotFrom(ex execer) (ouPolicySnapshot, error) {
	rows, err := ex.Query(`SELECT ` + ouGroupColumns("") + ` FROM user_groups`)
	if err != nil {
		return ouPolicySnapshot{}, err
	}
	defer rows.Close()
	b := newOUSnapshotBuilder()
	for rows.Next() {
		var r ouGroupRow
		if err := rows.Scan(r.dest()...); err != nil {
			return ouPolicySnapshot{}, err
		}
		b.add(r)
	}
	if err := rows.Err(); err != nil {
		return ouPolicySnapshot{}, err
	}
	return b.snapshot(), nil
}

func (s *Store) loadOUPolicySubjectFrom(ex execer, username string) (ouSubject, ouPolicySnapshot, error) {
	var sub ouSubject
	var restricted, active int
	err := ex.QueryRow(s.bind(`SELECT username,COALESCE(group_id,0),COALESCE(role,'user'),
		COALESCE(restricted,0),COALESCE(active,1),COALESCE(created_at,'') FROM users WHERE username=?`), username).
		Scan(&sub.Username, &sub.AssignedOU, &sub.Role, &restricted, &active, &sub.Created)
	if err != nil {
		return ouSubject{}, ouPolicySnapshot{}, err
	}
	snap, err := s.loadOUPolicySnapshotFrom(ex)
	if err != nil {
		return ouSubject{}, ouPolicySnapshot{}, err
	}
	sub.Restricted = restricted != 0
	sub.Active = active != 0
	sub.EffectiveOU = sub.AssignedOU
	if sub.EffectiveOU == 0 {
		sub.EffectiveOU = snap.graph.defaultID
	}
	return sub, snap, nil
}

// loadOUPolicySubject reads the account row and the whole organisation tree in one statement: one row per OU,
// each carrying the account's columns (one row with a null OU when the tree is empty, none when the account does
// not exist). A single statement reads one state of the database on both drivers, so the row and the tree agree
// without an explicit transaction, and it costs one round trip instead of four on PostgreSQL. It decides exactly
// what loadOUPolicySubjectFrom decides over a transaction, and a missing account is sql.ErrNoRows as there.
func (s *Store) loadOUPolicySubject(username string) (ouSubject, ouPolicySnapshot, error) {
	rows, err := s.readQuery(`SELECT u.username,COALESCE(u.group_id,0),COALESCE(u.role,'user'),
		COALESCE(u.restricted,0),COALESCE(u.active,1),COALESCE(u.created_at,''),`+ouGroupColumns("g.")+`
		FROM users u LEFT JOIN user_groups g ON 1=1 WHERE u.username=?`, username)
	if err != nil {
		return ouSubject{}, ouPolicySnapshot{}, err
	}
	defer rows.Close()
	var sub ouSubject
	var restricted, active int
	found := false
	b := newOUSnapshotBuilder()
	for rows.Next() {
		var r ouGroupRow
		dest := append([]any{&sub.Username, &sub.AssignedOU, &sub.Role, &restricted, &active, &sub.Created}, r.dest()...)
		if err := rows.Scan(dest...); err != nil {
			return ouSubject{}, ouPolicySnapshot{}, err
		}
		found = true
		b.add(r)
	}
	if err := rows.Err(); err != nil {
		return ouSubject{}, ouPolicySnapshot{}, err
	}
	if !found {
		return ouSubject{}, ouPolicySnapshot{}, sql.ErrNoRows
	}
	snap := b.snapshot()
	sub.Restricted = restricted != 0
	sub.Active = active != 0
	sub.EffectiveOU = sub.AssignedOU
	if sub.EffectiveOU == 0 {
		sub.EffectiveOU = snap.graph.defaultID
	}
	return sub, snap, nil
}

func (s ouPolicySnapshot) resolve(leaf int64) (ouResolvedPolicies, error) {
	chain, err := s.graph.policyChain(leaf)
	if err != nil {
		return ouResolvedPolicies{}, err
	}
	if len(chain) == 0 {
		return ouResolvedPolicies{}, errDanglingOU
	}
	p := ouResolvedPolicies{
		Weight:          nearestOUPolicy(chain, 0, func(id int64) *int { return s.values[id].weight }),
		UrgentUnlimited: nearestOUPolicy(chain, false, func(id int64) *bool { return s.values[id].urgent }),
		AllowUrgent:     nearestOUPolicy(chain, true, func(id int64) *bool { return s.values[id].allowUrgent }),
		MaxQueued:       nearestOUPolicy(chain, 0, func(id int64) *int { return s.values[id].maxQueued }),
		RunWindow:       nearestOUPolicy(chain, "", func(id int64) *string { return s.values[id].runWindow }),
		Restricted:      stickyOUPolicy(chain, func(id int64) *bool { return s.values[id].restricted }),
		Quota:           nearestOUPolicy(chain, ouRunQuota{Period: QuotaDay}, func(id int64) *ouRunQuota { return s.values[id].quota }),
		TOTP:            nearestOUPolicy(chain, true, func(id int64) *bool { return s.values[id].totp }),
		Passkey:         nearestOUPolicy(chain, true, func(id int64) *bool { return s.values[id].passkey }),
		Require2FA:      stickyOUPolicy(chain, func(id int64) *bool { return s.values[id].require2FA }),
		ForecastScope:   nearestOUPolicy(chain, scopeNone, func(id int64) *string { return s.values[id].scope }),
		// Not sticky: an administrator may switch export on for one sub-OU without its parent.
		ExportAllowed: nearestOUPolicy(chain, false, func(id int64) *bool { return s.values[id].exportAllowed }),
		ExportMax:     nearestOUPolicy(chain, defaultExportMaxReports, func(id int64) *int { return s.values[id].exportMax }),
		ExportQuota:   nearestOUPolicy(chain, defaultExportDailyQuota, func(id int64) *int { return s.values[id].exportDailyQuota }),
	}
	switch p.ForecastScope.Effective {
	case scopeAll, scopeOwn, scopeNone:
	default:
		p.ForecastScope.Effective = scopeNone
	}
	return p, nil
}

func (p ouResolvedPolicies) governance() GroupSettings {
	return GroupSettings{Weight: p.Weight.Effective, UrgentUnlimited: p.UrgentUnlimited.Effective,
		AllowUrgent: p.AllowUrgent.Effective, MaxQueued: p.MaxQueued.Effective, RunWindow: p.RunWindow.Effective,
		Restricted: p.Restricted.Effective, DailyRunQuota: p.Quota.Effective.Limit, QuotaPeriod: p.Quota.Effective.Period}
}

func (p ouResolvedPolicies) security() SecuritySettings {
	return SecuritySettings{TOTPAllowed: p.TOTP.Effective, PasskeyAllowed: p.Passkey.Effective, Require2FA: p.Require2FA.Effective}
}
