//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func updateStartJournalFixture(t *testing.T) (checkpointFixture, CheckpointReceipt, control.UpdateStartCommand, []byte, []byte) {
	t.Helper()
	f, checkpoint := appliedCheckpointFixture(t)
	checkpointRaw, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	command := control.UpdateStartCommand{
		Format: control.UpdateStartFormat, DeploymentID: f.command.DeploymentID,
		ControllerEpoch: f.command.ControllerEpoch, ControlRevision: "3",
		InstallationID: f.command.InstallationID, OperationID: f.command.OperationID,
		TargetGeneration: f.command.TargetGeneration, Stage: "start", EffectNumber: "1",
		EffectID: fixtureID(895), Kind: "update-start", InstallationFence: "2",
		Payload: control.UpdateStartPayload{
			ResourceID: fixtureID(896), NamespaceID: checkpoint.NamespaceID,
			SourceOperationID:             checkpoint.SourceOperationID,
			SourceStartEffectID:           checkpoint.SourceStartEffectID,
			SourceStartReceiptSHA256:      checkpoint.SourceReceiptSHA256,
			SourceDataEffectID:            checkpoint.SourceDataEffectID,
			SourceDataReceiptSHA256:       checkpoint.SourceDataReceiptSHA256,
			SourceCheckpointOperationID:   checkpoint.OperationID,
			SourceCheckpointEffectID:      checkpoint.EffectID,
			SourceCheckpointReceiptSHA256: candidateHash(checkpointRaw),
			CheckpointID:                  checkpoint.CheckpointID,
			CheckpointManifestSHA256:      checkpoint.ManifestSHA256,
			CheckpointArchiveBytes:        checkpoint.ArchiveBytes,
			SourceReleaseSHA256:           checkpoint.ReleaseSHA256,
			TargetReleaseSHA256:           strings.Repeat("c", 64),
			SourceGeneration:              "1", TargetPolicyRevision: "2",
			ProjectID: checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode,
			Device: checkpoint.Device, QuotaBytes: f.data.QuotaBytes,
			QuotaInodes:        f.data.QuotaInodes,
			RootFilesystemUUID: f.source.Claim.Preflight.RootFilesystemUUID,
			RootInode:          f.source.Claim.Preflight.RootInode,
			DataFormat:         checkpoint.DataFormat, SourceWriterFence: checkpoint.WriterFence,
			TargetWriterFence: "2", Platform: f.source.Claim.Preflight.Platform,
			SandboxProfile: control.CandidateSandboxProfile,
		},
	}
	command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
	raw, signature, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return f, checkpoint, command, raw, signature
}

func TestUpdateStartReservesExactTargetResource(t *testing.T) {
	f, checkpoint, command, raw, signature := updateStartJournalFixture(t)
	reservation, err := f.store.ReserveUpdateStart(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.ResourceID != command.Payload.ResourceID || reservation.EffectID != command.EffectID ||
		reservation.TargetGeneration != 2 || reservation.TargetWriterFence != 2 ||
		reservation.Replayed {
		t.Fatal("update-start reservation", reservation)
	}
	var state, native, generation, fence, writer, desired, observed string
	if err := f.store.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&state); err != nil ||
		state != "prepared" {
		t.Fatal("update-start command", state, err)
	}
	if err := f.store.db.QueryRow(`SELECT native_id,generation,control_fence,writer_fence,desired_state,observed_state FROM runner_resources WHERE id=?`,
		command.Payload.ResourceID).Scan(&native, &generation, &fence, &writer, &desired, &observed); err != nil ||
		native != updateStartPendingPrefix+command.Payload.ResourceID || generation != "2" ||
		fence != command.InstallationFence || writer != "2" || desired != "present" || observed != "unknown" {
		t.Fatal("update-start resource", native, generation, fence, writer, desired, observed, err)
	}
	replay, err := f.store.ReserveUpdateStart(context.Background(), raw, signature)
	if err != nil || !replay.Replayed || replay.ResourceID != reservation.ResourceID {
		t.Fatal("update-start replay", replay, err)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container' AND (id=? OR data_namespace_id=?)`, command.Payload.ResourceID, checkpoint.NamespaceID) != 2 {
		t.Fatal("update-start did not retain source plus target resource")
	}
}

func TestUpdateStartReservesAfterRemovedSourceCode(t *testing.T) {
	fixture, checkpoint, command, raw, signature :=
		updateStartJournalFixture(t)
	now := time.Now().UTC().UnixMilli()
	if _, err := fixture.store.db.Exec(`UPDATE runner_resources
		SET desired_state='absent',observed_state='absent',revision=4,
		observed_ms=?,updated_ms=? WHERE id=?`,
		now, now, checkpoint.ResourceID); err != nil {
		t.Fatal(err)
	}
	reservation, err := fixture.store.ReserveUpdateStart(
		context.Background(), raw, signature)
	if err != nil || reservation.ResourceID != command.Payload.ResourceID ||
		reservation.TargetGeneration != 2 {
		t.Fatal("removed source update-start reservation", reservation, err)
	}
}

