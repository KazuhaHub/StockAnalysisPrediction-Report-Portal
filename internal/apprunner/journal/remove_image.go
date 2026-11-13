package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Removing a managed image (doc 171, PR 4c).
//
// The Portal decides that no remaining installation references an image; the runner enforces the native guard (no
// container of any state uses it) and records the effect. The command follows the load-image idiom: the claim is
// committed as unknown before any native call, the work runs under the load gate (so it is fenced against a load of
// the same digest), the image is removed by ID and proven absent, and the effect settles together with the release
// of the image reservation in one transaction. The reservation is released only when the effect is applied.

var (
	// ErrRemoveImageUnavailable is a refusal before any claim: the runner cannot remove images now.
	ErrRemoveImageUnavailable = errors.New("runner image removal is unavailable")
	// ErrRemoveImageLoadPending is the typed refusal of a removal claim while a load of the same image has no
	// verdict: the load may be about to introduce the image, so nothing is claimed and nothing is touched.
	ErrRemoveImageLoadPending = errors.New("a load of the image has no verdict yet")
)

const removeClaimFormat = "portal.runner.remove-image-claim/1-dev.1"
const removeReceiptFormat = "portal.runner.remove-image-receipt/1-dev.1"
const removeStatusFormat = "portal.runner.remove-image-status/1-dev.1"

// RemoveOutcomeImageInUse is the typed outcome of a removal the native guard refused.
const RemoveOutcomeImageInUse = "image_in_use"

// RemoveImageNative is the native side of a removal. It returns plain snapshots so the journal binds every field.
type RemoveImageNative interface {
	RemoveImage(context.Context, driver.RemoveImageRequest) (driver.RemoveImageSnapshot, error)
	// ImagePresentByID resolves an unknown removal: the image is in the daemon by ID, or it is not.
	// The snapshot is "present" or "absent" and carries the image ID and store.
	ImagePresentByID(ctx context.Context, descriptor []byte, releaseSHA256, platform string) (driver.RemoveImageSnapshot, error)
}

type removeImageClaim struct {
	Format         string `json:"format"`
	ClaimID        string `json:"claim_id"`
	EffectID       string `json:"effect_id"`
	ReleaseSHA256  string `json:"release_sha256"`
	Platform       string `json:"platform"`
	ManifestDigest string `json:"manifest_digest"`
	ConfigDigest   string `json:"config_digest"`
	Layers         string `json:"layers"`
	AdmittedFence  string `json:"admitted_fence"`
	ClaimedMS      string `json:"claimed_ms"`
}

// RemoveImageClaim is the claim a receipt carries.
type RemoveImageClaim = removeImageClaim

// RemoveImageReceipt is a historical observation of one removal. State is "removed" or "absent" for an applied
// effect and "in_use" for a reverted one: the guard refused and nothing was removed.
type RemoveImageReceipt struct {
	Format           string           `json:"format"`
	Claim            RemoveImageClaim `json:"claim"`
	DeploymentID     string           `json:"deployment_id"`
	ControllerEpoch  string           `json:"controller_epoch"`
	InstallationID   string           `json:"installation_id"`
	OperationID      string           `json:"operation_id"`
	TargetGeneration string           `json:"target_generation"`
	Stage            string           `json:"stage"`
	EffectID         string           `json:"effect_id"`
	PayloadSHA256    string           `json:"payload_sha256"`
	ImageID          string           `json:"image_id"`
	Store            string           `json:"store"`
	State            string           `json:"state"`
	Outcome          string           `json:"outcome"`
	ObservedMS       string           `json:"observed_ms"`
	SettledMS        string           `json:"settled_ms"`
}

func validRemoveClaim(c removeImageClaim) bool {
	if c.Format != removeClaimFormat || !importID(c.ClaimID) || !importID(c.EffectID) || !dataSHA(c.ReleaseSHA256) || !dataSHA(c.ManifestDigest) ||
		!dataSHA(c.ConfigDigest) || !dataNumber(c.AdmittedFence, true) || !dataNumber(c.ClaimedMS, false) || (c.Platform != "linux/amd64" && c.Platform != "linux/arm64") {
		return false
	}
	_, ok := control.ParseLoadImageLayers(c.Layers)
	return ok
}

