package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Metadata fixtures do not supply native success. An unavailable endpoint must
// leave the precise pending source and worker token intact on repeated attempts.
func testPlatformWritableInstallRunPending(t *testing.T, pg bool) {
	for _, stage := range []string{"writer", "start", "probe", "activate"} {
		t.Run(stage, func(t *testing.T) {
			var s *Server
			var lease platformOperationLease
			switch stage {
			case "writer":
				s, lease = platformWriterIntentFixture(t, pg)
				if _, err := s.preparePlatformInstallWriter(context.Background(), lease); err != nil {
					t.Fatal(err)
				}
			case "start":
				s, lease, _ = platformWritableStartFixture(t, pg)
				if _, err := s.preparePlatformWritableStart(context.Background(), lease); err != nil {
					t.Fatal(err)
				}
			case "probe":
				s, lease, _ = platformWritableProbeFixture(t, pg)
			case "activate":
				s, lease = platformWritableRegistrationFixture(t, pg)
				intent, err := s.preparePlatformWritableRegistration(context.Background(), lease)
				if err != nil {
					t.Fatal(err)
				}
				intent.registration.Close()
			}
			endpoint := platformRuntimeEndpoint{path: filepath.Join(t.TempDir(), "absent.sock"), controllerUID: 41001, controllerGID: 41001}
			before := dumpOf(t, s.st)
			for range 2 {
				result, err := s.runPlatformWritableInstall(context.Background(), lease, endpoint)
				if err == nil || result.lease != lease || result.activation.activationID != "" {
					t.Fatal("failed attempt lost progress or fabricated activation", err)
				}
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("unavailable endpoint replaced durable source")
			}
		})
	}
}

func TestPlatformWritableInstallRunPendingSQLite(t *testing.T) {
	testPlatformWritableInstallRunPending(t, false)
}
func TestPlatformWritableInstallRunPendingPostgres(t *testing.T) {
	testPlatformWritableInstallRunPending(t, true)
}

func testPlatformWritableInstallRunRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"cancel", "endpoint", "static", "readonly", "generation", "action", "stage", "lease", "sponsor", "runtime", "pause", "prepared_start_writer"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, _ := platformWritableStartFixture(t, pg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			endpoint := platformRuntimeEndpoint{path: filepath.Join(t.TempDir(), "absent.sock"), controllerUID: 41001, controllerGID: 41001}
			switch mode {
			case "cancel":
				cancel()
			case "endpoint":
				endpoint = platformRuntimeEndpoint{}
			case "static":
				lease.snapshot.Managed = false
			case "readonly":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=REPLACE(plan_json,?,?)`, platformWritableInstallFormat, platformManagedInstallFormat)
			case "generation":
				lease.snapshot.Generation++
			case "action":
				lease.snapshot.Action = appplatform.ActionUpdate
			case "stage":
				lease.snapshot.Stage = appplatform.StageComplete
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=(SELECT username FROM app_platform_review_plans)`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "prepared_start_writer":
				if _, err := s.preparePlatformWritableStart(ctx, lease); err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE stage='prepare_data' AND effect_number=2`)
			}
			before := dumpOf(t, s.st)
			result, err := s.runPlatformWritableInstall(ctx, lease, endpoint)
			if err == nil || result.lease != lease || result.activation.activationID != "" {
				t.Fatal("invalid source advanced", err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			if mode == "prepared_start_writer" && !errors.Is(err, errAppPlatformStageEvidence) {
				t.Fatal("start routing skipped writer verification", err)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused worker mutated storage")
			}
		})
	}
}

func TestPlatformWritableInstallRunRefusalsSQLite(t *testing.T) {
	testPlatformWritableInstallRunRefusals(t, false)
}
func TestPlatformWritableInstallRunRefusalsPostgres(t *testing.T) {
	testPlatformWritableInstallRunRefusals(t, true)
}
