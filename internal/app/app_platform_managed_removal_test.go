package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformManagedRemovalTestObservation struct {
	receipt    journal.ManagedRemovalReceipt
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformManagedRemovalTestObservation) ManagedRemovalReceipt() (journal.ManagedRemovalReceipt, error) {
	return o.receipt, nil
}
func (o platformManagedRemovalTestObservation) Controller() control.Authority { return o.authority }
func (o platformManagedRemovalTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformManagedRemovalTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformManagedRemovalTestObservation) CommandSHA256() string { return o.commandSHA }

func platformManagedRemovalStopFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	s, actor, install := platformManagedAccessFixture(t, pg)
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, install.installationID)
	plugin, err := ensurePlatformPluginTable(s.st.poolExec(), appID)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("clear on managed uninstall")
	platformTestInsert(t, s.st.poolExec(), strings.TrimPrefix(plugin.TableName, platformPrefix), map[string]any{
		"id": platformFixtureID(1132), "installation_id": install.installationID, "owner_username": actor.username,
		"owner_account_created": actor.created, "owner_account_generation": actor.generation, "collection": "note",
		"label": "managed", "media_type": "application/json", "content": content,
		"content_sha256": platformDigest(content), "size_bytes": len(content), "revision": 1,
		"idempotency_key": "managed-uninstall-clear", "created_ms": 1, "updated_ms": 1,
	})
	plan, err := s.createPlatformRemovalReview(actor, install.installationID, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, platformFixtureID(970)); err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformManagedUninstall(context.Background(), platformFixtureID(971))
	if err != nil || selected.lease.id == "" || selected.lease.snapshot.Action != appplatform.ActionUninstall || !selected.lease.snapshot.Managed {
		t.Fatal("managed uninstall claim", selected, err)
	}
	current := selected.lease
	for range 2 {
		current, err = s.st.advancePlatformRemovalControl(current)
		if err != nil {
			t.Fatalf("advance managed removal control at %s: %v", current.snapshot.Stage, err)
		}
	}
	if current.snapshot.Stage != appplatform.StageRevoke {
		t.Fatal("managed uninstall did not revoke before stop", current.snapshot)
	}
	current, err = s.advancePlatformManagedRemovalStage(context.Background(), current, appplatform.StageStop)
	if err != nil || current.snapshot.Stage != appplatform.StageStop {
		t.Fatalf("managed uninstall did not enter stop: %+v: %v", current.snapshot, err)
	}
	return s, actor, current
}

func platformManagedRemovalReceiptFixture(intent platformManagedRemovalIntent) journal.ManagedRemovalReceipt {
	now := time.Now().UTC().UnixMilli()
	state := "stopped"
	if intent.command.Stage == "remove_code" {
		state = "absent"
	}
	return journal.ManagedRemovalReceipt{
		Format:              "portal.runner.managed-removal-receipt/1-dev.1",
		DeploymentID:        intent.command.DeploymentID,
		ControllerEpoch:     intent.command.ControllerEpoch,
		InstallationID:      intent.command.InstallationID,
		OperationID:         intent.command.OperationID,
		SourceOperationID:   intent.command.Payload.SourceOperationID,
		SourceEffectID:      intent.command.Payload.SourceEffectID,
		TargetGeneration:    intent.command.TargetGeneration,
		Stage:               intent.command.Stage,
		EffectID:            intent.command.EffectID,
		PayloadSHA256:       intent.command.PayloadSHA256,
		SourceReceiptSHA256: intent.command.Payload.SourceReceiptSHA256,
		ResourceID:          intent.command.Payload.ResourceID,
		DataNamespaceID:     intent.command.Payload.DataNamespaceID,
		ReleaseSHA256:       intent.command.Payload.ReleaseSHA256,
		NativeID:            intent.nativeID,
		State:               state,
		ObservedMS:          strconv.FormatInt(now, 10),
		SettledMS:           strconv.FormatInt(now, 10),
	}
}