func TestRestoreStartReservesFromSeparateCheckpointOperation(t *testing.T) {
	f, _, command, _, _ := updateStartJournalFixture(t)
	command.Kind = "restore-start"
	command.OperationID = fixtureID(1534)
	command.Payload.TargetReleaseSHA256 =
		command.Payload.SourceReleaseSHA256
	var err error
	command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	command.IssuedMS = strconv.FormatInt(now, 10)
	command.ExpiresMS = strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{
		DeploymentID:    enrollment.DeploymentID,
		ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision,
		PublicKey:       enrollment.PublicKey, Fingerprint: enrollment.Fingerprint,
	}
	raw, signature, err := control.SignUpdateStart(
		command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
		authority, now)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := f.store.ReserveUpdateStart(
		context.Background(), raw, signature)
	if err != nil || reservation.TargetReleaseSHA256 !=
		command.Payload.SourceReleaseSHA256 {
		t.Fatal("restore-start reservation", reservation, err)
	}
}

func TestRestoreStartReusesRemovedActivationResource(t *testing.T) {
	f, _, command, _, _ := updateStartJournalFixture(t)
	command.Kind = "restore-start"
	command.OperationID = fixtureID(1534)
	command.Payload.TargetReleaseSHA256 =
		command.Payload.SourceReleaseSHA256
	var err error
	command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	command.IssuedMS = strconv.FormatInt(now, 10)
	command.ExpiresMS = strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{
		DeploymentID:    enrollment.DeploymentID,
		ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision,
		PublicKey:       enrollment.PublicKey, Fingerprint: enrollment.Fingerprint,
	}
	raw, signature, err := control.SignUpdateStart(
		command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
		authority, now)
	if err != nil {
		t.Fatal(err)
	}
	mustJournalExec(t, f.store.db, `INSERT INTO runner_commands(
		effect_id,operation_id,installation_id,stage,effect_number,kind,
		payload_sha256,target_generation,controller_epoch,admitted_fence,
		state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,'remove_code',1,'remove-code',?,?,?,?,'applied',
		'{}',NULL,?,?)`,
		fixtureID(1535), command.OperationID, command.InstallationID,
		strings.Repeat("f", 64), mustDataInt(command.TargetGeneration),
		command.ControllerEpoch, command.InstallationFence, now-1, now-1)
	mustJournalExec(t, f.store.db, `INSERT INTO runner_resources(
		id,installation_id,data_namespace_id,kind,native_id,release_sha256,
		generation,controller_epoch,control_fence,writer_fence,desired_state,
		observed_state,ownership_sha256,revision,observed_ms,created_ms,
		updated_ms)
		VALUES(?,?,?,'container',?,?,?,?,?,?,'absent','absent',?,4,?,?,?)`,
		command.Payload.ResourceID, command.InstallationID,
		command.Payload.NamespaceID, updateStartPendingPrefix+
			command.Payload.ResourceID, strings.Repeat("d", 64),
		mustDataInt(command.TargetGeneration), command.ControllerEpoch,
		command.InstallationFence, command.Payload.TargetWriterFence,
		strings.Repeat("e", 64), now-1, now-1, now-1)
	reservation, err := f.store.ReserveUpdateStart(
		context.Background(), raw, signature)
	if err != nil || reservation.ResourceID != command.Payload.ResourceID ||
		reservation.Replayed {
		t.Fatal("removed activation reuse", reservation, err)
	}
	var desired, observed, release, native string
	var revision int64
	if err := f.store.db.QueryRow(`SELECT desired_state,observed_state,
		release_sha256,native_id,revision FROM runner_resources WHERE id=?`,
		command.Payload.ResourceID).Scan(
		&desired, &observed, &release, &native, &revision); err != nil ||
		desired != "present" || observed != "unknown" ||
		release != command.Payload.TargetReleaseSHA256 ||
		native != updateStartPendingPrefix+command.Payload.ResourceID ||
		revision != 1 {
		t.Fatal("reused resource state", desired, observed, release,
			native, revision, err)
	}
}

