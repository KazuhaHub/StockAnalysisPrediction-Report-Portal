package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Settling an update-start that was claimed and has no executor.
//
// A start the journal holds claimed (unknown) with the data execution gate free is orphaned: the runner stopped or
// failed somewhere after the claim, and a container may or may not exist. Nothing here creates or starts anything and
// nothing is resent. With the gate held, the runner looks by name and label at what exists and records it:
//
//   - no container: the start is closed as not run (reverted, outcome never_ran) and its resource is marked absent,
//     in one transaction;
//   - a container, in any state: the start is settled as applied, with a running receipt built from its own claim
//     and the container found, and its resource records that container. The ordinary stop and remove then take it
//     away like any running target.
//
// The settlement is a command of its own with its own effect, kept in a command row of the closed vocabulary the
// journal already has (stage validate, kind verify, neither used for anything else), so no schema changes. A replay
// returns the receipt that row holds.

// ErrUpdateStartNotOrphaned is the typed reason a settlement or a reserved removal refuses a start that is not in
// the state it is for.
var ErrUpdateStartNotOrphaned = errors.New("runner update-start is not orphaned")

// ErrUpdateStartNotReserved is the typed reason the reserved-only removal refuses a start that is no longer reserved.
var ErrUpdateStartNotReserved = errors.New("runner update-start is not reserved")

// TargetInspector reports what exists for a resource's container, by name and by label.
type TargetInspector interface {
	InspectTarget(ctx context.Context, resourceID string) (driver.TargetInspection, error)
	// ListNamespaceContainers and RemoveStrayContainer serve the sweep that follows a settled start.
	ListNamespaceContainers(ctx context.Context, namespaceID string) ([]driver.NamespaceContainer, error)
	RemoveStrayContainer(ctx context.Context, nativeID string) error
}

// SettleReasonForeignContainer is why a sweep is blocked: a container of the namespace is not one of the runner's.
const SettleReasonForeignContainer = "foreign_container"

const settleUpdateStartReceiptFormat = "portal.runner.settle-update-start-receipt/1-dev.1"

