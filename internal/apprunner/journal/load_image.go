package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
)

var (
	ErrLoadImageUnavailable = errors.New("runner image loading is unavailable")
	// ErrLoadImageRefused is a refusal before any claim: the command does not describe a load this runner may run.
	ErrLoadImageRefused = errors.New("runner image load refused")
)

const loadClaimFormat = "portal.runner.load-image-claim/1-dev.1"
const loadReceiptFormat = "portal.runner.load-image-receipt/1-dev.1"
const loadStatusFormat = "portal.runner.load-image-status/1-dev.1"

// LoadImageNative is the native side of a load. It returns a plain snapshot so the journal binds every field.
type LoadImageNative interface {
	LoadImage(context.Context, driver.LoadImageRequest) (driver.LoadImageSnapshot, error)
	LoadedImagePresent(ctx context.Context, descriptor []byte, releaseSHA256, platform string) (bool, error)
}

// loadImageClaim is the durable record of one uncertain load. It is committed with the unknown state before any
// native call and carries everything a read-back needs to say which blobs are still missing.
type loadImageClaim struct {
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

// LoadImageClaim is the claim a receipt carries.
type LoadImageClaim = loadImageClaim

// LoadImageReceipt is a historical observation of one load. State is "loaded" or "present" for an applied effect
// and "rejected" for a reverted one: Docker cannot undo a load, so reverted means the image was not accepted.
type LoadImageReceipt struct {
	Format           string         `json:"format"`
	Claim            LoadImageClaim `json:"claim"`
	DeploymentID     string         `json:"deployment_id"`
	ControllerEpoch  string         `json:"controller_epoch"`
	InstallationID   string         `json:"installation_id"`
	OperationID      string         `json:"operation_id"`
	TargetGeneration string         `json:"target_generation"`
	Stage            string         `json:"stage"`
	EffectID         string         `json:"effect_id"`
	PayloadSHA256    string         `json:"payload_sha256"`
	ImageID          string         `json:"image_id"`
	Store            string         `json:"store"`
	State            string         `json:"state"`
	Outcome          string         `json:"outcome"`
	Removed          bool           `json:"removed"`
	ObservedMS       string         `json:"observed_ms"`
	SettledMS        string         `json:"settled_ms"`
}

var loadOutcomes = map[string]bool{
	"": true, driver.LoadOutcomeArchiveMismatch: true, driver.LoadOutcomeRetagDetected: true,
	driver.LoadOutcomeInspectMismatch: true, driver.LoadOutcomeLoadRejected: true,
}

func validLoadClaim(c loadImageClaim) bool {
	if c.Format != loadClaimFormat || !importID(c.ClaimID) || !importID(c.EffectID) || !dataSHA(c.ReleaseSHA256) || !dataSHA(c.ManifestDigest) ||
		!dataSHA(c.ConfigDigest) || !dataNumber(c.AdmittedFence, true) || !dataNumber(c.ClaimedMS, false) || (c.Platform != "linux/amd64" && c.Platform != "linux/arm64") {
		return false
	}
	_, ok := control.ParseLoadImageLayers(c.Layers)
	return ok
}

func validLoadReceipt(r LoadImageReceipt) bool {
	if r.Format != loadReceiptFormat || r.Stage != control.LoadImageStage || !validLoadClaim(r.Claim) || !dataSHA(r.PayloadSHA256) ||
		!nativeImageIDPattern(r.ImageID) || (r.Store != string(driver.ImageStoreClassic) && r.Store != string(driver.ImageStoreContainerd)) || !loadOutcomes[r.Outcome] {
		return false
	}
	switch r.State {
	case driver.LoadStateLoaded, driver.LoadStatePresent:
		if r.Outcome != "" || r.Removed {
			return false
		}
	case driver.LoadStateRejected:
		if r.Outcome == "" {
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

func nativeImageIDPattern(id string) bool {
	return len(id) == 71 && id[:7] == "sha256:" && dataSHA(id[7:])
}

func ParseLoadImageReceipt(raw []byte) (LoadImageReceipt, error) {
	var receipt LoadImageReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) || !validLoadReceipt(receipt) {
		return LoadImageReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func loadClaimMatchesCommand(m loadImageClaim, c control.LoadImageCommand) bool {
	p := c.Payload
	return m.EffectID == c.EffectID && m.ReleaseSHA256 == p.ReleaseSHA256 && m.Platform == p.Platform && m.ManifestDigest == p.ManifestDigest &&
		m.ConfigDigest == p.ConfigDigest && m.Layers == p.Layers && m.AdmittedFence == c.InstallationFence
}

// LoadImageReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func LoadImageReceiptMatchesCommand(r LoadImageReceipt, c control.LoadImageCommand) bool {
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage && r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 && loadClaimMatchesCommand(r.Claim, c)
}

// LoadImageStatus is the runner's typed answer when there is no receipt: "pending" while the load lock is held,
// "orphaned" for an unknown effect nothing is running, "absent" for an effect the journal proves it never claimed
// (given only under the lock), and "blobs_missing" for an unknown effect whose scratch no longer holds blobs.
type LoadImageStatus struct {
	Format           string   `json:"format"`
	DeploymentID     string   `json:"deployment_id"`
	ControllerEpoch  string   `json:"controller_epoch"`
	InstallationID   string   `json:"installation_id"`
	OperationID      string   `json:"operation_id"`
	TargetGeneration string   `json:"target_generation"`
	Stage            string   `json:"stage"`
	EffectNumber     string   `json:"effect_number"`
	EffectID         string   `json:"effect_id"`
	PayloadSHA256    string   `json:"payload_sha256"`
	State            string   `json:"state"`
	Missing          []string `json:"missing,omitempty"`
	ObservedMS       string   `json:"observed_ms"`
}

func validLoadStatus(s LoadImageStatus) bool {
	if s.Format != loadStatusFormat || s.Stage != control.LoadImageStage || !dataSHA(s.PayloadSHA256) || !dataNumber(s.TargetGeneration, true) ||
		!dataNumber(s.EffectNumber, true) || !dataNumber(s.ObservedMS, true) {
		return false
	}
	switch s.State {
	case "pending", "orphaned", "absent":
		if len(s.Missing) != 0 {
			return false
		}
	case "blobs_missing":
		if len(s.Missing) == 0 || len(s.Missing) > appplatform.MaxImageLayers+2 {
			return false
		}
		seen := map[string]bool{}
		for _, digest := range s.Missing {
			if !dataSHA(digest) || seen[digest] {
				return false
			}
			seen[digest] = true
		}
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

func ParseLoadImageStatus(raw []byte) (LoadImageStatus, error) {
	var status LoadImageStatus
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &status) || !validLoadStatus(status) {
		return LoadImageStatus{}, ErrFenceReconciliation
	}
	return status, nil
}

func loadStatusFor(c control.LoadImageCommand, state string, missing []string, now int64) LoadImageStatus {
	return LoadImageStatus{
		Format: loadStatusFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID,
		OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectNumber: c.EffectNumber,
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, State: state, Missing: missing, ObservedMS: strconv.FormatInt(now, 10),
	}
}

// LoadImageStatusMatchesCommand reports whether the status answers exactly this signed command.
func LoadImageStatusMatchesCommand(s LoadImageStatus, c control.LoadImageCommand) bool {
	return s.DeploymentID == c.DeploymentID && s.ControllerEpoch == c.ControllerEpoch && s.InstallationID == c.InstallationID &&
		s.OperationID == c.OperationID && s.TargetGeneration == c.TargetGeneration && s.Stage == c.Stage && s.EffectNumber == c.EffectNumber &&
		s.EffectID == c.EffectID && s.PayloadSHA256 == c.PayloadSHA256
}

func (s *Store) acquireLoadGate(ctx context.Context) error {
	s.loadOnce.Do(func() { s.loadGate = make(chan struct{}, 1) })
	select {
	case s.loadGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) tryAcquireLoadGate() bool {
	s.loadOnce.Do(func() { s.loadGate = make(chan struct{}, 1) })
	select {
	case s.loadGate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Store) releaseLoadGate() { <-s.loadGate }

// bindLoadImageDescriptor checks the descriptor the dispatch was given against the signed payload and the host:
// the release, a backend with managed image content, the host platform and the exact image entry.
func bindLoadImageDescriptor(c control.LoadImageCommand, descriptor []byte) (appplatform.BackendImage, error) {
	var zero appplatform.BackendImage
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != c.Payload.ReleaseSHA256 || parsed.Manifest.Backend == nil || len(parsed.Manifest.Backend.Images) == 0 ||
		c.Payload.Platform != "linux/"+runtime.GOARCH {
		return zero, ErrLoadImageRefused
	}
	for _, entry := range parsed.Manifest.Backend.Images {
		if entry.Platform == c.Payload.Platform {
			layers, ok := control.ParseLoadImageLayers(c.Payload.Layers)
			if !ok || entry.ManifestDigest != c.Payload.ManifestDigest || entry.ConfigDigest != c.Payload.ConfigDigest || len(layers) != len(entry.Layers) {
				return zero, ErrLoadImageRefused
			}
			for i := range layers {
				if layers[i] != entry.Layers[i] {
					return zero, ErrLoadImageRefused
				}
			}
			return entry, nil
		}
	}
	return zero, ErrLoadImageRefused
}

type loadImageIntent struct {
	full           control.LoadImageCommand
	raw, signature []byte
	marker         loadImageClaim
	markerRaw      string
	entry          appplatform.BackendImage
}

func sameLoadCommand(r *importRow, c control.CommandHeader) bool { return sameMigrateCommand(r, c) }

// loadState classifies the effect without writing. The command is verified under the settlement purpose, which is
// not bound by the window; an unclaimed command must still be inside its signed window to be dispatched.
func (s *Store) loadState(ctx context.Context, raw, signature []byte) (string, control.LoadImageCommand, *importRow, error) {
	var none control.LoadImageCommand
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
	verified, err := control.VerifyLoadImageSettlement(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return "", none, nil, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return "", none, nil, err
	}
	if row == nil {
		if _, err := control.VerifyLoadImage(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
			return "", none, nil, err
		}
		return "", c, nil, nil
	}
	if !sameLoadCommand(row, c.CommandHeader) {
		return "", none, nil, ErrCommandConflict
	}
	if row.state != "applied" && row.state != "reverted" && row.state != "unknown" {
		return "", none, nil, ErrFenceReconciliation
	}
	return row.state, c, row, nil
}

// beginLoadImage verifies the signed command and, in one transaction, checks the floors and commits the effect as
// unknown. A lost commit acknowledgement leaves unknown work and never authorizes a native call. The caller holds
// the load lock.
func (s *Store) beginLoadImage(ctx context.Context, raw, signature []byte, entry appplatform.BackendImage) (loadImageIntent, error) {
	var zero loadImageIntent
	if s.closing.Load() {
		return zero, ErrLoadImageUnavailable
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
	verified, err := control.VerifyLoadImage(raw, signature, authority, now)
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
		return zero, fmt.Errorf("%w: load fence floors", ErrCommandConflict)
	}
	claimID, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	marker := loadImageClaim{
		Format: loadClaimFormat, ClaimID: claimID, EffectID: c.EffectID, ReleaseSHA256: c.Payload.ReleaseSHA256, Platform: c.Payload.Platform,
		ManifestDigest: c.Payload.ManifestDigest, ConfigDigest: c.Payload.ConfigDigest, Layers: c.Payload.Layers,
		AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(now, 10),
	}
	markerRaw, err := json.Marshal(marker)
	if err != nil || len(markerRaw) > 16384 || !validLoadClaim(marker) || !loadClaimMatchesCommand(marker, c) {
		return zero, ErrLoadImageUnavailable
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
	if _, err := control.VerifyLoadImage(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
		return zero, fmt.Errorf("load claim command: %w", err)
	}
	if s.closing.Load() {
		return zero, ErrLoadImageUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return loadImageIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: string(markerRaw), entry: entry}, nil
}

// loadIntentFromRow rebuilds the intent of a load left unknown by an earlier attempt, so it is completed under the
// same claim and never claimed again.
func loadIntentFromRow(row *importRow, c control.LoadImageCommand, raw, signature []byte, entry appplatform.BackendImage) (loadImageIntent, error) {
	var marker loadImageClaim
	if row.state != "unknown" || !canonicalDataJSON(row.receipt, &marker) || !validLoadClaim(marker) || !loadClaimMatchesCommand(marker, c) {
		return loadImageIntent{}, ErrFenceReconciliation
	}
	return loadImageIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: row.receipt, entry: entry}, nil
}

// settleLoadImage records a definite end in one transaction: the receipt on the command row, applied or reverted.
func (s *Store) settleLoadImage(ctx context.Context, intent loadImageIntent, snapshot driver.LoadImageSnapshot) (LoadImageReceipt, error) {
	var zero LoadImageReceipt
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
	verified, err := control.VerifyLoadImageSettlement(intent.raw, intent.signature, importAuthorityFrom(deployment), now)
	if err != nil || verified.Command() != intent.full {
		return zero, ErrFenceReconciliation
	}
	c := intent.full
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil || row == nil || !sameLoadCommand(row, c.CommandHeader) || row.state != "unknown" || row.receipt != intent.markerRaw {
		return zero, fmt.Errorf("load settlement state: %w", ErrFenceReconciliation)
	}
	claimed := mustDataInt(intent.marker.ClaimedMS)
	receipt := LoadImageReceipt{
		Format: loadReceiptFormat, Claim: intent.marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, ImageID: snapshot.ImageID, Store: string(snapshot.Store),
		State: snapshot.State, Outcome: snapshot.Outcome, Removed: snapshot.Removed,
		ObservedMS: strconv.FormatInt(maxInt64(snapshot.ObservedMS, claimed), 10), SettledMS: strconv.FormatInt(maxInt64(now, maxInt64(snapshot.ObservedMS, claimed)), 10),
	}
	if !validLoadReceipt(receipt) || !LoadImageReceiptMatchesCommand(receipt, c) {
		return zero, fmt.Errorf("load settlement receipt: %w", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	commandState := "applied"
	if snapshot.State == driver.LoadStateRejected {
		commandState = "reverted"
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state=?,receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		commandState, string(encoded), now, c.EffectID, intent.markerRaw)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("load settlement rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

// bindLoadSnapshot refuses a native observation that does not describe the claimed load.
func bindLoadSnapshot(intent loadImageIntent, snapshot driver.LoadImageSnapshot) bool {
	expected := driver.ExpectedImageID(snapshot.Store, intent.entry)
	return snapshot.ReleaseSHA256 == intent.full.Payload.ReleaseSHA256 && snapshot.Platform == intent.full.Payload.Platform && snapshot.ImageID == expected &&
		(snapshot.State == driver.LoadStateLoaded || snapshot.State == driver.LoadStatePresent || snapshot.State == driver.LoadStateRejected) &&
		(snapshot.State == driver.LoadStateRejected) == (snapshot.Outcome != "") && snapshot.ObservedMS > 0
}

// DispatchLoadImage is the trusted orchestrator for one signed load. Applied or reverted work replays its receipt
// without touching Docker. A load left unknown by an earlier attempt is completed under that same claim, never
// claimed again, because loading is convergent: the archive is content-addressed and the runner writes it. A claimed
// load runs detached from the connection, to a definite end bounded by driver.ImageLoadTimeout.
//
// The result is a LoadImageReceipt, or a LoadImageStatus when the scratch no longer holds blobs the load needs: the
// claim stays unknown and the sender re-uploads the named blobs and sends the same command again.
func (s *Store) DispatchLoadImage(ctx context.Context, raw, signature, descriptor []byte, native LoadImageNative, scratch *imagestore.Store) (any, error) {
	if native == nil || scratch == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 ||
		len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return nil, ErrLoadImageUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireLoadGate(ctx); err != nil {
		return nil, err
	}
	defer s.releaseLoadGate()
	state, command, row, err := s.loadState(ctx, raw, signature)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	if state == "applied" || state == "reverted" {
		return s.readLoadReceiptHeld(ctx, raw, signature)
	}
	entry, err := bindLoadImageDescriptor(command, descriptor)
	if err != nil {
		return nil, err
	}
	var intent loadImageIntent
	if state == "unknown" {
		if intent, err = loadIntentFromRow(row, command, raw, signature, entry); err != nil {
			return nil, err
		}
	} else if intent, err = s.beginLoadImage(ctx, raw, signature, entry); err != nil {
		return nil, fmt.Errorf("begin load: %w", err)
	}
	// The claim is committed: the load runs to a definite end whatever happens to the connection.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), driver.ImageLoadTimeout)
	defer cancel()
	snapshot, err := native.LoadImage(ctx, driver.LoadImageRequest{
		Descriptor: descriptor, ReleaseSHA256: intent.full.Payload.ReleaseSHA256, Platform: intent.full.Payload.Platform,
		Source: scratch.Source(intent.full.OperationID),
	})
	if errors.Is(err, driver.ErrImageBlobUnavailable) {
		missing := scratch.Missing(intent.full.OperationID, entry)
		if len(missing) == 0 {
			// The scratch is complete yet a blob could not be read: nothing the sender can fix by re-uploading.
			return nil, fmt.Errorf("load blobs: %w", err)
		}
		return loadStatusFor(intent.full, "blobs_missing", missing, time.Now().UTC().UnixMilli()), nil
	}
	if err != nil {
		return nil, fmt.Errorf("load image: %w", err)
	}
	if !bindLoadSnapshot(intent, snapshot) {
		return nil, fmt.Errorf("load snapshot binding: %w", ErrFenceReconciliation)
	}
	receipt, err := s.settleLoadImage(ctx, intent, snapshot)
	if err != nil {
		return nil, fmt.Errorf("settle load: %w", err)
	}
	// The scratch served its load, accepted or not; a later operation uploads its own.
	_ = scratch.RemoveOperation(intent.full.OperationID)
	return receipt, nil
}

func (s *Store) readLoadReceiptHeld(ctx context.Context, raw, signature []byte) (LoadImageReceipt, error) {
	got, err := s.readLoadImage(ctx, raw, signature, control.VerifyLoadImageSettlement)
	if err != nil {
		return LoadImageReceipt{}, err
	}
	if got.receipt == nil {
		return LoadImageReceipt{}, ErrLoadImageUnavailable
	}
	return *got.receipt, nil
}

type loadReadResult struct {
	receipt *LoadImageReceipt
	command control.LoadImageCommand
	claim   loadImageClaim
	missing bool
	unknown bool
}

type loadVerifier func(raw, signature []byte, authority control.Authority, nowMS int64) (control.VerifiedLoadImageCommand, error)

func (s *Store) readLoadImage(ctx context.Context, raw, signature []byte, verify loadVerifier) (loadReadResult, error) {
	var zero loadReadResult
	if s.closing.Load() {
		return zero, ErrLoadImageUnavailable
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
		return loadReadResult{command: c, missing: true}, nil
	}
	if !sameLoadCommand(row, c.CommandHeader) {
		return zero, ErrCommandConflict
	}
	if row.state == "unknown" {
		var claim loadImageClaim
		if !canonicalDataJSON(row.receipt, &claim) || !validLoadClaim(claim) || !loadClaimMatchesCommand(claim, c) {
			return zero, ErrFenceReconciliation
		}
		return loadReadResult{command: c, claim: claim, unknown: true}, nil
	}
	if row.state != "applied" && row.state != "reverted" {
		return zero, ErrLoadImageUnavailable
	}
	receipt, err := ParseLoadImageReceipt([]byte(row.receipt))
	if err != nil || !LoadImageReceiptMatchesCommand(receipt, c) || (row.state == "applied") != (receipt.State != driver.LoadStateRejected) {
		return zero, ErrFenceReconciliation
	}
	return loadReadResult{command: c, receipt: &receipt}, nil
}

// ReadLoadImageOrStatus is the runner's read-back answer: the receipt (applied or reverted), or a LoadImageStatus.
// A status never waits for a running load: "pending" while the load lock is held, "orphaned" for an unknown effect
// with the lock free and every blob in scratch (or the image already present), "blobs_missing" with the digests
// the scratch no longer holds, and "absent" only for no row at all, proven with the lock held by this read. The
// command is verified under the read purpose and is not bound by the signed window. A read never writes a row.
func (s *Store) ReadLoadImageOrStatus(ctx context.Context, raw, signature, descriptor []byte, native LoadImageNative, scratch *imagestore.Store) (any, error) {
	got, err := s.readLoadImage(ctx, raw, signature, control.VerifyLoadImageRead)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().UnixMilli()
	if got.receipt != nil {
		return *got.receipt, nil
	}
	if !s.tryAcquireLoadGate() {
		return loadStatusFor(got.command, "pending", nil, now), nil
	}
	defer s.releaseLoadGate()
	// The load that held the lock may have settled the effect since the first look.
	again, err := s.readLoadImage(ctx, raw, signature, control.VerifyLoadImageRead)
	if err != nil {
		return nil, err
	}
	switch {
	case again.receipt != nil:
		return *again.receipt, nil
	case again.missing:
		return loadStatusFor(again.command, "absent", nil, time.Now().UTC().UnixMilli()), nil
	}
	return s.unknownLoadStatus(ctx, again, descriptor, native, scratch)
}

// unknownLoadStatus says what an unknown load needs: nothing the sender cannot give by resending (orphaned), or
// the blobs scratch lost.
func (s *Store) unknownLoadStatus(ctx context.Context, got loadReadResult, descriptor []byte, native LoadImageNative, scratch *imagestore.Store) (any, error) {
	now := time.Now().UTC().UnixMilli()
	entry, err := bindLoadImageDescriptor(got.command, descriptor)
	if err != nil || scratch == nil {
		return loadStatusFor(got.command, "orphaned", nil, now), nil
	}
	missing := scratch.Missing(got.command.OperationID, entry)
	if len(missing) == 0 {
		return loadStatusFor(got.command, "orphaned", nil, now), nil
	}
	// An image already in the daemon needs no blobs: a resend completes the claim as present.
	if native != nil {
		if present, err := native.LoadedImagePresent(ctx, descriptor, got.command.Payload.ReleaseSHA256, got.command.Payload.Platform); err == nil && present {
			return loadStatusFor(got.command, "orphaned", nil, now), nil
		}
	}
	return loadStatusFor(got.command, "blobs_missing", missing, now), nil
}