func validRemoveReceipt(r RemoveImageReceipt) bool {
	if r.Format != removeReceiptFormat || r.Stage != control.RemoveImageStage || !validRemoveClaim(r.Claim) || !dataSHA(r.PayloadSHA256) ||
		!nativeImageIDPattern(r.ImageID) || (r.Store != string(driver.ImageStoreClassic) && r.Store != string(driver.ImageStoreContainerd)) {
		return false
	}
	switch r.State {
	case driver.RemoveStateRemoved, driver.RemoveStateAbsent:
		if r.Outcome != "" {
			return false
		}
	case driver.RemoveStateInUse:
		if r.Outcome != RemoveOutcomeImageInUse {
			return false
		}
	default:
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID} {
		if !importID(id) {
			return false
		}
	}
	for _, value := range []string{r.TargetGeneration, r.ObservedMS, r.SettledMS} {
		if !dataNumber(value, true) {
			return false
		}
	}
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	return r.Claim.EffectID == r.EffectID && observed >= claimed && settled >= observed
}

func ParseRemoveImageReceipt(raw []byte) (RemoveImageReceipt, error) {
	var receipt RemoveImageReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) || !validRemoveReceipt(receipt) {
		return RemoveImageReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func removeClaimMatchesCommand(m removeImageClaim, c control.RemoveImageCommand) bool {
	p := c.Payload
	return m.EffectID == c.EffectID && m.ReleaseSHA256 == p.ReleaseSHA256 && m.Platform == p.Platform && m.ManifestDigest == p.ManifestDigest &&
		m.ConfigDigest == p.ConfigDigest && m.Layers == p.Layers && m.AdmittedFence == c.InstallationFence
}

// RemoveImageReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func RemoveImageReceiptMatchesCommand(r RemoveImageReceipt, c control.RemoveImageCommand) bool {
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage && r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 && removeClaimMatchesCommand(r.Claim, c)
}

