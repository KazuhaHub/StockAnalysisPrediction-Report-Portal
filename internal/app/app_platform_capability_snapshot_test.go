package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func testPlatformCapabilitySnapshotRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"missing", "unknown", "policy", "review", "review_declines", "review_binding"} {
		t.Run(kind, func(t *testing.T) {
			s, reader, id := platformLaunchFixture(t, pg)
			platformGrantLaunchUser(t, s, reader, id)
			binding, err := s.inspectPlatformLaunchAccess(reader, id)
			if err != nil {
				t.Fatal("coherent denied capabilities refused", err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=?`, id) == 0 {
				t.Fatal("optional capabilities absent from fixture")
			}
			switch kind {
			case "missing":
				mustExec(t, s.st, `DELETE FROM app_platform_capability_grants WHERE installation_id=?`, id)
			case "unknown":
				mustExec(t, s.st, `UPDATE app_platform_capability_grants SET capability='unknown.capability' WHERE installation_id=?`, id)
			case "policy":
				mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json='{"origin":"https://unreviewed.invalid"}' WHERE installation_id=?`, id)
			case "review_declines":
				var plan, raw string
				if err := s.st.queryRow(`SELECT p.id,p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=?`, id).Scan(&plan, &raw); err != nil {
					t.Fatal(err)
				}
				var projection platformInstallProjection
				if err := json.Unmarshal([]byte(raw), &projection); err != nil {
					t.Fatal(err)
				}
				projection.DeclinedCapabilities = []string{}
				changed, err := json.Marshal(projection)
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=?`, string(changed), platformDigest(changed), plan)
			case "review_binding":
				const columns = "installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms"
				other := platformFixtureID(9957)
				mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,`+columns+`) SELECT ?,`+columns+` FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE installation_id=?)`, other, id)
				mustExec(t, s.st, `UPDATE app_platform_capability_grants SET review_plan_id=? WHERE installation_id=?`, other, id)

			case "review":
				// The original review projection must still bind its declined set.
				var plan string
				if err := s.st.queryRow(`SELECT review_plan_id FROM app_platform_operations WHERE installation_id=?`, id).Scan(&plan); err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json='{}' WHERE id=?`, plan)
			}
			before := dumpOf(t, s.st)
			if _, err := s.inspectPlatformLaunchAccess(reader, id); err == nil {
				t.Fatal("incoherent capability snapshot retained launch authority")
			}
			if _, err := s.readPlatformLaunchDocumentContext(context.Background(), reader, binding); err == nil {
				t.Fatal("previously returned binding bypassed changed capability state")
			}

			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("denied launch mutated storage")
			}
		})
	}
}
func TestPlatformCapabilitySnapshotSQLite(t *testing.T) {
	testPlatformCapabilitySnapshotRefusals(t, false)
}
func TestPlatformCapabilitySnapshotPostgres(t *testing.T) {
	testPlatformCapabilitySnapshotRefusals(t, true)
}

func testPlatformBackendCapabilitySnapshot(t *testing.T, pg bool) {
	for _, writable := range []bool{false, true} {
		name := "managed"
		if writable {
			name = "writable"
		}
		t.Run(name, func(t *testing.T) {
			var s *Server
			var actor platformActorSession
			var lease platformOperationLease
			if writable {
				s, actor, lease = platformWritableAccessFixture(t, pg)
			} else {
				s, actor, lease = platformManagedAccessFixture(t, pg)
			}
			read := func() error {
				if writable {
					a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
					if err == nil {
						a.Close()
					}
					return err
				}
				a, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID)
				if err == nil {
					a.Close()
				}
				return err
			}
			if err := read(); err != nil {
				t.Fatal("valid backend denied", err)
			}
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json='{"unexpected":true}' WHERE installation_id=?`, lease.installationID)
			// Some no-capability fixtures have no optional rows. Insert an undeclared
			// denied row bound to the actual review: it still cannot be interpreted as
			// a legitimate capability snapshot.
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=?`, lease.installationID) == 0 {
				mustExec(t, s.st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) SELECT installation_id,'reports.read',0,'{}',1,review_plan_id,created_ms,updated_ms FROM app_platform_operations WHERE id=?`, lease.id)
			}
			before := dumpOf(t, s.st)
			if err := read(); err == nil {
				t.Fatal("inconsistent capability policy reached backend access")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("failed capability verification mutated storage")
			}
		})
	}
}
func TestPlatformBackendCapabilitySnapshotSQLite(t *testing.T) {
	testPlatformBackendCapabilitySnapshot(t, false)
}
func TestPlatformBackendCapabilitySnapshotPostgres(t *testing.T) {
	testPlatformBackendCapabilitySnapshot(t, true)
}