// SettleUpdateStartReceipt records how an orphaned start was settled and what was observed.
type SettleUpdateStartReceipt struct {
	Format           string `json:"format"`
	DeploymentID     string `json:"deployment_id"`
	ControllerEpoch  string `json:"controller_epoch"`
	InstallationID   string `json:"installation_id"`
	OperationID      string `json:"operation_id"`
	EffectID         string `json:"effect_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	StartOperationID string `json:"start_operation_id"`
	StartEffectID    string `json:"start_effect_id"`
	ResourceID       string `json:"resource_id"`
	NamespaceID      string `json:"namespace_id"`
	Outcome          string `json:"outcome"`
	ContainerState   string `json:"container_state"`
	Removed          string `json:"removed,omitempty"`
	Reason           string `json:"reason,omitempty"`
	NativeID         string `json:"native_id"`
	ObservedMS       string `json:"observed_ms"`
	SettledMS        string `json:"settled_ms"`
}

func validSettleUpdateStartReceipt(r SettleUpdateStartReceipt) bool {
	if r.Format != settleUpdateStartReceiptFormat || !dataSHA(r.PayloadSHA256) || !containerSHAValue(r.NativeID) ||
		!dataNumber(r.ObservedMS, false) || !dataNumber(r.SettledMS, false) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID, r.StartOperationID, r.StartEffectID, r.ResourceID, r.NamespaceID} {
		if !importID(id) {
			return false
		}
	}
	if r.Reason != "" && !(r.Outcome == "blocked" && r.Reason == SettleReasonForeignContainer) {
		return false
	}
	switch r.Outcome {
	case "blocked":
		// Never recorded: it answers a sweep that could not proceed, with the reason an administrator is shown.
		return r.Reason == SettleReasonForeignContainer && r.ContainerState == "" && r.Removed == "" && r.NativeID == control.ReservedStartNativeID(r.ResourceID)
	case "never_ran":
		return r.ContainerState == "" && r.Removed == "" && r.NativeID == control.ReservedStartNativeID(r.ResourceID)
	case "swept":
		return r.ContainerState == "" && dataNumber(r.Removed, false) && r.NativeID == control.ReservedStartNativeID(r.ResourceID)
	case "applied":
		return r.ContainerState != "" && len(r.ContainerState) <= 24 && r.Removed == ""
	}
	return false
}

func ParseSettleUpdateStartReceipt(raw []byte) (SettleUpdateStartReceipt, error) {
	var receipt SettleUpdateStartReceipt
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &receipt) || !validSettleUpdateStartReceipt(receipt) {
		return SettleUpdateStartReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

// SettleUpdateStartReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func SettleUpdateStartReceiptMatchesCommand(r SettleUpdateStartReceipt, c control.SettleUpdateStartCommand, start control.UpdateStartCommand) bool {
	return r.DeploymentID == start.DeploymentID && r.ControllerEpoch == start.ControllerEpoch && r.InstallationID == start.InstallationID &&
		r.OperationID == c.OperationID && r.EffectID == c.EffectID && r.PayloadSHA256 == c.PayloadSHA256 &&
		r.StartOperationID == start.OperationID && r.StartEffectID == start.EffectID && r.ResourceID == start.Payload.ResourceID &&
		r.NamespaceID == start.Payload.NamespaceID
}

type settleStartFacts struct {
	verified control.VerifiedSettleUpdateStartCommand
	start    admittedCommand
	row      admittedCommand
	resource *dataResource
	startRow string
	now      int64
}

// settleRowStageAndKind are the closed-vocabulary values of the settlement's own command row.
const settleRowStage, settleRowKind = "validate", "verify"

func loadSettleStart(ctx context.Context, tx *sql.Tx, raw, signature []byte, mode string) (settleStartFacts, error) {
	var zero settleStartFacts
	fail := func(reason string) (settleStartFacts, error) {
		return zero, fmt.Errorf("%w: settle update-start %s", ErrFenceReconciliation, reason)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	var verified control.VerifiedSettleUpdateStartCommand
	switch mode {
	case "read":
		verified, err = control.VerifySettleUpdateStartRead(raw, signature, authority, now)
	case "settle":
		verified, err = control.VerifySettleUpdateStartSettlement(raw, signature, authority, now)
	default:
		verified, err = control.VerifySettleUpdateStart(raw, signature, authority, now)
	}
	if err != nil {
		return zero, err
	}
	command, startCommand := verified.Command(), verified.Start().Command()
	start, err := loadAdmittedCommand(ctx, tx, startCommand.EffectID)
	if err != nil || !sameLogicalCommand(start, updateStartIndex(startCommand)) {
		return fail("start is not held")
	}
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	floor, err := commandFenceFloor(ctx, tx, updateStartIndex(startCommand))
	if err != nil || fence < floor || fence < start.fence {
		return fail("fence")
	}
	row, err := loadAdmittedCommand(ctx, tx, command.EffectID)
	if err == nil {
		if row.operationID != command.OperationID || row.stage != settleRowStage || row.kind != settleRowKind || row.payloadSHA != command.PayloadSHA256 ||
			row.installationID != startCommand.InstallationID || row.fence != fence || (row.state != "unknown" && row.state != "applied") {
			return fail("settlement row")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var startReceipt string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, startCommand.EffectID).Scan(&startReceipt); err != nil {
		return zero, err
	}
	resource, err := existingCandidateResourcePrefix(ctx, tx, updateStartIndex(startCommand), updateStartPendingPrefix, startCommand.Payload.ResourceID)
	if err != nil {
		return zero, err
	}
	return settleStartFacts{verified: verified, start: start, row: row, resource: resource, startRow: startReceipt, now: now}, nil
}

// SettleUpdateStart settles an orphaned start, or sweeps the namespace of a settled one, as the command asks. It takes
// the data execution gate without waiting: a start that is executing holds it, and nothing is touched then.
//
// Settling an orphan changes nothing natively (it only looks), so nothing is claimed beforehand: it inspects first,
// then writes the outcome in a single transaction. A failed inspection writes nothing, and a retry starts clean. The
// sweep does change things natively, so it is claimed first: see sweepNamespace. It reports the settlement receipt,
// which a replay returns again.
func (s *Store) SettleUpdateStart(ctx context.Context, raw, signature []byte, inspector TargetInspector) (SettleUpdateStartReceipt, error) {
	var zero SettleUpdateStartReceipt
	if inspector == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrManagedRemovalUnavailable
	}
	if !s.tryAcquireDataExecution() {
		return zero, fmt.Errorf("%w: the data execution gate is held", ErrManagedRemovalUnavailable)
	}
	defer s.releaseDataExecution()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	facts, err := loadSettleStart(ctx, tx, raw, signature, "execute")
	if err != nil {
		tx.Rollback()
		return zero, err
	}
	if facts.row.state == "applied" {
		defer tx.Rollback()
		return readSettleReceipt(ctx, tx, facts)
	}
	mode := facts.verified.Command().Mode
	start := facts.verified.Start().Command()
	tx.Rollback()
	if mode == control.SettleModeSweep {
		return s.sweepNamespace(ctx, raw, signature, facts, inspector)
	}
	if facts.start.state != "unknown" {
		return zero, fmt.Errorf("%w: the start is %s", ErrUpdateStartNotOrphaned, facts.start.state)
	}
	if err := requirePendingTarget(facts.resource, start, facts.start.fence); err != nil {
		return zero, err
	}
	observed, err := inspector.InspectTarget(ctx, facts.resource.id)
	if err != nil {
		return zero, fmt.Errorf("%w: inspect the target: %v", ErrManagedRemovalUnavailable, err)
	}
	return s.settleOrphan(ctx, raw, signature, observed)
}

// insertSettleRow records the settlement as applied, in the transaction that carries its effect.
func insertSettleRow(ctx context.Context, tx *sql.Tx, command control.SettleUpdateStartCommand, start control.UpdateStartCommand, receipt SettleUpdateStartReceipt, now int64) error {
	if !validSettleUpdateStartReceipt(receipt) || receipt.Outcome == "blocked" {
		return fmt.Errorf("%w: settle update-start receipt", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	_, err = tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,1,?,?,?,?,?,'applied',?,NULL,?,?)`,
		command.EffectID, command.OperationID, start.InstallationID, settleRowStage, settleRowKind, command.PayloadSHA256,
		start.TargetGeneration, start.ControllerEpoch, fence, string(encoded), now, now)
	return err
}

