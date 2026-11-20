package app

import (
	"errors"
	"testing"
)

// platformAppliedRaiseRows is a snapshot of every applied set-data-quota effect row: the chain relies on these rows
// never being rewritten after they apply (doc 185, risk 4), so a test compares two snapshots.
func platformAppliedRaiseRows(t *testing.T, s *Server) map[string]string {
	t.Helper()
	rows, err := s.st.query(`SELECT operation_id||'/'||stage||'/'||effect_number, effect_id||'|'||payload_sha256||'|'||updated_ms||'|'||observed_fence||'|'||receipt_json
		FROM app_platform_operation_effects WHERE kind='set-data-quota' AND state='applied'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		result[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func platformRequireSameRaiseRows(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) == 0 {
		t.Fatal("no applied raise rows to compare")
	}
	for key, value := range before {
		if after[key] != value {
			t.Fatalf("applied raise row %s was rewritten: %q -> %q", key, value, after[key])
		}
	}
}

// The quota chain is derived from applied set-data-quota effect receipts, linked by where each raise starts.
func testPlatformQuotaChainFromEffects(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	(&platformFakeDataRunner{fits: true}).install(t)
	installation := enabled.installationID
	namespace := scalar[string](t, s.st, `SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	inodes0 := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	first := platformRaiseQuota(t, s, actor, installation, target, runtime, bytes0*2, inodes0, 1500)
	second := platformRaiseQuota(t, s, actor, installation, target, runtime, bytes0*2, inodes0*2, 1510)
	chain, err := platformNamespaceQuotaChain(s.st.poolExec(), installation, namespace)
	if err != nil || len(chain) != 3 || chain[1].Bytes != bytes0*2 || chain[1].Inodes != inodes0 || chain[2].Bytes != bytes0*2 || chain[2].Inodes != inodes0*2 {
		t.Fatalf("chain from effects: %+v %v", chain, err)
	}
	// A link takes effect when its effect row was written: for a standalone raise, when its operation completed.
	for i, op := range []platformOperationLease{first, second} {
		if want := scalar[int64](t, s.st, `SELECT completed_ms FROM app_platform_operations WHERE id=?`, op.id); chain[i+1].CompletedMS != want {
			t.Fatalf("link %d took effect at %d, its operation completed at %d", i+1, chain[i+1].CompletedMS, want)
		}
	}
	rowsBefore := platformAppliedRaiseRows(t, s)

	tamper := func(name, statement string, args ...any) {
		t.Run(name, func(t *testing.T) {
			tx, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			e := migExec{s: s.st, ex: tx}
			if _, err := e.exec(statement, args...); err != nil {
				t.Fatal(err)
			}
			_, chainErr := platformNamespaceQuotaChain(e, installation, namespace)
			if !errors.Is(chainErr, errAppPlatformStageEvidence) {
				t.Fatalf("a tampered chain was accepted: %v", chainErr)
			}
		})
	}
	tamper("a wrong old value",
		`UPDATE app_platform_operation_effects SET receipt_json=REPLACE(receipt_json,'"old_inodes":"`+itoa64(inodes0)+`"','"old_inodes":"`+itoa64(inodes0+1)+`"') WHERE operation_id=? AND kind='set-data-quota'`, first.id)
	tamper("a foreign namespace",
		`UPDATE app_platform_operation_effects SET receipt_json=REPLACE(receipt_json,'`+namespace+`','00000000-0000-4000-8000-000000000000') WHERE operation_id=? AND kind='set-data-quota'`, first.id)
	tamper("a payload digest that is not the receipt's",
		`UPDATE app_platform_operation_effects SET payload_sha256=? WHERE operation_id=? AND kind='set-data-quota'`, platformDigest([]byte("other")), first.id)
	tamper("an effect id that is not the receipt's",
		`UPDATE app_platform_operation_effects SET effect_id=? WHERE operation_id=? AND kind='set-data-quota'`, platformFixtureID(1590), first.id)
	tamper("a receipt that does not match its operation's plan",
		`UPDATE app_platform_operation_effects SET receipt_json=REPLACE(receipt_json,'"new_inodes":"`+itoa64(inodes0*2)+`"','"new_inodes":"`+itoa64(inodes0*3)+`"') WHERE operation_id=? AND kind='set-data-quota'`, second.id)
	tamper("a leftover link the chain never reaches",
		`UPDATE app_platform_operation_effects SET receipt_json=REPLACE(REPLACE(receipt_json,'"old_bytes":"`+itoa64(bytes0*2)+`"','"old_bytes":"`+itoa64(bytes0*3)+`"'),'"new_bytes":"`+itoa64(bytes0*2)+`"','"new_bytes":"`+itoa64(bytes0*4)+`"') WHERE operation_id=? AND kind='set-data-quota'`, second.id)

	// An effect that did not apply is no link: a standalone failure leaves its row prepared or reverted.
	for _, state := range []string{"prepared", "reverted", "unknown"} {
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state=? WHERE operation_id=? AND kind='set-data-quota'`, state, second.id)
		chain, err := platformNamespaceQuotaChain(s.st.poolExec(), installation, namespace)
		if err != nil || len(chain) != 2 {
			t.Fatalf("a %s raise entered the chain: %+v %v", state, chain, err)
		}
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='applied' WHERE operation_id=? AND kind='set-data-quota'`, second.id)
	platformRequireSameRaiseRows(t, rowsBefore, platformAppliedRaiseRows(t, s))
}

func TestPlatformQuotaChainFromEffectsSQLite(t *testing.T)   { testPlatformQuotaChainFromEffects(t, false) }
func TestPlatformQuotaChainFromEffectsPostgres(t *testing.T) { testPlatformQuotaChainFromEffects(t, true) }
