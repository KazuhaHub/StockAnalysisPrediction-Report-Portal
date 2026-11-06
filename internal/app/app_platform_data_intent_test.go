package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"strconv"
	"testing"
	"time"
)

func platformDataIntentFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	s, session, lease := platformInstallStageFixture(t, pg)
	runtime, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmPlatformObservedRuntime(session, runtime, runtimeObservationFixture(runtime)); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage, appplatform.StagePrepareData} {
		lease, err = s.st.advancePlatformInstallStaging(lease)
		if err != nil || lease.snapshot.Stage != stage {
			t.Fatal(err)
		}
	}
	return s, session, lease
}
func testPlatformDataIntent(t *testing.T, pg bool) {
	s, _, lease := platformDataIntentFixture(t, pg)
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.Verify(intent.raw, intent.signature, intent.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	c := verified.Command()
	if c.OperationID != lease.id || c.InstallationID != lease.installationID || c.Payload.DataNamespaceID != intent.namespaceID || c.EffectID != intent.effectID || c.Payload.QuotaBytes != "268435456" || c.Payload.QuotaInodes != "65536" {
		t.Fatal("signed intent lost exact installation identity")
	}
	expires, _ := strconv.ParseInt(c.ExpiresMS, 10, 64)
	if expires > lease.leaseUntilMS-1000 {
		t.Fatal("signed authority outlives conservative lease bound")
	}
	again, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil || again.namespaceID != intent.namespaceID || again.effectID != intent.effectID || again.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		t.Fatal("retry replaced stable data intent", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE state='review_required' AND data_format=0 AND writer_fence=0 AND revision=1`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE state='prepared' AND receipt_json='{}' AND observed_fence=0`) != 1 {
		t.Fatal("intent fabricated native completion")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE runtime_id IS NOT NULL`) != 0 {
		t.Fatal("data service became application backend")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_intent' AND target_id=?`, lease.id) != 1 {
		t.Fatal("missing or duplicate intent audit")
	}
}
func TestPlatformDataIntentSQLite(t *testing.T)   { testPlatformDataIntent(t, false) }
func TestPlatformDataIntentPostgres(t *testing.T) { testPlatformDataIntent(t, true) }

func testPlatformDataIntentRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"disabled_runtime", "bad_key", "namespace", "effect", "sponsor", "paused", "lease", "takeover"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := platformDataIntentFixture(t, pg)
			if _, err := s.preparePlatformInstallData(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "disabled_runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "bad_key":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key='sealed:invalid'`)
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET data_format=1`)
			case "effect":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown'`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=(SELECT username FROM app_platform_review_plans)`)
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "takeover":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
				var err error
				lease, err = s.st.claimPlatformOperation(lease.id, platformFixtureID(990))
				if err != nil {
					t.Fatal(err)
				}
			}
			before := dumpOf(t, s.st)
			result, err := s.preparePlatformInstallData(context.Background(), lease)
			if err == nil || len(result.raw) != 0 || len(result.signature) != 0 || result.namespaceID != "" {
				t.Fatal("invalid authority emitted signed intent", kind, err)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused intent modified state", kind)
			}
		})
	}
}
func TestPlatformDataIntentRefusalsSQLite(t *testing.T)   { testPlatformDataIntentRefusals(t, false) }
func TestPlatformDataIntentRefusalsPostgres(t *testing.T) { testPlatformDataIntentRefusals(t, true) }
func TestPlatformDataIntentFailureAtomicSQLite(t *testing.T) {
	s, _, lease := platformDataIntentFixture(t, false)
	mustExec(t, s.st, `CREATE TRIGGER fixture_intent_failure BEFORE INSERT ON app_platform_operation_effects BEGIN SELECT RAISE(ABORT,'fixture intent failure');END`)
	result, err := s.preparePlatformInstallData(context.Background(), lease)
	if err == nil || len(result.raw) != 0 {
		t.Fatal("partial intent returned signature")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_intent'`) != 0 {
		t.Fatal("failed effect insert left namespace/audit")
	}
}
func TestPlatformDataContextDatabaseWaitSQLite(t *testing.T) {
	for _, phase := range []string{"intent", "receipt"} {
		t.Run(phase, func(t *testing.T) {
			s, _, lease := platformDataIntentFixture(t, false)
			intent, err := s.preparePlatformInstallData(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
			proof := platformDataObservationFixture(t, intent, endpoint)
			before := dumpOf(t, s.st)
			s.st.db.SetMaxOpenConns(1)
			held, err := s.st.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if phase == "intent" {
				_, err = s.preparePlatformInstallData(ctx, lease)
			} else {
				_, err = s.commitPlatformObservedData(ctx, lease, intent, endpoint, proof)
			}
			cancel()
			held.Close()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("database wait ignored context", err)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("canceled database wait committed")
			}
		})
	}
}