// sweepClaimFormat is the claim a sweep writes before it removes anything.
const sweepClaimFormat = "portal.runner.sweep-claim/1-dev.1"

// sweepClaim holds the intended container ids; the journal's JSON reader takes objects and strings only, so they are
// joined by commas in the encoded form.
type sweepClaim struct {
	Intended []string
}

type sweepClaimJSON struct {
	Format   string `json:"format"`
	Intended string `json:"intended"`
}

func (c sweepClaim) encode() ([]byte, error) {
	return json.Marshal(sweepClaimJSON{Format: sweepClaimFormat, Intended: strings.Join(c.Intended, ",")})
}

func parseSweepClaim(raw string) (sweepClaim, bool) {
	var encoded sweepClaimJSON
	if len(raw) > 65536 || !canonicalDataJSON(raw, &encoded) || encoded.Format != sweepClaimFormat {
		return sweepClaim{}, false
	}
	var claim sweepClaim
	if encoded.Intended != "" {
		claim.Intended = strings.Split(encoded.Intended, ",")
	}
	if len(claim.Intended) > 256 {
		return sweepClaim{}, false
	}
	for _, id := range claim.Intended {
		if !containerSHAValue(id) {
			return sweepClaim{}, false
		}
	}
	return claim, true
}

// sweepNamespace removes the containers labeled for the namespace that no live resource accounts for. It runs after
// the start is settled and its own target is gone, so what it finds is the leftover of a create that was still in
// flight when the runner stopped. A container a live resource accounts for is left alone.
//
// Removing a container is a native change, so the claim comes first: the set of container ids it intends to remove is
// written as the command's own row (unknown) before the first removal, and only then are they removed. A crash after
// the removals leaves that row; the resend removes the same set again (a removal of what is gone is a no-op), reads the
// namespace back and reports the removed set from the claim, not from what it happens to find. A container that
// appears after the claim is added to the claim before it is removed. A container that is not one of the runner's
// blocks the sweep with a typed reason and nothing is claimed or removed on its account.
func (s *Store) sweepNamespace(ctx context.Context, raw, signature []byte, facts settleStartFacts, inspector TargetInspector) (SettleUpdateStartReceipt, error) {
	var zero SettleUpdateStartReceipt
	start := facts.verified.Start().Command()
	command := facts.verified.Command()
	if facts.resource == nil || facts.resource.desired != "absent" || facts.resource.observed != "absent" {
		return zero, fmt.Errorf("%w: the start's target is not removed", ErrUpdateStartNotOrphaned)
	}
	base := SettleUpdateStartReceipt{
		Format: settleUpdateStartReceiptFormat, DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
		InstallationID: start.InstallationID, OperationID: command.OperationID, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		StartOperationID: start.OperationID, StartEffectID: start.EffectID, ResourceID: start.Payload.ResourceID, NamespaceID: start.Payload.NamespaceID,
		NativeID: control.ReservedStartNativeID(start.Payload.ResourceID),
	}
	var claim sweepClaim
	claimed := false
	if facts.row.effectID != "" {
		var rawClaim string
		if err := s.db.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&rawClaim); err != nil {
			return zero, err
		}
		var ok bool
		if claim, ok = parseSweepClaim(rawClaim); !ok {
			return zero, fmt.Errorf("%w: sweep claim", ErrFenceReconciliation)
		}
		claimed = true
	}
	for round := 0; ; round++ {
		listed, err := inspector.ListNamespaceContainers(ctx, start.Payload.NamespaceID)
		if errors.Is(err, driver.ErrForeignContainer) {
			blocked := base
			blocked.Outcome, blocked.Reason = "blocked", SettleReasonForeignContainer
			now := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
			blocked.ObservedMS, blocked.SettledMS = now, now
			return blocked, nil
		}
		if err != nil {
			return zero, fmt.Errorf("%w: list the namespace: %v", ErrManagedRemovalUnavailable, err)
		}
		var fresh []string
		for _, container := range listed {
			var accounted int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE kind='container' AND native_id=? AND NOT (desired_state='absent' AND observed_state='absent')`,
				container.NativeID).Scan(&accounted); err != nil {
				return zero, err
			}
			if accounted == 0 {
				fresh = append(fresh, container.NativeID)
			}
		}
		if len(fresh) == 0 {
			break
		}
		if round == 3 {
			return zero, fmt.Errorf("%w: a stray container is still there", ErrManagedRemovalUnavailable)
		}
		// Claim what is about to be removed, durably, before removing any of it.
		for _, id := range fresh {
			if !slices.Contains(claim.Intended, id) {
				claim.Intended = append(claim.Intended, id)
			}
		}
		if err := s.writeSweepClaim(ctx, raw, signature, claim, claimed); err != nil {
			return zero, err
		}
		claimed = true
		for _, id := range fresh {
			if err := inspector.RemoveStrayContainer(ctx, id); err != nil {
				return zero, fmt.Errorf("%w: remove a stray container: %v", ErrManagedRemovalUnavailable, err)
			}
		}
	}
	// The set reported is the claim's, whether this call or an earlier one removed it.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	again, err := loadSettleStart(ctx, tx, raw, signature, "settle")
	if err != nil {
		return zero, err
	}
	if again.row.state == "applied" {
		return readSettleReceipt(ctx, tx, again)
	}
	now := time.Now().UTC().UnixMilli()
	receipt := base
	receipt.Outcome, receipt.Removed = "swept", strconv.Itoa(len(claim.Intended))
	receipt.ObservedMS, receipt.SettledMS = strconv.FormatInt(now, 10), strconv.FormatInt(now, 10)
	if !validSettleUpdateStartReceipt(receipt) {
		return zero, fmt.Errorf("%w: settle update-start receipt", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return zero, err
	}
	if claimed {
		changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND ack_attempted_ms IS NULL`,
			string(encoded), now, command.EffectID)
		if err != nil {
			return zero, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return zero, fmt.Errorf("%w: sweep outcome rows=%d err=%v", ErrFenceReconciliation, n, err)
		}
	} else if err := insertSettleRow(ctx, tx, command, start, receipt, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

// writeSweepClaim records the intended set as the sweep's own row: inserted unknown the first time, extended after.
func (s *Store) writeSweepClaim(ctx context.Context, raw, signature []byte, claim sweepClaim, exists bool) error {
	encoded, err := claim.encode()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	facts, err := loadSettleStart(ctx, tx, raw, signature, "settle")
	if err != nil {
		return err
	}
	command, start := facts.verified.Command(), facts.verified.Start().Command()
	now := time.Now().UTC().UnixMilli()
	if exists {
		if facts.row.state != "unknown" {
			return fmt.Errorf("%w: sweep claim state %q", ErrFenceReconciliation, facts.row.state)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runner_commands SET receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND ack_attempted_ms IS NULL`,
			string(encoded), now, command.EffectID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if facts.row.effectID != "" {
		return fmt.Errorf("%w: sweep claim exists", ErrFenceReconciliation)
	}
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,1,?,?,?,?,?,'unknown',?,NULL,?,?)`,
		command.EffectID, command.OperationID, start.InstallationID, settleRowStage, settleRowKind, command.PayloadSHA256,
		start.TargetGeneration, start.ControllerEpoch, fence, string(encoded), now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// requirePendingTarget checks the resource is the start's own pending target, untouched.
func requirePendingTarget(resource *dataResource, start control.UpdateStartCommand, fence int64) error {
	p := start.Payload
	if resource == nil || resource.id != p.ResourceID || resource.installation != start.InstallationID || !resource.namespace.Valid ||
		resource.namespace.String != p.NamespaceID || resource.kind != "container" || resource.native != updateStartPendingPrefix+p.ResourceID ||
		!resource.release.Valid || resource.release.String != p.TargetReleaseSHA256 || resource.generation != mustDataInt(start.TargetGeneration) ||
		resource.epoch != start.ControllerEpoch || resource.fence != fence || resource.writer != mustDataInt(p.TargetWriterFence) ||
		resource.ownership != updateStartOwnership(start, fence) || resource.desired != "present" || resource.observed != "unknown" ||
		resource.revision != 1 || resource.observedMS.Valid {
		return fmt.Errorf("%w: settle update-start resource", ErrFenceReconciliation)
	}
	return nil
}

func (s *Store) settleOrphan(ctx context.Context, raw, signature []byte, observed driver.TargetInspection) (SettleUpdateStartReceipt, error) {
	var zero SettleUpdateStartReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	facts, err := loadSettleStart(ctx, tx, raw, signature, "settle")
	if err != nil {
		return zero, err
	}
	command, start := facts.verified.Command(), facts.verified.Start().Command()
	// With the gate held, the state must still be what the inspection was asked about, and nothing is recorded yet.
	if facts.row.effectID != "" || facts.start.state != "unknown" {
		return zero, fmt.Errorf("%w: the start is %s", ErrUpdateStartNotOrphaned, facts.start.state)
	}
	if err := requirePendingTarget(facts.resource, start, facts.start.fence); err != nil {
		return zero, err
	}
	now := time.Now().UTC().UnixMilli()
	receipt := SettleUpdateStartReceipt{
		Format: settleUpdateStartReceiptFormat, DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
		InstallationID: start.InstallationID, OperationID: command.OperationID, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		StartOperationID: start.OperationID, StartEffectID: start.EffectID, ResourceID: start.Payload.ResourceID, NamespaceID: start.Payload.NamespaceID,
		ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	var changed sql.Result
	if !observed.Found {
		receipt.Outcome, receipt.NativeID = "never_ran", control.ReservedStartNativeID(start.Payload.ResourceID)
		notRun, _ := json.Marshal(updateStartNotRun{Format: updateStartNotRunFormat, Outcome: "never_ran", EffectID: start.EffectID, SettledMS: strconv.FormatInt(now, 10)})
		changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
			string(notRun), now, start.EffectID, facts.startRow)
		if err != nil {
			return zero, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return zero, fmt.Errorf("%w: settle update-start start rows=%d err=%v", ErrFenceReconciliation, n, err)
		}
		changed, err = tx.ExecContext(ctx, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=4,observed_ms=?,updated_ms=?
			WHERE id=? AND kind='container' AND native_id=? AND revision=1 AND desired_state='present' AND observed_state='unknown' AND observed_ms IS NULL`,
			now, now, facts.resource.id, updateStartPendingPrefix+start.Payload.ResourceID)
	} else {
		var marker UpdateStartClaimMarker
		if !canonicalDataJSON(facts.startRow, &marker) || marker.Preflight == nil || !validUpdateStartClaimMarker(marker) || !containerSHAValue(observed.NativeID) {
			return zero, fmt.Errorf("%w: a container exists but the claim cannot account for it", ErrFenceReconciliation)
		}
		receipt.Outcome, receipt.ContainerState, receipt.NativeID = "applied", observed.State, observed.NativeID
		running := UpdateStartRunningReceipt{
			Format: updateStartRunningFormat, Claim: marker, DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
			InstallationID: start.InstallationID, OperationID: start.OperationID, NamespaceID: start.Payload.NamespaceID,
			SourceReleaseSHA256: start.Payload.SourceReleaseSHA256, TargetReleaseSHA256: start.Payload.TargetReleaseSHA256,
			SourceGeneration: start.Payload.SourceGeneration, TargetGeneration: start.TargetGeneration, ControlFence: start.InstallationFence,
			SourceWriterFence: start.Payload.SourceWriterFence, TargetWriterFence: start.Payload.TargetWriterFence, DataFormat: start.Payload.DataFormat,
			NativeID: observed.NativeID, NativeObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
		}
		encoded, _ := json.Marshal(running)
		changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
			string(encoded), now, start.EffectID, facts.startRow)
		if err != nil {
			return zero, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return zero, fmt.Errorf("%w: settle update-start start rows=%d err=%v", ErrFenceReconciliation, n, err)
		}
		changed, err = tx.ExecContext(ctx, `UPDATE runner_resources SET native_id=?,observed_state='running',revision=2,observed_ms=?,updated_ms=?
			WHERE id=? AND kind='container' AND native_id=? AND revision=1 AND observed_state='unknown' AND observed_ms IS NULL`,
			observed.NativeID, now, now, facts.resource.id, updateStartPendingPrefix+start.Payload.ResourceID)
	}
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("%w: settle update-start resource rows=%d err=%v", ErrFenceReconciliation, n, err)
	}
	if err := insertSettleRow(ctx, tx, command, start, receipt, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func readSettleReceipt(ctx context.Context, tx *sql.Tx, facts settleStartFacts) (SettleUpdateStartReceipt, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, facts.row.effectID).Scan(&raw); err != nil {
		return SettleUpdateStartReceipt{}, err
	}
	receipt, err := ParseSettleUpdateStartReceipt([]byte(raw))
	if err != nil || !SettleUpdateStartReceiptMatchesCommand(receipt, facts.verified.Command(), facts.verified.Start().Command()) {
		return SettleUpdateStartReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

// ReadSettleUpdateStartReceipt answers a read-back of a settlement: its receipt once applied. It signals with the
// typed refusal when the settlement was never admitted, so a caller reads the start itself.
func (s *Store) ReadSettleUpdateStartReceipt(ctx context.Context, raw, signature []byte) (SettleUpdateStartReceipt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SettleUpdateStartReceipt{}, err
	}
	defer tx.Rollback()
	facts, err := loadSettleStart(ctx, tx, raw, signature, "read")
	if err != nil {
		return SettleUpdateStartReceipt{}, err
	}
	if facts.row.state != "applied" {
		return SettleUpdateStartReceipt{}, ErrUpdateStartNotOrphaned
	}
	return readSettleReceipt(ctx, tx, facts)
}
