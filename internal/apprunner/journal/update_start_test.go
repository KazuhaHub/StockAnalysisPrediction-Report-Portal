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
