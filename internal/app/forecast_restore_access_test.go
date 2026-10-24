package app

// A restore and the reader-surface access settings.
//
// The reading stage and the evaluator mode follow "a restore never raises anything". The OU forecast
// scopes do not: they come back exactly as dumped (implementation plan section 3.3), so a restore can
// WIDEN access compared with the live database, and it takes the audit log with it. The runbook tells
// the operator to compare the access endpoint after any restore that follows a record incident; these
// tests pin the behaviour that instruction rests on, so the runbook cannot drift from the code.

import (
	"bytes"
	"database/sql"
	"testing"
)

func TestRestoreBringsOUScopesBackAsDumped(t *testing.T) {
	eachRestoreDriver(t, func(t *testing.T, dst *Store) {
		// The dump was taken while the OU was open to its members.
		src := newTestStore(t)
		open := seedOU(t, src, 0, scopeAll)
		dump := dumpOf(t, src)

		// Later a record incident closed every OU, and the edit was audited.
		closed := seedOU(t, dst, 0, scopeNone)
		dst.WriteAudit(AuditEntry{
			Actor: "admin", Action: AuditGroupChange, TargetType: "group", TargetID: itoa64(closed),
			Detail: `{"forecast_scope":{"from":"all","to":"none"}}`,
		})
		if rows, _ := dst.ListAudit(AuditFilter{Action: AuditGroupChange}); len(rows) != 1 {
			t.Fatalf("setup: the incident edit left %d audit rows, want 1", len(rows))
		}

		if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
			t.Fatalf("restore: %v", err)
		}

		var scope sql.NullString
		if err := dst.queryRow(`SELECT forecast_scope FROM user_groups WHERE id=?`, open).Scan(&scope); err != nil {
			t.Fatalf("the dumped OU is missing after the restore: %v", err)
		}
		if !scope.Valid || scope.String != scopeAll {
			t.Fatalf("OU scope after the restore = %v, want the dumped %q: a restore widens access, it does not take the lower of the two", scope, scopeAll)
		}
		// The audit log is part of the dump, so the incident's own edit is gone with the restore and
		// leaves no trace of the widening. The access endpoint is the only record of what is open.
		if rows, _ := dst.ListAudit(AuditFilter{Action: AuditGroupChange}); len(rows) != 0 {
			t.Fatalf("the incident's audit row survived a restore from an older dump: %+v", rows)
		}
	})
}
