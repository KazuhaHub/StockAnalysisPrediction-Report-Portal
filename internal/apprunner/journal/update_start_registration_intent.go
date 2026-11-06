package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func updateStartRegistrationIndex(v control.VerifiedUpdateStartRegistrationCommand) control.Command {
	start := v.Start().Command()
	return control.Command{
		Format:       control.UpdateStartRegistrationFormat,
		DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
		ControlRevision: start.ControlRevision, InstallationID: start.InstallationID,
		OperationID: start.OperationID, TargetGeneration: start.TargetGeneration,
		Stage: "activate", EffectNumber: "1", EffectID: v.Command().EffectID,
		Kind: "activate", InstallationFence: start.InstallationFence,
		PayloadSHA256: v.Command().PayloadSHA256,
		IssuedMS:      start.IssuedMS, ExpiresMS: start.ExpiresMS,
		Payload: control.PrepareDataPayload{
			DataNamespaceID: start.Payload.NamespaceID,
			ReleaseSHA256:   start.Payload.TargetReleaseSHA256,
			DataFormat:      start.Payload.DataFormat,
			WriterFence:     start.Payload.TargetWriterFence,
		},
	}
}

type updateStartRegistrationSource struct {
	start    updateStartFacts
	running  UpdateStartRunningReceipt
	request  driver.UpdateStartLaunchRequest
	expected driver.UpdateStartLaunchSnapshot
}

func loadUpdateStartRegistrationSource(ctx context.Context, tx *sql.Tx, v control.VerifiedUpdateStartRegistrationCommand, authority control.Authority, now int64) (updateStartRegistrationSource, error) {
	var zero updateStartRegistrationSource
	startCommand := v.Start().Command()
	index := updateStartIndex(startCommand)
	stored, err := loadAdmittedCommand(ctx, tx, startCommand.EffectID)
	if err != nil || stored.state != "applied" ||
		!sameLogicalCommand(stored, index) {
		return zero, fmt.Errorf("update-start registration start command: %w", ErrFenceReconciliation)
	}
	fence, _ := strconv.ParseInt(startCommand.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, index)
	if err != nil || fence < floor {
		return zero, fmt.Errorf("update-start registration floor: %w", ErrFenceReconciliation)
	}
	source, err := loadUpdateStartSourceExcludingRegistration(
		ctx, tx, startCommand, now, v.Command().EffectID)
	if err != nil || fence < source.resource.controlFence {
		return zero, fmt.Errorf("update-start registration checkpoint source: %w", ErrFenceReconciliation)
	}
	registrationCommand := v.Command()
	facts := updateStartFacts{
		command: startCommand, stored: stored, source: source,
		authority: authority, now: now,
		raw: registrationCommand.StartRaw, signature: registrationCommand.StartSignature,
	}
	var raw string
	var ack sql.NullInt64
	var created, updated int64
	if err := tx.QueryRowContext(ctx,
		`SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`,
		startCommand.EffectID).
		Scan(&raw, &ack, &created, &updated); err != nil ||
		ack.Valid || created < 0 || updated < created || updated > now {
		return zero, fmt.Errorf("update-start registration running row: %w", ErrFenceReconciliation)
	}
	facts.receipt, facts.created, facts.updated = raw, created, updated
	running, err := ParseUpdateStartRunningReceipt([]byte(raw))
	if err != nil || candidateHash([]byte(raw)) != v.Command().RunningReceiptSHA256 {
		return zero, fmt.Errorf("update-start registration running receipt: %w", ErrFenceReconciliation)
	}
	resource, err := existingCandidateResourcePrefix(ctx, tx, index, updateStartPendingPrefix, startCommand.Payload.ResourceID)
	if err != nil || !updateStartRunningMatches(facts, resource, running) ||
		updated != mustDataInt(running.SettledMS) {
		return zero, fmt.Errorf("update-start registration running resource: %w", ErrFenceReconciliation)
	}
	request, expected, err := updateStartExpectedSnapshot(facts, running.Claim, nil)
	if err != nil || request.Binding.PolicyRevision != startCommand.Payload.TargetPolicyRevision {
		return zero, fmt.Errorf("update-start registration native request: %w", ErrFenceReconciliation)
	}
	return updateStartRegistrationSource{
		start: facts, running: running, request: request, expected: expected,
	}, nil
}