func platformManagedRemovalObservationFixture(intent platformManagedRemovalIntent, endpoint platformRuntimeEndpoint) platformManagedRemovalTestObservation {
	receipt := platformManagedRemovalReceiptFixture(intent)
	return platformManagedRemovalTestObservation{
		receipt: receipt, authority: intent.authority,
		endpoint:   transport.CandidateReceiptEndpoint{Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID, ControllerGID: endpoint.controllerGID, RootController: true},
		receivedAt: time.Now().UTC(), commandSHA: platformDigest(intent.raw),
	}
}

func platformManagedRemovalFixtureEndpoint() platformRuntimeEndpoint {
	return platformRuntimeEndpoint{path: "/run/portal-test/managed-removal.sock", controllerUID: 1000, controllerGID: 1001}
}

func platformWritableManagedRemovalStopFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, actor, install := platformWritableAccessFixture(t, pg)
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, install.installationID)
	if _, err := ensurePlatformPluginTable(s.st.poolExec(), appID); err != nil {
		t.Fatal(err)
	}
	plan, err := s.createPlatformRemovalReview(actor, install.installationID, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, platformFixtureID(1130)); err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformManagedUninstall(context.Background(), platformFixtureID(1131))
	if err != nil || selected.lease.id == "" {
		t.Fatal("writable managed uninstall claim", selected, err)
	}
	current := selected.lease
	for range 2 {
		current, err = s.st.advancePlatformRemovalControl(current)
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err = s.advancePlatformManagedRemovalStage(context.Background(), current, appplatform.StageStop)
	if err != nil || current.snapshot.Stage != appplatform.StageStop {
		t.Fatal("writable managed uninstall did not enter stop", current.snapshot, err)
	}
	return s, current
}

func testPlatformManagedRemovalSignsExactStopIntent(t *testing.T, pg bool) {
	s, _, lease := platformManagedRemovalStopFixture(t, pg)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if intent.command.Stage != "stop" || intent.command.Kind != "stop" || intent.command.OperationID != lease.id ||
		intent.command.TargetGeneration != "2" || intent.command.InstallationFence != strconv.FormatInt(lease.fence, 10) ||
		!platformValidUUID(intent.command.EffectID) || !platformValidSHA(intent.command.PayloadSHA256) ||
		len(intent.raw) == 0 || len(intent.signature) != 64 || len(intent.descriptor) == 0 ||
		intent.sourceOperationID == "" || intent.runtime.id == "" {
		t.Fatalf("invalid managed stop intent: %+v", intent)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='stop' AND effect_id=? AND kind='stop' AND state='prepared' AND receipt_json='{}'`, lease.id, intent.command.EffectID) != 1 {
		t.Fatal("managed stop intent not durable")
	}
	if scalar[int](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) < 2 {
		t.Fatal("managed namespace lost its admission fence")
	}
}

func TestPlatformManagedRemovalSignsExactStopIntentSQLite(t *testing.T) {
	testPlatformManagedRemovalSignsExactStopIntent(t, false)
}

func TestPlatformManagedRemovalSignsExactStopIntentPostgres(t *testing.T) {
	testPlatformManagedRemovalSignsExactStopIntent(t, true)
}

func TestPlatformManagedRemovalRefusesChangedRuntimeAndReplaysPreparedStage(t *testing.T) {
	s, _, lease := platformManagedRemovalStopFixture(t, false)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease); err == nil {
		t.Fatal("prepared stop intent was replaced")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
	if _, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease); err == nil {
		t.Fatal("disabled runtime signed managed stop")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE effect_id=?`, intent.command.EffectID) != 1 {
		t.Fatal("refusal duplicated removal effect")
	}
}