// RemoveImageStatus is the runner's typed answer when there is no receipt: "pending" while the load lock is held,
// "orphaned" for an unknown effect nothing is running, and "absent" for an effect the journal proves it never
// claimed (given only under the lock). A read never writes a row.
type RemoveImageStatus struct {
	Format           string `json:"format"`
	DeploymentID     string `json:"deployment_id"`
	ControllerEpoch  string `json:"controller_epoch"`
	InstallationID   string `json:"installation_id"`
	OperationID      string `json:"operation_id"`
	TargetGeneration string `json:"target_generation"`
	Stage            string `json:"stage"`
	EffectNumber     string `json:"effect_number"`
	EffectID         string `json:"effect_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	State            string `json:"state"`
	ObservedMS       string `json:"observed_ms"`
}

func validRemoveStatus(s RemoveImageStatus) bool {
	if s.Format != removeStatusFormat || s.Stage != control.RemoveImageStage || !dataSHA(s.PayloadSHA256) || !dataNumber(s.TargetGeneration, true) ||
		!dataNumber(s.EffectNumber, true) || !dataNumber(s.ObservedMS, true) {
		return false
	}
	switch s.State {
	case "pending", "orphaned", "absent":
	default:
		return false
	}
	for _, id := range []string{s.DeploymentID, s.ControllerEpoch, s.InstallationID, s.OperationID, s.EffectID} {
		if !importID(id) {
			return false
		}
	}
	return true
}

func ParseRemoveImageStatus(raw []byte) (RemoveImageStatus, error) {
	var status RemoveImageStatus
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &status) || !validRemoveStatus(status) {
		return RemoveImageStatus{}, ErrFenceReconciliation
	}
	return status, nil
}

func removeStatusFor(c control.RemoveImageCommand, state string, now int64) RemoveImageStatus {
	return RemoveImageStatus{
		Format: removeStatusFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID,
		OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectNumber: c.EffectNumber,
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, State: state, ObservedMS: strconv.FormatInt(now, 10),
	}
}

// RemoveImageStatusMatchesCommand reports whether the status answers exactly this signed command.
func RemoveImageStatusMatchesCommand(s RemoveImageStatus, c control.RemoveImageCommand) bool {
	return s.DeploymentID == c.DeploymentID && s.ControllerEpoch == c.ControllerEpoch && s.InstallationID == c.InstallationID &&
		s.OperationID == c.OperationID && s.TargetGeneration == c.TargetGeneration && s.Stage == c.Stage && s.EffectNumber == c.EffectNumber &&
		s.EffectID == c.EffectID && s.PayloadSHA256 == c.PayloadSHA256
}

func sameRemoveCommand(r *importRow, c control.CommandHeader) bool { return sameMigrateCommand(r, c) }

type removeImageIntent struct {
	full           control.RemoveImageCommand
	raw, signature []byte
	marker         removeImageClaim
	markerRaw      string
	entry          appplatform.BackendImage
	// resumed is true for an effect an earlier attempt left unknown.
	resumed bool
}

// removeState classifies the effect without writing, under the settlement purpose. An unclaimed command must still be
// inside its signed window to be dispatched.
func (s *Store) removeState(ctx context.Context, raw, signature []byte) (string, control.RemoveImageCommand, *importRow, error) {
	var none control.RemoveImageCommand
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", none, nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return "", none, nil, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return "", none, nil, err
	}
	authority := importAuthorityFrom(deployment)
	verified, err := control.VerifyRemoveImageSettlement(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return "", none, nil, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return "", none, nil, err
	}
	if row == nil {
		if _, err := control.VerifyRemoveImage(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
			return "", none, nil, err
		}
		return "", c, nil, nil
	}
	if !sameRemoveCommand(row, c.CommandHeader) {
		return "", none, nil, ErrCommandConflict
	}
	if row.state != "applied" && row.state != "reverted" && row.state != "unknown" {
		return "", none, nil, ErrFenceReconciliation
	}
	return row.state, c, row, nil
}

// loadOfImageUnknown counts the loads of a config digest that have no verdict.
const loadOfImageUnknownSQL = `SELECT COUNT(*) FROM runner_commands WHERE kind='load-image' AND state='unknown'
	AND COALESCE(json_extract(receipt_json,'$.claim.config_digest'),json_extract(receipt_json,'$.config_digest'))=?`

// beginRemoveImage verifies the signed command and, in one transaction, checks the floors and the load fence and
// commits the effect as unknown. A lost commit acknowledgement leaves unknown work and never authorizes a native
// call. The caller holds the load lock.
func (s *Store) beginRemoveImage(ctx context.Context, raw, signature []byte, entry appplatform.BackendImage) (removeImageIntent, error) {
	var zero removeImageIntent
	if s.closing.Load() {
		return zero, ErrRemoveImageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := importAuthorityFrom(deployment)
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyRemoveImage(raw, signature, authority, now)
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	existing, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	if existing != nil {
		return zero, ErrCommandConflict
	}
	var siblings int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE operation_id=? AND stage=? AND effect_number=?`,
		c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil {
		return zero, err
	}
	if siblings != 0 {
		return zero, fmt.Errorf("%w: effect number in use", ErrCommandConflict)
	}
	var foreign int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND controller_epoch<>?)+(SELECT COUNT(*) FROM runner_resources WHERE installation_id=? AND controller_epoch<>?)`,
		c.InstallationID, c.ControllerEpoch, c.InstallationID, c.ControllerEpoch).Scan(&foreign); err != nil || foreign != 0 {
		return zero, ErrFenceReconciliation
	}
	var fenceFloor, generationFloor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(fence),0) FROM (SELECT admitted_fence AS fence FROM runner_commands WHERE installation_id=? UNION ALL SELECT control_fence AS fence FROM runner_resources WHERE installation_id=?)`,
		c.InstallationID, c.InstallationID).Scan(&fenceFloor); err != nil {
		return zero, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM (SELECT target_generation AS generation FROM runner_commands WHERE installation_id=? UNION ALL SELECT generation FROM runner_resources WHERE installation_id=?)`,
		c.InstallationID, c.InstallationID).Scan(&generationFloor); err != nil {
		return zero, err
	}
	fence, generation := mustDataInt(c.InstallationFence), mustDataInt(c.TargetGeneration)
	if fence < fenceFloor || generation < generationFloor {
		return zero, fmt.Errorf("%w: remove fence floors", ErrCommandConflict)
	}
	// A load of this image with no verdict may be about to introduce it: the claim is refused, typed, and nothing is
	// written. Both run under the load gate, so a load cannot start between this check and the removal.
	var pendingLoads int
	if err := tx.QueryRowContext(ctx, loadOfImageUnknownSQL, c.Payload.ConfigDigest).Scan(&pendingLoads); err != nil {
		return zero, err
	}
	if pendingLoads != 0 {
		return zero, ErrRemoveImageLoadPending
	}
	claimID, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	marker := removeImageClaim{
		Format: removeClaimFormat, ClaimID: claimID, EffectID: c.EffectID, ReleaseSHA256: c.Payload.ReleaseSHA256, Platform: c.Payload.Platform,
		ManifestDigest: c.Payload.ManifestDigest, ConfigDigest: c.Payload.ConfigDigest, Layers: c.Payload.Layers,
		AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(now, 10),
	}
	markerRaw, err := json.Marshal(marker)
	if err != nil || len(markerRaw) > 16384 || !validRemoveClaim(marker) || !removeClaimMatchesCommand(marker, c) {
		return zero, ErrRemoveImageUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'unknown',?,NULL,?,?)`,
		c.EffectID, c.OperationID, c.InstallationID, c.Stage, mustDataInt(c.EffectNumber), c.Kind, c.PayloadSHA256, generation, c.ControllerEpoch, fence,
		string(markerRaw), now, now); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if _, err := control.VerifyRemoveImage(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
		return zero, fmt.Errorf("remove claim command: %w", err)
	}
	if s.closing.Load() {
		return zero, ErrRemoveImageUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return removeImageIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: string(markerRaw), entry: entry}, nil
}