func (s *Store) AdmitUpdateStartRegistrationCommand(ctx context.Context, raw, signature []byte) (CommandAdmission, error) {
	return s.admitSignedCommand(ctx, raw, signature, func(raw, signature []byte, authority control.Authority, now int64) (verifiedAdmissionCommand, error) {
		verified, err := control.VerifyUpdateStartRegistration(raw, signature, authority, now)
		if err != nil {
			return verifiedAdmissionCommand{}, err
		}
		start := verified.Start().Command()
		return verifiedAdmissionCommand{
			identity:                updateStartRegistrationIndex(verified),
			containerResourceID:     start.Payload.ResourceID,
			updateStartRegistration: &verified,
		}, nil
	})
}

func checkUpdateStartRegistrationCurrent(ctx context.Context, f updateStartRegistrationFacts) error {
	if err := finalUpdateStartCheck(ctx, f.source.start); err != nil {
		return fmt.Errorf(
			"update-start registration embedded start final: %w", err)
	}
	start := f.verified.Start().Command()
	now := time.Now().UnixMilli()
	issued, issuedErr := strconv.ParseInt(start.IssuedMS, 10, 64)
	expires, expiresErr := strconv.ParseInt(start.ExpiresMS, 10, 64)
	if issuedErr != nil || expiresErr != nil ||
		now < issued || now >= expires {
		return fmt.Errorf(
			"update-start registration start window: %w",
			control.ErrInvalidCommand)
	}
	if _, err := control.VerifyUpdateStart(
		f.source.start.raw, f.source.start.signature,
		f.source.start.authority, now); err != nil {
		return fmt.Errorf(
			"update-start registration embedded start: %w", err)
	}
	_, err := control.VerifyUpdateStartRegistration(
		f.raw, f.signature, f.source.start.authority,
		now)
	if err != nil {
		return fmt.Errorf(
			"update-start registration outer: %w", err)
	}
	return nil
}

func sameUpdateStartRegistrationSource(a, b updateStartRegistrationFacts) bool {
	return sameUpdateStartRunningReceipt(a.source.running, b.source.running) &&
		sameUpdateStartRegistrationRequest(a.source.request, b.source.request) &&
		a.source.expected == b.source.expected &&
		a.source.start.receipt == b.source.start.receipt &&
		a.source.start.created == b.source.start.created &&
		a.source.start.updated == b.source.start.updated &&
		a.row.receipt == b.row.receipt &&
		a.row.created == b.row.created && a.row.updated == b.row.updated &&
		a.row.stored == b.row.stored && b.source.start.now >= a.source.start.now
}

func updateStartRegistrationRecoveryFresh(f updateStartRegistrationFacts) bool {
	start := f.verified.Start().Command()
	issued, _ := strconv.ParseInt(start.IssuedMS, 10, 64)
	expires, _ := strconv.ParseInt(f.row.marker.ExpiresMS, 10, 64)
	return issued > expires && issued <= f.source.start.now
}

func sameUpdateStartRegistrationRequest(a, b driver.UpdateStartLaunchRequest) bool {
	return a.Identity == b.Identity && a.Platform == b.Platform &&
		a.Data == b.Data && a.Binding == b.Binding &&
		a.SandboxProfile == b.SandboxProfile &&
		bytes.Equal(a.Descriptor, b.Descriptor)
}

func sameUpdateStartRunningReceipt(a, b UpdateStartRunningReceipt) bool {
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	return bytes.Equal(ar, br)
}

func beginUpdateStartRegistration(ctx context.Context, s *Store, raw, signature []byte, state string) (*sql.Tx, updateStartRegistrationFacts, error) {
	var zero updateStartRegistrationFacts
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 {
		return nil, zero, ErrFenceReconciliation
	}
	raw, signature = append([]byte(nil), raw...), append([]byte(nil), signature...)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, zero, err
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return nil, zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyUpdateStartRegistration(raw, signature, authority, now)
	if err != nil {
		return nil, zero, fmt.Errorf(
			"verify update-start registration: %w", err)
	}
	row, present, err := readUpdateStartRegistrationRow(ctx, tx, verified, now)
	if err != nil || !present || row.stored.state != state {
		return nil, zero, ErrFenceReconciliation
	}
	source, err := loadUpdateStartRegistrationSource(
		ctx, tx, verified, authority, now)
	if err != nil {
		return nil, zero, err
	}
	facts := updateStartRegistrationFacts{
		verified: verified, source: source, row: row, raw: raw, signature: signature,
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return nil, zero, fmt.Errorf(
			"current update-start registration: %w", err)
	}
	ok = true
	return tx, facts, nil
}