func testPlatformManagedRemovalSettlesStopAndRemoveCode(t *testing.T, pg bool) {
	s, _, lease := platformManagedRemovalStopFixture(t, pg)
	pluginTable := platformPluginDataTableName(scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, lease.installationID))
	if s.st.tableExists(pluginTable) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables`) != 0 {
		t.Fatal("managed uninstall admission retained plugin data")
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	stopIntent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	stopObservation := platformManagedRemovalObservationFixture(stopIntent, endpoint)
	stopReceipt, ok := validPlatformManagedRemovalObservation(stopObservation, stopIntent, endpoint)
	if !ok {
		t.Fatal("exact managed stop observation refused")
	}
	removeLease, err := s.settlePlatformManagedRemoval(context.Background(), lease, stopIntent, stopReceipt)
	if err != nil || removeLease.snapshot.Stage != appplatform.StageRemoveCode ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "remove_code" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='stop'`, lease.id) != "applied" {
		t.Fatal("managed stop settlement", removeLease.snapshot, err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != "active" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) == 0 {
		t.Fatal("stop settlement released retained namespace or authority early")
	}

	removeIntent, err := s.preparePlatformManagedRemovalIntent(context.Background(), removeLease)
	if err != nil {
		t.Fatal(err)
	}
	if removeIntent.command.Stage != "remove_code" || removeIntent.command.Kind != "remove-code" ||
		removeIntent.command.Payload.ResourceID != stopIntent.command.Payload.ResourceID ||
		removeIntent.command.Payload.SourceReceiptSHA256 != stopIntent.command.Payload.SourceReceiptSHA256 {
		t.Fatalf("changed remove-code identity: %+v", removeIntent.command)
	}
	removeObservation := platformManagedRemovalObservationFixture(removeIntent, endpoint)
	removeReceipt, ok := validPlatformManagedRemovalObservation(removeObservation, removeIntent, endpoint)
	if !ok {
		t.Fatal("exact managed remove-code observation refused")
	}
	completed, err := s.settlePlatformManagedRemoval(context.Background(), removeLease, removeIntent, removeReceipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("managed remove-code settlement", completed.snapshot, err)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "removed:removed" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != "retained" ||
		scalar[string](t, s.st, `SELECT stage||':'||COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`, lease.id) != "complete:" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND state='applied'`, lease.id) != 2 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=?`, lease.installationID) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'`, lease.installationID) != 0 ||
		s.st.tableExists(pluginTable) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables`) != 0 {
		t.Fatal("managed uninstall retained authority or plugin data")
	}
}

func TestPlatformManagedRemovalSettlesStopAndRemoveCodeSQLite(t *testing.T) {
	testPlatformManagedRemovalSettlesStopAndRemoveCode(t, false)
}
func TestPlatformManagedRemovalSettlesStopAndRemoveCodePostgres(t *testing.T) {
	testPlatformManagedRemovalSettlesStopAndRemoveCode(t, true)
}

func testPlatformWritableManagedRemoval(t *testing.T, pg bool) {
	s, lease := platformWritableManagedRemovalStopFixture(t, pg)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	sourcePlan := scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=(SELECT operation_id FROM app_platform_operation_effects WHERE operation_id IN (SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install') AND stage='start'))`, lease.installationID)
	var plan platformInstallProjection
	if json.Unmarshal([]byte(sourcePlan), &plan) != nil || plan.Format != platformWritableInstallFormat {
		t.Fatal("writable source plan lost format")
	}
	startReceipt := scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=(SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install') AND stage='start'`, lease.installationID)
	running, err := journal.ParseWritableCandidateRunningReceipt([]byte(startReceipt))
	if err != nil || intent.nativeID != running.NativeID ||
		intent.command.Payload.ResourceID != running.Claim.ResourceID ||
		intent.command.Payload.SourceEffectID != running.Claim.EffectID ||
		intent.command.Payload.SourceReceiptSHA256 != platformDigest([]byte(startReceipt)) ||
		intent.command.Payload.Platform != running.Claim.Preflight.Native.Platform {
		t.Fatal("writable managed stop intent", running, intent, err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	stopReceipt, ok := validPlatformManagedRemovalObservation(platformManagedRemovalObservationFixture(intent, endpoint), intent, endpoint)
	if !ok {
		t.Fatal("writable managed stop observation")
	}
	removeLease, err := s.settlePlatformManagedRemoval(context.Background(), lease, intent, stopReceipt)
	if err != nil || removeLease.snapshot.Stage != appplatform.StageRemoveCode {
		t.Fatal("writable managed stop settlement", removeLease.snapshot, err)
	}
	removeIntent, err := s.preparePlatformManagedRemovalIntent(context.Background(), removeLease)
	if err != nil || removeIntent.command.Kind != "remove-code" {
		t.Fatal("writable managed remove-code intent", err)
	}
	removeReceipt, ok := validPlatformManagedRemovalObservation(platformManagedRemovalObservationFixture(removeIntent, endpoint), removeIntent, endpoint)
	if !ok {
		t.Fatal("writable managed remove-code observation")
	}
	completed, err := s.settlePlatformManagedRemoval(context.Background(), removeLease, removeIntent, removeReceipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "removed:removed" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != "retained" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables`) != 0 {
		t.Fatal("writable managed uninstall completion", completed.snapshot, err)
	}
}

func TestPlatformWritableManagedRemovalSQLite(t *testing.T) {
	testPlatformWritableManagedRemoval(t, false)
}
func TestPlatformWritableManagedRemovalPostgres(t *testing.T) {
	testPlatformWritableManagedRemoval(t, true)
}

func TestPlatformManagedRemovalBindsEveryReceiptField(t *testing.T) {
	s, _, lease := platformManagedRemovalStopFixture(t, false)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	mutations := map[string]func(*journal.ManagedRemovalReceipt){
		"deployment":        func(v *journal.ManagedRemovalReceipt) { v.DeploymentID = platformFixtureID(1100) },
		"epoch":             func(v *journal.ManagedRemovalReceipt) { v.ControllerEpoch = platformFixtureID(1101) },
		"installation":      func(v *journal.ManagedRemovalReceipt) { v.InstallationID = platformFixtureID(1102) },
		"operation":         func(v *journal.ManagedRemovalReceipt) { v.OperationID = platformFixtureID(1103) },
		"source operation":  func(v *journal.ManagedRemovalReceipt) { v.SourceOperationID = platformFixtureID(1104) },
		"source effect":     func(v *journal.ManagedRemovalReceipt) { v.SourceEffectID = platformFixtureID(1105) },
		"target generation": func(v *journal.ManagedRemovalReceipt) { v.TargetGeneration = "3" },
		"stage":             func(v *journal.ManagedRemovalReceipt) { v.Stage, v.State = "remove_code", "absent" },
		"effect":            func(v *journal.ManagedRemovalReceipt) { v.EffectID = platformFixtureID(1106) },
		"payload":           func(v *journal.ManagedRemovalReceipt) { v.PayloadSHA256 = platformDigest([]byte("changed")) },
		"source receipt": func(v *journal.ManagedRemovalReceipt) {
			v.SourceReceiptSHA256 = platformDigest([]byte("changed source"))
		},
		"resource":  func(v *journal.ManagedRemovalReceipt) { v.ResourceID = platformFixtureID(1107) },
		"namespace": func(v *journal.ManagedRemovalReceipt) { v.DataNamespaceID = platformFixtureID(1108) },
		"release":   func(v *journal.ManagedRemovalReceipt) { v.ReleaseSHA256 = platformDigest([]byte("changed release")) },
		"native":    func(v *journal.ManagedRemovalReceipt) { v.NativeID = platformDigest([]byte("changed native")) },
		"before issue": func(v *journal.ManagedRemovalReceipt) {
			issued, _ := strconv.ParseInt(intent.command.IssuedMS, 10, 64)
			v.ObservedMS, v.SettledMS = strconv.FormatInt(issued-1, 10), strconv.FormatInt(issued-1, 10)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			observation := platformManagedRemovalObservationFixture(intent, endpoint)
			mutate(&observation.receipt)
			if _, ok := validPlatformManagedRemovalObservation(observation, intent, endpoint); ok {
				t.Fatal("changed receipt accepted")
			}
		})
	}
	observation := platformManagedRemovalObservationFixture(intent, endpoint)
	observation.authority.PublicKey = append([]byte(nil), observation.authority.PublicKey...)
	observation.authority.PublicKey[0] ^= 1
	if _, ok := validPlatformManagedRemovalObservation(observation, intent, endpoint); ok {
		t.Fatal("changed authority accepted")
	}
	observation = platformManagedRemovalObservationFixture(intent, endpoint)
	observation.endpoint.Path = "/run/portal-test/other.sock"
	if _, ok := validPlatformManagedRemovalObservation(observation, intent, endpoint); ok {
		t.Fatal("changed endpoint accepted")
	}
}

func testPlatformManagedRemovalPreparationRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*testing.T, *Server, platformOperationLease) error{
		"restore paused": func(t *testing.T, s *Server, _ platformOperationLease) error {
			mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			return errAppPlatformControlPaused
		},
		"runtime disabled": func(t *testing.T, s *Server, _ platformOperationLease) error {
			mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			return nil
		},
		"source history": func(t *testing.T, s *Server, _ platformOperationLease) error {
			mustExec(t, s.st, `UPDATE app_platform_operations SET stage='failed' WHERE action='install'`)
			return errAppPlatformStageEvidence
		},
		"source receipt": func(t *testing.T, s *Server, _ platformOperationLease) error {
			mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE stage='start'`)
			return errAppPlatformStageEvidence
		},
		"pending effect": func(t *testing.T, s *Server, lease platformOperationLease) error {
			mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
                VALUES(?, 'stop', 1, ?, 'stop', ?, (SELECT runtime_id FROM app_platform_installations WHERE id=?), ?, ?, 0, 'prepared', '{}', 1, 1)`,
				lease.id, platformFixtureID(1120), platformDigest([]byte("pending")), lease.installationID, lease.controllerEpoch, lease.fence)
			return errAppPlatformStageEvidence
		},
		"stale lease": func(t *testing.T, s *Server, lease platformOperationLease) error {
			mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0 WHERE id=?`, lease.id)
			return errAppPlatformLeaseConflict
		},
		"changed target": func(t *testing.T, s *Server, lease platformOperationLease) error {
			mustExec(t, s.st, `UPDATE app_platform_installations SET generation=generation+1 WHERE id=?`, lease.installationID)
			return errAppPlatformLeaseConflict
		},
		"changed plan": func(t *testing.T, s *Server, lease platformOperationLease) error {
			changed := platformDigest([]byte("changed plan"))
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, changed, lease.id)
			return errAppPlatformStageEvidence
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, lease := platformManagedRemovalStopFixture(t, pg)
			want := mutate(t, s, lease)
			before := dumpOf(t, s.st)
			_, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
			if err == nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("managed removal preparation: %v, want %v", err, want)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused managed removal preparation mutated state")
			}
		})
	}
}