func removeIntentFromRow(row *importRow, c control.RemoveImageCommand, raw, signature []byte, entry appplatform.BackendImage) (removeImageIntent, error) {
	var marker removeImageClaim
	if row.state != "unknown" || !canonicalDataJSON(row.receipt, &marker) || !validRemoveClaim(marker) || !removeClaimMatchesCommand(marker, c) {
		return removeImageIntent{}, ErrFenceReconciliation
	}
	return removeImageIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: row.receipt, entry: entry, resumed: true}, nil
}

// settleRemoveImage records a definite end in one transaction: the receipt on the command row, applied (removed or
// absent) with the release of the image reservation, or reverted (in use) with the reservation kept. The release
// happens here and only here, and only when the effect is applied.
func (s *Store) settleRemoveImage(ctx context.Context, intent removeImageIntent, snapshot driver.RemoveImageSnapshot) (RemoveImageReceipt, error) {
	var zero RemoveImageReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyRemoveImageSettlement(intent.raw, intent.signature, importAuthorityFrom(deployment), now)
	if err != nil || verified.Command() != intent.full {
		return zero, ErrFenceReconciliation
	}
	c := intent.full
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil || row == nil || !sameRemoveCommand(row, c.CommandHeader) || row.state != "unknown" || row.receipt != intent.markerRaw {
		return zero, fmt.Errorf("remove settlement state: %w", ErrFenceReconciliation)
	}
	claimed := mustDataInt(intent.marker.ClaimedMS)
	receipt := RemoveImageReceipt{
		Format: removeReceiptFormat, Claim: intent.marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, ImageID: snapshot.ImageID, Store: string(snapshot.Store), State: snapshot.State,
		ObservedMS: strconv.FormatInt(maxInt64(snapshot.ObservedMS, claimed), 10), SettledMS: strconv.FormatInt(maxInt64(now, maxInt64(snapshot.ObservedMS, claimed)), 10),
	}
	commandState := "applied"
	if snapshot.State == driver.RemoveStateInUse {
		receipt.Outcome, commandState = RemoveOutcomeImageInUse, "reverted"
	}
	if !validRemoveReceipt(receipt) || !RemoveImageReceiptMatchesCommand(receipt, c) {
		return zero, fmt.Errorf("remove settlement receipt: %w", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state=?,receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		commandState, string(encoded), now, c.EffectID, intent.markerRaw)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("remove settlement rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	if commandState == "applied" {
		// The image is proven absent in this transaction, so its reservation is released in it.
		if err := releaseReservation(ctx, tx, imageResourceID(c.Payload.ConfigDigest), now); err != nil {
			return zero, err
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

// bindRemoveSnapshot refuses a native observation that does not describe the claimed removal.
func bindRemoveSnapshot(intent removeImageIntent, snapshot driver.RemoveImageSnapshot) bool {
	expected := driver.ExpectedImageID(snapshot.Store, intent.entry)
	return snapshot.ReleaseSHA256 == intent.full.Payload.ReleaseSHA256 && snapshot.Platform == intent.full.Payload.Platform && snapshot.ImageID == expected &&
		(snapshot.State == driver.RemoveStateRemoved || snapshot.State == driver.RemoveStateAbsent || snapshot.State == driver.RemoveStateInUse) && snapshot.ObservedMS > 0
}

// DispatchRemoveImage is the trusted orchestrator for one signed removal. Applied or reverted work replays its
// receipt without touching Docker. A removal left unknown by an earlier attempt is resolved by proving the image's
// absence or presence by ID first: an absent image settles as absent without any removal call, and only a present
// one goes through the guard again. A removal is never sent blind. A claimed removal runs detached from the
// connection, to a definite end bounded by driver.ImageRemoveTimeout.
func (s *Store) DispatchRemoveImage(ctx context.Context, raw, signature, descriptor []byte, native RemoveImageNative) (any, error) {
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 ||
		len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return nil, ErrRemoveImageUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireLoadGate(ctx); err != nil {
		return nil, err
	}
	defer s.releaseLoadGate()
	state, command, row, err := s.removeState(ctx, raw, signature)
	if err != nil {
		return nil, fmt.Errorf("remove state: %w", err)
	}
	if state == "applied" || state == "reverted" {
		return s.readRemoveReceiptHeld(ctx, raw, signature)
	}
	entry, err := bindImagePayload(control.LoadImagePayload(command.Payload), descriptor)
	if err != nil {
		return nil, err
	}
	var intent removeImageIntent
	if state == "unknown" {
		if intent, err = removeIntentFromRow(row, command, raw, signature, entry); err != nil {
			return nil, err
		}
	} else if intent, err = s.beginRemoveImage(ctx, raw, signature, entry); err != nil {
		return nil, fmt.Errorf("begin remove: %w", err)
	}
	// The claim is committed: the removal runs to a definite end whatever happens to the connection.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), driver.ImageRemoveTimeout)
	defer cancel()
	request := driver.RemoveImageRequest{Descriptor: descriptor, ReleaseSHA256: intent.full.Payload.ReleaseSHA256, Platform: intent.full.Payload.Platform}
	if intent.resumed {
		seen, err := native.ImagePresentByID(ctx, descriptor, request.ReleaseSHA256, request.Platform)
		if err != nil {
			// Nothing is settled and nothing is removed: the effect stays unknown and is resolved by the next attempt.
			return nil, fmt.Errorf("presence check of an unknown removal: %w", err)
		}
		if seen.State == driver.RemoveStateAbsent {
			// Proven absent by ID: the effect settles as applied with no removal call.
			if !bindRemoveSnapshot(intent, seen) {
				return nil, fmt.Errorf("remove presence binding: %w", ErrFenceReconciliation)
			}
			receipt, err := s.settleRemoveImage(ctx, intent, seen)
			if err != nil {
				return nil, fmt.Errorf("settle absent removal: %w", err)
			}
			return receipt, nil
		}
		if seen.State != driver.RemoveStatePresent || !bindPresence(intent, seen) {
			return nil, fmt.Errorf("remove presence binding: %w", ErrFenceReconciliation)
		}
	}
	snapshot, err := native.RemoveImage(ctx, request)
	if err != nil {
		log.Printf("remove-image %s: native removal: %s", intent.full.EffectID, printableDaemonDetail(err.Error()))
		return nil, fmt.Errorf("remove image: %w", err)
	}
	if !bindRemoveSnapshot(intent, snapshot) {
		return nil, fmt.Errorf("remove snapshot binding: %w", ErrFenceReconciliation)
	}
	receipt, err := s.settleRemoveImage(ctx, intent, snapshot)
	if err != nil {
		return nil, fmt.Errorf("settle removal: %w", err)
	}
	return receipt, nil
}

// bindPresence refuses a presence observation that does not describe the claimed image.
func bindPresence(intent removeImageIntent, snapshot driver.RemoveImageSnapshot) bool {
	return snapshot.ReleaseSHA256 == intent.full.Payload.ReleaseSHA256 && snapshot.Platform == intent.full.Payload.Platform &&
		snapshot.ImageID == driver.ExpectedImageID(snapshot.Store, intent.entry)
}

func (s *Store) readRemoveReceiptHeld(ctx context.Context, raw, signature []byte) (RemoveImageReceipt, error) {
	got, err := s.readRemoveImage(ctx, raw, signature, control.VerifyRemoveImageSettlement)
	if err != nil {
		return RemoveImageReceipt{}, err
	}
	if got.receipt == nil {
		return RemoveImageReceipt{}, ErrRemoveImageUnavailable
	}
	return *got.receipt, nil
}

type removeReadResult struct {
	receipt *RemoveImageReceipt
	command control.RemoveImageCommand
	missing bool
	unknown bool
}

type removeVerifier func(raw, signature []byte, authority control.Authority, nowMS int64) (control.VerifiedRemoveImageCommand, error)

func (s *Store) readRemoveImage(ctx context.Context, raw, signature []byte, verify removeVerifier) (removeReadResult, error) {
	var zero removeReadResult
	if s.closing.Load() {
		return zero, ErrRemoveImageUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	verified, err := verify(raw, signature, importAuthorityFrom(deployment), time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	if row == nil {
		var siblings int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=? OR (operation_id=? AND stage=? AND effect_number=?)`,
			c.EffectID, c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil {
			return zero, err
		}
		if siblings != 0 {
			return zero, ErrCommandConflict
		}
		return removeReadResult{command: c, missing: true}, nil
	}
	if !sameRemoveCommand(row, c.CommandHeader) {
		return zero, ErrCommandConflict
	}
	if row.state == "unknown" {
		var claim removeImageClaim
		if !canonicalDataJSON(row.receipt, &claim) || !validRemoveClaim(claim) || !removeClaimMatchesCommand(claim, c) {
			return zero, ErrFenceReconciliation
		}
		return removeReadResult{command: c, unknown: true}, nil
	}
	if row.state != "applied" && row.state != "reverted" {
		return zero, ErrRemoveImageUnavailable
	}
	receipt, err := ParseRemoveImageReceipt([]byte(row.receipt))
	if err != nil || !RemoveImageReceiptMatchesCommand(receipt, c) || (row.state == "applied") != (receipt.State != driver.RemoveStateInUse) {
		return zero, ErrFenceReconciliation
	}
	return removeReadResult{command: c, receipt: &receipt}, nil
}

// ReadRemoveImageOrStatus is the runner's read-back answer: the receipt (applied or reverted), or a status. A status
// never waits for a running removal: "pending" while the load lock is held, "orphaned" for an unknown effect with the
// lock free, and "absent" only for no row at all, proven with the lock held by this read. The command is verified
// under the read purpose and is not bound by the signed window. A read never writes a row.
func (s *Store) ReadRemoveImageOrStatus(ctx context.Context, raw, signature []byte) (any, error) {
	got, err := s.readRemoveImage(ctx, raw, signature, control.VerifyRemoveImageRead)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().UnixMilli()
	if got.receipt != nil {
		return *got.receipt, nil
	}
	if !s.tryAcquireLoadGate() {
		return removeStatusFor(got.command, "pending", now), nil
	}
	defer s.releaseLoadGate()
	// The removal that held the lock may have settled the effect since the first look.
	again, err := s.readRemoveImage(ctx, raw, signature, control.VerifyRemoveImageRead)
	if err != nil {
		return nil, err
	}
	switch {
	case again.receipt != nil:
		return *again.receipt, nil
	case again.missing:
		return removeStatusFor(again.command, "absent", time.Now().UTC().UnixMilli()), nil
	}
	return removeStatusFor(again.command, "orphaned", time.Now().UTC().UnixMilli()), nil
}