type updateStartRegistrationFacts struct {
	verified       control.VerifiedUpdateStartRegistrationCommand
	source         updateStartRegistrationSource
	row            updateStartRegistrationRow
	raw, signature []byte
}

func (s *Store) preflightUpdateStartRegistration(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime, state string) (updateStartRegistrationFacts, error) {
	var zero updateStartRegistrationFacts
	tx, before, err := beginUpdateStartRegistration(ctx, s, raw, signature, state)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration preflight begin: %w", err)
	}
	tx.Rollback()
	if native == nil || len(descriptor) == 0 {
		return zero, ErrFenceReconciliation
	}
	request, expected, err := updateStartExpectedSnapshot(
		before.source.start, before.source.running.Claim, descriptor)
	comparable := before.source.request
	comparable.Descriptor = bytes.Clone(descriptor)
	if err != nil ||
		!sameUpdateStartRegistrationRequest(request, comparable) {
		return zero, fmt.Errorf(
			"update-start registration native request: %w",
			ErrFenceReconciliation)
	}
	observation, err := native.CheckUpdateStartReadiness(
		ctx, request, expected,
		before.source.running.NativeID, before.source.start.command.Payload.DataFormat)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration native readiness: %w", err)
	}
	actual, err := observation.Snapshot()
	if err != nil || actual.Launch != expected ||
		actual.NativeID != before.source.running.NativeID {
		return zero, fmt.Errorf(
			"update-start registration native snapshot: %w",
			ErrFenceReconciliation)
	}
	post, after, err := beginUpdateStartRegistration(ctx, s, raw, signature, state)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration preflight recheck: %w", err)
	}
	defer post.Rollback()
	if !sameUpdateStartRunningReceipt(
		before.source.running, after.source.running) {
		return zero, fmt.Errorf(
			"update-start registration source running: %w",
			ErrFenceReconciliation)
	}
	if !sameUpdateStartRegistrationRequest(
		before.source.request, after.source.request) {
		return zero, fmt.Errorf(
			"update-start registration source request: %w",
			ErrFenceReconciliation)
	}
	if before.source.expected != after.source.expected {
		return zero, fmt.Errorf(
			"update-start registration source snapshot: %w",
			ErrFenceReconciliation)
	}
	if before.source.start.receipt != after.source.start.receipt ||
		before.source.start.created != after.source.start.created ||
		before.source.start.updated != after.source.start.updated {
		return zero, fmt.Errorf(
			"update-start registration source start row: %w",
			ErrFenceReconciliation)
	}
	if before.row.receipt != after.row.receipt ||
		before.row.created != after.row.created ||
		before.row.updated != after.row.updated ||
		before.row.stored != after.row.stored {
		return zero, fmt.Errorf(
			"update-start registration source registration row: %w",
			ErrFenceReconciliation)
	}
	if after.source.start.now < before.source.start.now {
		return zero, fmt.Errorf(
			"update-start registration source clock: %w",
			ErrFenceReconciliation)
	}
	if actual.ObservedMS < before.source.start.now ||
		actual.ObservedMS > after.source.start.now {
		return zero, fmt.Errorf(
			"update-start registration source observation: %w",
			ErrFenceReconciliation)
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, after); err != nil {
		return zero, fmt.Errorf(
			"update-start registration preflight current: %w", err)
	}
	if s.closing.Load() {
		return zero, fmt.Errorf(
			"update-start registration preflight closing: %w",
			ErrFenceReconciliation)
	}
	if err := post.Commit(); err != nil {
		return zero, fmt.Errorf(
			"update-start registration preflight commit: %w", err)
	}
	return after, nil
}

func updateStartRegistrationDocumentDigest(document []byte) string {
	return candidateHash(document)
}

func updateStartRegistrationExpiry(f updateStartRegistrationFacts) (int64, error) {
	expires, err := strconv.ParseInt(f.verified.Start().Command().ExpiresMS, 10, 64)
	if err != nil || expires <= f.source.start.now {
		return 0, ErrFenceReconciliation
	}
	return expires, nil
}