func TestPlatformManagedRemovalPreparationRefusalsSQLite(t *testing.T) {
	testPlatformManagedRemovalPreparationRefusals(t, false)
}
func TestPlatformManagedRemovalPreparationRefusalsPostgres(t *testing.T) {
	testPlatformManagedRemovalPreparationRefusals(t, true)
}

func TestPlatformManagedRemovalRefusesInvalidSettlementInput(t *testing.T) {
	s, _, lease := platformManagedRemovalStopFixture(t, false)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	receipt := platformManagedRemovalReceiptFixture(intent)
	receipt.OperationID = platformFixtureID(1109)
	if _, err := s.settlePlatformManagedRemoval(context.Background(), lease, intent, receipt); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("changed receipt settlement: %v", err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE effect_id=?`, intent.command.EffectID) != "prepared" {
		t.Fatal("refused settlement changed durable effect")
	}
}

func testPlatformManagedRemovalCompletionRollback(t *testing.T, pg bool) {
	s, _, lease := platformManagedRemovalStopFixture(t, pg)
	endpoint := platformManagedRemovalFixtureEndpoint()
	stopIntent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	stopReceipt, ok := validPlatformManagedRemovalObservation(platformManagedRemovalObservationFixture(stopIntent, endpoint), stopIntent, endpoint)
	if !ok {
		t.Fatal("stop receipt")
	}
	removeLease, err := s.settlePlatformManagedRemoval(context.Background(), lease, stopIntent, stopReceipt)
	if err != nil {
		t.Fatal(err)
	}
	removeIntent, err := s.preparePlatformManagedRemovalIntent(context.Background(), removeLease)
	if err != nil {
		t.Fatal(err)
	}
	removeReceipt, ok := validPlatformManagedRemovalObservation(platformManagedRemovalObservationFixture(removeIntent, endpoint), removeIntent, endpoint)
	if !ok {
		t.Fatal("remove receipt")
	}
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_managed_removal_completion_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture managed removal completion failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_managed_removal_completion_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.operation_stage' AND NEW.target_id='`+lease.id+`') EXECUTE FUNCTION fixture_managed_removal_completion_failure()`)
		t.Cleanup(func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_managed_removal_completion_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_managed_removal_completion_failure()`)
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_managed_removal_completion_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.operation_stage' AND NEW.target_id='`+lease.id+`' BEGIN SELECT RAISE(ABORT,'fixture managed removal completion failure'); END`)
		t.Cleanup(func() { mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_managed_removal_completion_failure`) })
	}
	if _, err := s.settlePlatformManagedRemoval(context.Background(), removeLease, removeIntent, removeReceipt); err == nil {
		t.Fatal("managed removal completed without settlement audit")
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "remove_code" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='remove_code'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "removed:pending" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != "active" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) == 0 {
		t.Fatal("failed managed completion partially released authority or namespace")
	}
	if pg {
		mustExec(t, s.st, `DROP TRIGGER fixture_managed_removal_completion_failure ON audit_log`)
		mustExec(t, s.st, `DROP FUNCTION fixture_managed_removal_completion_failure()`)
	} else {
		mustExec(t, s.st, `DROP TRIGGER fixture_managed_removal_completion_failure`)
	}
	completed, err := s.settlePlatformManagedRemoval(context.Background(), removeLease, removeIntent, removeReceipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("managed completion retry", completed.snapshot, err)
	}
}

func TestPlatformManagedRemovalCompletionRollbackSQLite(t *testing.T) {
	testPlatformManagedRemovalCompletionRollback(t, false)
}
func TestPlatformManagedRemovalCompletionRollbackPostgres(t *testing.T) {
	testPlatformManagedRemovalCompletionRollback(t, true)
}

func TestPlatformManagedRemovalFailureRequiresRecoveryAndDoesNotRetry(t *testing.T) {
	s, _, lease := platformManagedRemovalStopFixture(t, false)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishPlatformManagedUninstallForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, lease.id) != "recovery_required:managed_uninstall_retry_exhausted" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "removed:recovery_required" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE effect_id=?`, intent.command.EffectID) != "prepared" {
		t.Fatalf("failed managed uninstall did not require recovery: %s/%s/%s",
			scalar[string](t, s.st, `SELECT stage||':'||COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`, lease.id),
			scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID),
			scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE effect_id=?`, intent.command.EffectID))
	}
	selected, err := s.claimNextPlatformManagedUninstall(context.Background(), platformFixtureID(1110))
	if err != nil || selected.lease.id != "" {
		t.Fatal("failed managed uninstall was automatically reclaimed", selected, err)
	}
}

func TestPlatformManagedRemovalFailureBeforePrepareStillRequiresRecovery(t *testing.T) {
	s, _, lease := platformManagedRemovalStopFixture(t, false)
	if err := s.finishPlatformManagedUninstallForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, lease.id) != "recovery_required:managed_uninstall_retry_exhausted" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "removed:recovery_required" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 0 {
		t.Fatal("pre-dispatch managed uninstall failure did not require recovery")
	}
}

func TestPlatformManagedRemovalPreparedCrashIsClaimableOnlyForRecovery(t *testing.T) {
	s, _, lease := platformManagedRemovalStopFixture(t, false)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0 WHERE id=?`, lease.id)
	reclaimed, err := s.claimNextPlatformManagedUninstall(context.Background(), platformFixtureID(1140))
	if err != nil || reclaimed.lease.id != lease.id || reclaimed.lease.snapshot.Stage != appplatform.StageStop {
		t.Fatal("prepared managed uninstall crash was stranded", reclaimed, err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE effect_id=?`, intent.command.EffectID) != "prepared" {
		t.Fatal("recovery claim changed the prepared effect")
	}
	if _, err := s.runPlatformManagedUninstall(context.Background(), reclaimed.lease, platformManagedRemovalFixtureEndpoint()); err == nil {
		t.Fatal("recovery claim reissued native managed removal")
	}
	if err := s.finishPlatformManagedUninstallForReview(context.Background(), reclaimed.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, lease.id) != "recovery_required:managed_uninstall_retry_exhausted" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "removed:recovery_required" {
		t.Fatal("reclaimed crash did not require recovery")
	}
}
