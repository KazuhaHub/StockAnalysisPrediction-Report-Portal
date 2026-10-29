package app

import (
	"context"
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

// loadOUPolicySnapshotFrom loads node identity and typed overrides in one statement. It never
// acquires another pool connection, so it is also safe inside SQLite's sole writer transaction.
func (s *Store) loadOUPolicySnapshotFrom(ex execer) (ouPolicySnapshot, error) {
	rows, err := ex.Query(`SELECT id,COALESCE(parent_id,0),COALESCE(is_default,0),
		COALESCE(name,''),COALESCE(created_at,''),COALESCE(description,''),weight,urgent_unlimited,allow_urgent,max_queued,
		run_window,restricted,daily_run_quota,run_quota_period,totp_enroll,passkey_enroll,require_2fa,
		forecast_scope,priority,export_allowed,export_max_reports,export_daily_quota FROM user_groups`)
	if err != nil {
		return ouPolicySnapshot{}, err
	}
	defer rows.Close()
	nodes := make(map[int64]ouNode)
	values := make(map[int64]ouPolicyValues)
	for rows.Next() {
		var id, parent int64
		var isDefault int
		var name, created, description string
		var weight, urgent, allow, maxQueued, restricted, quota, totp, passkey, require sql.NullInt64
		var exportAllowed, exportMax, exportQuota sql.NullInt64
		var window, period, scope, priority sql.NullString
		if err := rows.Scan(&id, &parent, &isDefault, &name, &created, &description, &weight, &urgent, &allow, &maxQueued,
			&window, &restricted, &quota, &period, &totp, &passkey, &require, &scope, &priority,
			&exportAllowed, &exportMax, &exportQuota); err != nil {
			return ouPolicySnapshot{}, err
		}
		nodes[id] = ouNode{parent: parent, isDefault: isDefault == 1, name: name, description: description, created: created}
		v := ouPolicyValues{weight: ouNullInt(weight), urgent: ouNullBool(urgent), allowUrgent: ouNullBool(allow),
			maxQueued: ouNullInt(maxQueued), runWindow: ouNullString(window), restricted: ouNullBool(restricted),
			totp: ouNullBool(totp), passkey: ouNullBool(passkey), require2FA: ouNullBool(require),
			scope: ouNullString(scope), priority: ouNullString(priority),
			exportAllowed: ouNullBool(exportAllowed), exportMax: ouNullInt(exportMax), exportDailyQuota: ouNullInt(exportQuota)}
		if quota.Valid {
			p := QuotaDay
			if validQuotaPeriod(period.String) {
				p = period.String
			}
			v.quota = &ouRunQuota{Limit: int(quota.Int64), Period: p}
		}
		values[id] = v
	}
	if err := rows.Err(); err != nil {
		return ouPolicySnapshot{}, err
	}
	return ouPolicySnapshot{graph: newOUGraph(nodes), values: values}, nil
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

func (s *Store) loadOUPolicySubject(username string) (ouSubject, ouPolicySnapshot, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return ouSubject{}, ouPolicySnapshot{}, err
	}
	defer tx.Rollback()
	sub, snap, err := s.loadOUPolicySubjectFrom(tx, username)
	if err != nil {
		return ouSubject{}, ouPolicySnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return ouSubject{}, ouPolicySnapshot{}, err
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