func TestUpdateStartRefusesChangedCheckpointAndSources(t *testing.T) {
	for _, mode := range []string{"checkpoint state", "running source", "source receipt", "resource alias"} {
		t.Run(mode, func(t *testing.T) {
			f, checkpoint, command, raw, signature := updateStartJournalFixture(t)
			switch mode {
			case "checkpoint state":
				mustJournalExec(t, f.store.db, `UPDATE runner_checkpoints SET state='review_required' WHERE id=?`, checkpoint.CheckpointID)
			case "running source":
				mustJournalExec(t, f.store.db, `UPDATE runner_resources SET observed_state='running' WHERE id=?`, checkpoint.ResourceID)
			case "source receipt":
				mustJournalExec(t, f.store.db, `UPDATE runner_commands SET receipt_json='{}' WHERE effect_id=?`, checkpoint.EffectID)
			case "resource alias":
				changed := command
				changed.Payload.ResourceID = checkpoint.ResourceID
				digest, err := control.UpdateStartLogicalDigest(changed)
				if err != nil {
					t.Fatal(err)
				}
				changed.PayloadSHA256 = digest
				now := time.Now().UnixMilli()
				changed.IssuedMS, changed.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
				enrollment := enrollmentFixture(30)
				authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
				raw, signature, err = control.SignUpdateStart(changed, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.ReserveUpdateStart(context.Background(), raw, signature); err == nil {
				t.Fatal("changed update-start source admitted")
			}
			if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, command.EffectID) != 0 {
				t.Fatal("refused update-start wrote command")
			}
		})
	}
}

func TestUpdateStartReservationRollback(t *testing.T) {
	f, _, command, raw, signature := updateStartJournalFixture(t)
	if _, err := f.store.db.Exec(`CREATE TRIGGER fixture_update_start_resource BEFORE INSERT ON runner_resources BEGIN SELECT RAISE(ABORT,'fixture update start'); END`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.store.db.Exec(`DROP TRIGGER fixture_update_start_resource`)
	}()
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, signature); err == nil {
		t.Fatal("failed update-start resource insert committed")
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, command.EffectID) != 0 ||
		scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_resources WHERE id=?`, command.Payload.ResourceID) != 0 {
		t.Fatal("failed update-start left partial rows")
	}
}

func TestReadUpdateStartReservation(t *testing.T) {
	f, _, command, raw, signature := updateStartJournalFixture(t)
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ReadUpdateStartReservation(context.Background(), raw, signature)
	if err != nil || got.ResourceID != command.Payload.ResourceID || !got.Replayed {
		t.Fatal("read update-start reservation", got, err)
	}
	if _, err := f.store.ReserveUpdateStart(context.Background(), bytes.Repeat([]byte("x"), 32), signature); !errors.Is(err, control.ErrInvalidCommand) {
		t.Fatal("malformed update-start accepted", err)
	}
}

func TestUpdateStartClaimsExactGenerationBeforeNativeExecution(t *testing.T) {
	f, _, command, raw, signature := updateStartJournalFixture(t)
	if _, err := f.store.ClaimUpdateStart(context.Background(), raw, signature); err == nil {
		t.Fatal("update-start claimed without a reserved target")
	}
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	claim, err := f.store.ClaimUpdateStart(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	var state, markerRaw string
	if err := f.store.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`,
		command.EffectID).Scan(&state, &markerRaw); err != nil || state != "unknown" {
		t.Fatal("claimed update-start state", state, err)
	}
	var marker UpdateStartClaimMarker
	if !canonicalDataJSON(markerRaw, &marker) || marker.Preflight != nil ||
		marker.ResourceID != command.Payload.ResourceID ||
		marker.EffectID != command.EffectID ||
		marker.CheckpointReceiptSHA256 != command.Payload.SourceCheckpointReceiptSHA256 ||
		marker.AdmittedFence != command.InstallationFence {
		t.Fatal("update-start claim marker", marker)
	}
	tx, facts, err := f.store.beginUpdateStartExecution(context.Background(), raw, signature, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	request, err := updateStartNativeRequest(facts, marker, nil)
	if err != nil || request.Identity.Generation != "2" ||
		request.Identity.WriterFence != "2" ||
		request.Identity.ReleaseSHA256 != command.Payload.TargetReleaseSHA256 ||
		request.Binding.EffectID != command.EffectID ||
		request.Binding.CheckpointReceiptSHA256 != command.Payload.SourceCheckpointReceiptSHA256 {
		tx.Rollback()
		t.Fatal("update-start native request", request, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimUpdateStart(context.Background(), raw, signature); err == nil {
		t.Fatal("update-start claim was reused")
	}
	if _, err := json.Marshal(claim); err == nil {
		t.Fatal("live update-start claim serialized")
	}
}

func TestCheckpointSourceRootUsesEachSourceReceiptShape(t *testing.T) {
	const filesystem, inode = "00000000-0000-4000-8000-000000001234", "42"
	candidate := checkpointSource{}
	candidate.source.Claim.Preflight = &candidatePreflightEvidence{
		RootFilesystemUUID: filesystem,
		RootInode:          inode,
	}
	writable := checkpointSource{}
	writable.writable = &WritableCandidateRunningReceipt{
		Claim: writableCandidateClaimMarker{
			Preflight: &writableCandidatePreflightEvidence{
				Native: candidatePreflightEvidence{
					RootFilesystemUUID: filesystem,
					RootInode:          inode,
				},
			},
		},
	}
	update := checkpointSource{}
	update.update = &UpdateStartRunningReceipt{
		Claim: UpdateStartClaimMarker{
			Preflight: &UpdateStartPreflightEvidence{
				RootFilesystemUUID: filesystem,
				RootInode:          inode,
			},
		},
	}
	for name, source := range map[string]checkpointSource{
		"candidate": candidate,
		"writable":  writable,
		"update":    update,
	} {
		t.Run(name, func(t *testing.T) {
			gotFilesystem, gotInode, ok := checkpointSourceRoot(source)
			if !ok || gotFilesystem != filesystem || gotInode != inode {
				t.Fatal("checkpoint source root", gotFilesystem, gotInode, ok)
			}
		})
	}
	if _, _, ok := checkpointSourceRoot(checkpointSource{}); ok {
		t.Fatal("empty checkpoint source exposed a root")
	}
}

func TestUpdateStartExecutionClaimIsSingleUseAndUncertain(t *testing.T) {
	f, _, command, raw, signature := updateStartJournalFixture(t)
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	claim, err := f.store.ClaimUpdateStart(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	invocations := 0
	attempt, err := f.store.RunUpdateStartOnce(context.Background(), claim, func(context.Context) error {
		invocations++
		return errors.New("fixture native uncertainty")
	})
	if !attempt.Invoked || invocations != 1 || !errors.Is(err, ErrUpdateStartExecutionUncertain) {
		t.Fatal("first update-start invocation", attempt, invocations, err)
	}
	if attempt, err := f.store.RunUpdateStartOnce(context.Background(), claim, func(context.Context) error {
		invocations++
		return nil
	}); attempt.Invoked || invocations != 1 || err == nil {
		t.Fatal("update-start uncertainty was replayed", attempt, invocations, err)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=? AND state='unknown'`, command.EffectID) != 1 {
		t.Fatal("update-start uncertainty was not retained")
	}
}

func TestUpdateStartRecoveryUsesRenewedEnvelope(t *testing.T) {
	f, _, command, raw, signature := updateStartJournalFixture(t)
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimUpdateStart(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	renewed := command
	now := time.Now().UnixMilli()
	renewed.IssuedMS, renewed.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
	renewedRaw, renewedSignature, err := control.SignUpdateStart(renewed, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	tx, facts, err := f.store.beginUpdateStartExecution(context.Background(), renewedRaw, renewedSignature, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if facts.command.IssuedMS != renewed.IssuedMS || facts.command.ExpiresMS != renewed.ExpiresMS {
		t.Fatal("renewed update-start envelope was not accepted")
	}
}

func TestUpdateStartRunningReceiptBindsExactGeneration(t *testing.T) {
	_, _, command, _, _ := updateStartJournalFixture(t)
	preflight := UpdateStartPreflightEvidence{
		Format:       "portal.runner.update-start-preflight/1-dev.1",
		ImageID:      "sha256:" + strings.Repeat("a", 64),
		Platform:     command.Payload.Platform,
		PolicySHA256: strings.Repeat("b", 64), MountsSHA256: strings.Repeat("c", 64),
		RootFilesystemUUID: command.Payload.RootFilesystemUUID,
		RootInode:          command.Payload.RootInode, ProjectID: command.Payload.ProjectID,
		Device: command.Payload.Device, NamespaceInode: command.Payload.NamespaceInode,
		QuotaBytes: command.Payload.QuotaBytes, QuotaInodes: command.Payload.QuotaInodes,
		SeccompSHA256: driver.UpdateStartSeccompSHA256(command.Payload.SandboxProfile),
	}
	if !validUpdateStartPreflight(preflight) {
		t.Fatal("update-start preflight fixture invalid")
	}
	marker := UpdateStartClaimMarker{
		Format:   "portal.runner.update-start-claim/1-dev.1",
		ClaimID:  "70000000-0000-4000-8000-000000001501",
		EffectID: command.EffectID, ResourceID: command.Payload.ResourceID,
		PayloadSHA256: command.PayloadSHA256, OwnershipSHA256: strings.Repeat("d", 64),
		CheckpointReceiptSHA256: command.Payload.SourceCheckpointReceiptSHA256,
		PolicyRevision:          command.Payload.TargetPolicyRevision,
		ControlRevision:         command.ControlRevision, AdmittedFence: command.InstallationFence,
		SandboxProfile: command.Payload.SandboxProfile,
		ClaimedMS:      command.IssuedMS, ExpiresMS: command.ExpiresMS,
		Preflight: &preflight, PreflightBoundMS: command.IssuedMS,
	}
	if !validUpdateStartClaimMarker(marker) {
		t.Fatal("update-start claim fixture invalid")
	}
	receipt := UpdateStartRunningReceipt{
		Format: updateStartRunningFormat, Claim: marker,
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID,
		NamespaceID:         command.Payload.NamespaceID,
		SourceReleaseSHA256: command.Payload.SourceReleaseSHA256,
		TargetReleaseSHA256: command.Payload.TargetReleaseSHA256,
		SourceGeneration:    command.Payload.SourceGeneration,
		TargetGeneration:    command.TargetGeneration, ControlFence: command.InstallationFence,
		SourceWriterFence: command.Payload.SourceWriterFence,
		TargetWriterFence: command.Payload.TargetWriterFence,
		DataFormat:        command.Payload.DataFormat, NativeID: strings.Repeat("e", 64),
		NativeObservedMS: command.IssuedMS, SettledMS: command.IssuedMS,
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseUpdateStartRunningReceipt(raw)
	if err != nil || parsed.TargetGeneration != "2" || parsed.Claim.Preflight == nil {
		t.Fatal("update-start running receipt", parsed, err)
	}
}

func TestUpdateStartReadinessBindsRunningReceipt(t *testing.T) {
	readiness := UpdateStartReadiness{
		Format: updateStartReadinessFormat, PayloadSHA256: strings.Repeat("a", 64),
		RunningReceiptSHA256: strings.Repeat("b", 64), NativeID: strings.Repeat("c", 64),
		Protocol: "portal.backend/1", DataFormat: "1",
		ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	raw, err := json.Marshal(readiness)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := ParseUpdateStartReadiness(raw); err != nil || parsed != readiness {
		t.Fatal("update-start readiness", parsed, err)
	}
	for name, mutate := range map[string]func(*UpdateStartReadiness){
		"payload":  func(r *UpdateStartReadiness) { r.PayloadSHA256 = "bad" },
		"receipt":  func(r *UpdateStartReadiness) { r.RunningReceiptSHA256 = "bad" },
		"native":   func(r *UpdateStartReadiness) { r.NativeID = "bad" },
		"protocol": func(r *UpdateStartReadiness) { r.Protocol = "portal.backend/2" },
		"format":   func(r *UpdateStartReadiness) { r.DataFormat = "0" },
		"observed": func(r *UpdateStartReadiness) { r.ObservedMS = "01" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := readiness
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if _, err := ParseUpdateStartReadiness(encoded); err == nil {
				t.Fatal("changed readiness accepted")
			}
		})
	}
}

func seedUpdateStartRunningFixture(t *testing.T, f checkpointFixture, command control.UpdateStartCommand, startRaw, startSignature []byte) []byte {
	t.Helper()
	reservation, err := f.store.ReserveUpdateStart(
		context.Background(), startRaw, startSignature)
	if err != nil {
		t.Fatal(err)
	}
	preflight := UpdateStartPreflightEvidence{
		Format:       "portal.runner.update-start-preflight/1-dev.1",
		ImageID:      "sha256:" + strings.Repeat("a", 64),
		Platform:     command.Payload.Platform,
		PolicySHA256: strings.Repeat("b", 64), MountsSHA256: strings.Repeat("c", 64),
		RootFilesystemUUID: command.Payload.RootFilesystemUUID,
		RootInode:          command.Payload.RootInode, ProjectID: command.Payload.ProjectID,
		Device: command.Payload.Device, NamespaceInode: command.Payload.NamespaceInode,
		QuotaBytes: command.Payload.QuotaBytes, QuotaInodes: command.Payload.QuotaInodes,
		SeccompSHA256: driver.UpdateStartSeccompSHA256(command.Payload.SandboxProfile),
	}
	observed := time.Now().UTC().UnixMilli()
	marker := UpdateStartClaimMarker{
		Format:  "portal.runner.update-start-claim/1-dev.1",
		ClaimID: fixtureID(1530), EffectID: command.EffectID,
		ResourceID: command.Payload.ResourceID, PayloadSHA256: command.PayloadSHA256,
		OwnershipSHA256:         reservation.OwnershipSHA256,
		CheckpointReceiptSHA256: command.Payload.SourceCheckpointReceiptSHA256,
		PolicyRevision:          command.Payload.TargetPolicyRevision,
		ControlRevision:         command.ControlRevision,
		AdmittedFence:           command.InstallationFence,
		SandboxProfile:          command.Payload.SandboxProfile,
		ClaimedMS:               strconv.FormatInt(observed, 10), ExpiresMS: command.ExpiresMS,
		Preflight:        &preflight,
		PreflightBoundMS: strconv.FormatInt(observed, 10),
	}
	running := UpdateStartRunningReceipt{
		Format: updateStartRunningFormat, Claim: marker,
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID,
		NamespaceID:         command.Payload.NamespaceID,
		SourceReleaseSHA256: command.Payload.SourceReleaseSHA256,
		TargetReleaseSHA256: command.Payload.TargetReleaseSHA256,
		SourceGeneration:    command.Payload.SourceGeneration,
		TargetGeneration:    command.TargetGeneration,
		ControlFence:        command.InstallationFence,
		SourceWriterFence:   command.Payload.SourceWriterFence,
		TargetWriterFence:   command.Payload.TargetWriterFence,
		DataFormat:          command.Payload.DataFormat,
		NativeID:            strings.Repeat("e", 64),
		NativeObservedMS:    strconv.FormatInt(observed, 10),
		SettledMS:           strconv.FormatInt(observed, 10),
	}
	raw, err := json.Marshal(running)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`,
		string(raw), observed, command.EffectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE runner_resources SET native_id=?,release_sha256=?,generation=?,control_fence=?,writer_fence=?,desired_state='present',observed_state='running',ownership_sha256=?,revision=2,observed_ms=?,updated_ms=? WHERE id=?`,
		running.NativeID, command.Payload.TargetReleaseSHA256, 2,
		command.InstallationFence, command.Payload.TargetWriterFence,
		reservation.OwnershipSHA256, observed, observed, command.Payload.ResourceID); err != nil {
		t.Fatal(err)
	}
	return raw
}

func updateStartRegistrationCommandFixture(t *testing.T, command control.UpdateStartCommand, startRaw, startSignature, runningRaw []byte) (control.UpdateStartRegistrationCommand, []byte, []byte) {
	t.Helper()
	registration := control.UpdateStartRegistrationCommand{
		Format: control.UpdateStartRegistrationFormat, EffectID: fixtureID(1531),
		StartRaw: startRaw, StartSignature: startSignature,
		RunningReceiptSHA256: candidateHash(runningRaw),
		DocumentSHA256:       strings.Repeat("f", 64),
	}
	var err error
	registration.PayloadSHA256, err = control.UpdateStartRegistrationLogicalDigest(registration)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := enrollmentFixture(30)
	authority := control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
	raw, signature, err := control.SignUpdateStartRegistration(
		registration, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
		authority, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return registration, raw, signature
}

func TestUpdateStartRegistrationAdmissionBindsAppliedTarget(t *testing.T) {
	f, _, command, startRaw, startSignature := updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	registration, raw, signature := updateStartRegistrationCommandFixture(
		t, command, startRaw, startSignature, runningRaw)
	admitted, err := f.store.AdmitUpdateStartRegistrationCommand(
		context.Background(), raw, signature)
	if err != nil || admitted.State != "prepared" ||
		admitted.EffectID != registration.EffectID {
		t.Fatal("update-start registration admission", admitted, err)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=? AND stage='activate' AND effect_number=1 AND kind='activate' AND state='prepared' AND payload_sha256=? AND target_generation=2`,
		registration.EffectID, registration.PayloadSHA256) != 1 {
		t.Fatal("registration intent not persisted exactly")
	}
	replay, err := f.store.AdmitUpdateStartRegistrationCommand(
		context.Background(), raw, signature)
	if err != nil || !replay.Replayed || replay.EffectID != admitted.EffectID {
		t.Fatal("registration replay", replay, err)
	}
}

func TestUpdateStartRegistrationCurrentUsesOuterCommand(t *testing.T) {
	f, _, command, startRaw, startSignature := updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	_, raw, signature := updateStartRegistrationCommandFixture(
		t, command, startRaw, startSignature, runningRaw)
	if _, err := f.store.AdmitUpdateStartRegistrationCommand(
		context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	tx, facts, err := beginUpdateStartRegistration(
		context.Background(), f.store, raw, signature, "prepared")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := checkUpdateStartRegistrationCurrent(
		context.Background(), facts); err != nil {
		t.Fatal("current registration command", err)
	}
}

func TestUpdateStartRunningReceiptComparisonUsesContent(t *testing.T) {
	f, _, command, startRaw, startSignature := updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	first, err := ParseUpdateStartRunningReceipt(runningRaw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseUpdateStartRunningReceipt(runningRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !sameUpdateStartRunningReceipt(first, second) {
		t.Fatal("identical update-start running receipts differed")
	}
}

func TestUpdateStartRegistrationRefusesChangedTargetEvidence(t *testing.T) {
	t.Run("running receipt hash", func(t *testing.T) {
		f, _, command, startRaw, startSignature := updateStartJournalFixture(t)
		runningRaw := seedUpdateStartRunningFixture(
			t, f, command, startRaw, startSignature)
		registration, _, _ := updateStartRegistrationCommandFixture(
			t, command, startRaw, startSignature, runningRaw)
		registration.RunningReceiptSHA256 = strings.Repeat("1", 64)
		var err error
		registration.PayloadSHA256, err =
			control.UpdateStartRegistrationLogicalDigest(registration)
		if err != nil {
			t.Fatal(err)
		}
		enrollment := enrollmentFixture(30)
		authority := control.Authority{
			DeploymentID:    enrollment.DeploymentID,
			ControllerEpoch: enrollment.ControllerEpoch,
			ControlRevision: enrollment.ControlRevision,
			PublicKey:       enrollment.PublicKey, Fingerprint: enrollment.Fingerprint,
		}
		raw, signature, err := control.SignUpdateStartRegistration(
			registration,
			ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
			authority, time.Now().UTC().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.AdmitUpdateStartRegistrationCommand(
			context.Background(), raw, signature); err == nil {
			t.Fatal("changed running receipt admitted")
		}
	})
	t.Run("native identity", func(t *testing.T) {
		f, _, command, startRaw, startSignature := updateStartJournalFixture(t)
		runningRaw := seedUpdateStartRunningFixture(
			t, f, command, startRaw, startSignature)
		_, raw, signature := updateStartRegistrationCommandFixture(
			t, command, startRaw, startSignature, runningRaw)
		if _, err := f.store.db.Exec(
			`UPDATE runner_resources SET native_id=? WHERE id=?`,
			strings.Repeat("d", 62)+"01", command.Payload.ResourceID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.AdmitUpdateStartRegistrationCommand(
			context.Background(), raw, signature); err == nil {
			t.Fatal("changed native identity admitted")
		}
	})
	t.Run("target policy", func(t *testing.T) {
		f, _, command, startRaw, startSignature := updateStartJournalFixture(t)
		runningRaw := seedUpdateStartRunningFixture(
			t, f, command, startRaw, startSignature)
		changed := command
		changed.Payload.TargetPolicyRevision = "3"
		var err error
		changed.PayloadSHA256, err = control.UpdateStartLogicalDigest(changed)
		if err != nil {
			t.Fatal(err)
		}
		enrollment := enrollmentFixture(30)
		authority := control.Authority{
			DeploymentID:    enrollment.DeploymentID,
			ControllerEpoch: enrollment.ControllerEpoch,
			ControlRevision: enrollment.ControlRevision,
			PublicKey:       enrollment.PublicKey, Fingerprint: enrollment.Fingerprint,
		}
		changedRaw, changedSignature, err := control.SignUpdateStart(
			changed,
			ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
			authority, time.Now().UTC().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		registration, raw, signature := updateStartRegistrationCommandFixture(
			t, changed, changedRaw, changedSignature, runningRaw)
		if registration.StartRaw == nil {
			t.Fatal("changed start fixture missing")
		}
		if _, err := f.store.AdmitUpdateStartRegistrationCommand(
			context.Background(), raw, signature); err == nil {
			t.Fatal("changed target policy admitted")
		}
	})
}

func updateStartRemovalCommandFixture(t *testing.T, command control.UpdateStartCommand, startRaw, startSignature, runningRaw []byte, stage string) (control.UpdateStartRemovalCommand, []byte, []byte) {
	t.Helper()
	return updateStartRemovalCommandFixtureAtFence(
		t, command, startRaw, startSignature, runningRaw, stage,
		command.InstallationFence)
}

func updateStartRemovalCommandFixtureAtFence(t *testing.T, command control.UpdateStartCommand, startRaw, startSignature, runningRaw []byte, stage, fence string) (control.UpdateStartRemovalCommand, []byte, []byte) {
	t.Helper()
	removal := control.UpdateStartRemovalCommand{
		Format: control.UpdateStartRemovalFormat, EffectID: fixtureID(1532),
		OperationID: fixtureID(1533), Stage: stage,
		InstallationFence: fence,
		StartRaw:          startRaw, StartSignature: startSignature,
		RunningReceiptSHA256: candidateHash(runningRaw),
	}
	var err error
	removal.PayloadSHA256, err =
		control.UpdateStartRemovalLogicalDigest(removal)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := enrollmentFixture(30)
	authority := control.Authority{
		DeploymentID:    enrollment.DeploymentID,
		ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision,
		PublicKey:       enrollment.PublicKey, Fingerprint: enrollment.Fingerprint,
	}
	raw, signature, err := control.SignUpdateStartRemoval(
		removal, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
		authority, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return removal, raw, signature
}

func TestUpdateStartRemovalBindsRunningCandidate(t *testing.T) {
	f, _, command, startRaw, startSignature :=
		updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	_, raw, signature := updateStartRemovalCommandFixture(
		t, command, startRaw, startSignature, runningRaw, "stop")
	tx, facts, err := beginUpdateStartRemoval(
		context.Background(), f.store, raw, signature, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if facts.running.NativeID == "" ||
		facts.resource.id != command.Payload.ResourceID ||
		facts.verified.Command().OperationID ==
			facts.verified.Start().Command().OperationID {
		t.Fatal("removal source binding", facts)
	}
}

func TestUpdateStartRemovalRefusesChangedCandidate(t *testing.T) {
	f, _, command, startRaw, startSignature :=
		updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	_, raw, signature := updateStartRemovalCommandFixture(
		t, command, startRaw, startSignature, runningRaw, "stop")
	if _, err := f.store.db.Exec(
		`UPDATE runner_resources SET observed_state='stopped',revision=3 WHERE id=?`,
		command.Payload.ResourceID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := beginUpdateStartRemoval(
		context.Background(), f.store, raw, signature, nil); err == nil {
		t.Fatal("changed candidate state admitted for stop")
	}
}

func TestUpdateStartRemovalAllowsHistoricalSourceAfterWriterAdvance(t *testing.T) {
	f, _, command, startRaw, startSignature :=
		updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	_, raw, signature := updateStartRemovalCommandFixture(
		t, command, startRaw, startSignature, runningRaw, "stop")
	if _, err := f.store.db.Exec(
		`UPDATE runner_resources SET writer_fence=writer_fence+1
		WHERE kind='volume' AND data_namespace_id=?`,
		command.Payload.NamespaceID); err != nil {
		t.Fatal(err)
	}
	tx, facts, err := beginUpdateStartRemoval(
		context.Background(), f.store, raw, signature, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if facts.running.NativeID == "" ||
		facts.resource.writer != mustDataInt(command.Payload.TargetWriterFence) {
		t.Fatal("historical removal source binding", facts)
	}
}

func TestUpdateStartRemovalUnknownReloadUsesExactHistoricalSource(t *testing.T) {
	f, _, command, startRaw, startSignature :=
		updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	_, raw, signature := updateStartRemovalCommandFixtureAtFence(
		t, command, startRaw, startSignature, runningRaw, "stop", "3")

	tx, facts, err := beginUpdateStartRemoval(
		context.Background(), f.store, raw, signature, nil)
	if err != nil {
		t.Fatal(err)
	}
	index := updateStartRemovalIndex(facts.verified)
	if _, err := tx.Exec(
		`INSERT INTO runner_commands(effect_id,operation_id,installation_id,
		stage,effect_number,kind,payload_sha256,target_generation,
		controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,
		created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'unknown','{}',NULL,?,?)`,
		index.EffectID, index.OperationID, index.InstallationID,
		index.Stage, index.EffectNumber, index.Kind, index.PayloadSHA256,
		mustDataInt(index.TargetGeneration), index.ControllerEpoch, 3,
		facts.now, facts.now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(
		`UPDATE runner_resources SET writer_fence=writer_fence+1
		WHERE kind='volume' AND data_namespace_id=?`,
		command.Payload.NamespaceID); err != nil {
		t.Fatal(err)
	}

	tx, facts, err = beginUpdateStartRemoval(
		context.Background(), f.store, raw, signature, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if facts.row.state != "unknown" ||
		facts.resource.fence != mustDataInt(command.InstallationFence) ||
		facts.resource.writer != mustDataInt(command.Payload.TargetWriterFence) {
		t.Fatal("unknown removal source reload", facts)
	}
}

func TestUpdateStartRemovalResourceAdvancesStopThenRemoveCode(t *testing.T) {
	f, _, command, startRaw, startSignature :=
		updateStartJournalFixture(t)
	runningRaw := seedUpdateStartRunningFixture(
		t, f, command, startRaw, startSignature)
	running, err := ParseUpdateStartRunningReceipt(runningRaw)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixMilli()
	changed, err := advanceUpdateStartRemovalResource(
		context.Background(), tx, command.Payload.ResourceID,
		running.NativeID, "stop", now, now)
	if err != nil || changed != 1 {
		t.Fatal("stop resource transition", changed, err)
	}
	changed, err = advanceUpdateStartRemovalResource(
		context.Background(), tx, command.Payload.ResourceID,
		running.NativeID, "remove_code", now+1, now+1)
	if err != nil || changed != 1 {
		t.Fatal("remove-code resource transition", changed, err)
	}
	if changed, err := advanceUpdateStartRemovalResource(
		context.Background(), tx, command.Payload.ResourceID,
		running.NativeID, "remove_code", now+2, now+2); err != nil ||
		changed != 0 {
		t.Fatal("replayed remove-code transition", changed, err)
	}
}
