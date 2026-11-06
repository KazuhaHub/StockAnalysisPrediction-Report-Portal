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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// importResourceMatches checks the reserved namespace row against the claim. A
// settled row is present at revision two; a claimed one is unknown at revision one.
func importResourceMatches(ctx context.Context, tx *sql.Tx, c control.ImportCheckpointCommand, marker importClaimMarker, settled bool) (observedMS int64, err error) {
	var installation, kind, native, epoch, desired, observed, ownership string
	var namespace, release sql.NullString
	var generation, fence, writer, revision, created, updated int64
	var observedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms FROM runner_resources WHERE id=?`,
		marker.ResourceID).Scan(&installation, &namespace, &kind, &native, &release, &generation, &epoch, &fence, &writer, &desired, &observed, &ownership, &revision, &observedAt, &created, &updated); err != nil {
		return 0, ErrFenceReconciliation
	}
	targetGeneration, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	admitted, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	writerFence, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	if installation != c.InstallationID || !namespace.Valid || namespace.String != c.Payload.NamespaceID || kind != "volume" ||
		native != dataProjectPrefix+marker.ProjectID || !release.Valid || release.String != c.Payload.ReleaseSHA256 ||
		generation != targetGeneration || epoch != c.ControllerEpoch || fence != admitted || writer != writerFence ||
		desired != "present" || ownership != marker.OwnershipSHA256 || created < 0 {
		return 0, ErrFenceReconciliation
	}
	if settled {
		if observed != "present" || revision != 2 || !observedAt.Valid || updated != observedAt.Int64 {
			return 0, ErrFenceReconciliation
		}
		return observedAt.Int64, nil
	}
	if observed != "unknown" || revision != 1 || observedAt.Valid || updated != created {
		return 0, ErrFenceReconciliation
	}
	return 0, nil
}

func importIntentFrom(c control.ImportCheckpointCommand, raw, signature []byte, marker importClaimMarker, markerRaw string) (importIntent, error) {
	project, err := strconv.ParseUint(marker.ProjectID, 10, 32)
	if err != nil {
		return importIntent{}, ErrFenceReconciliation
	}
	quotaBytes, _ := strconv.ParseInt(c.Payload.QuotaBytes, 10, 64)
	quotaInodes, _ := strconv.ParseInt(c.Payload.QuotaInodes, 10, 64)
	return importIntent{
		command: c.Payload, full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature),
		pin: markerImportRoot(marker), project: uint32(project), quotaBytes: quotaBytes, quotaInodes: quotaInodes,
		marker: marker, markerRaw: markerRaw,
	}, nil
}

// loadUnknownImport rebuilds the intent of an import left unknown, from the
// journal alone, for read-only reconciliation. Expiry governs dispatch, not
// settlement, so the settlement variant of verification is used. It writes
// nothing and never produces a second claim.
func (s *Store) loadUnknownImport(ctx context.Context, raw, signature []byte, pin data.RootIdentity) (importIntent, error) {
	var zero importIntent
	if s.closing.Load() || !validDataRoot(pin) {
		return zero, ErrImportCheckpointUnavailable
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
	verified, err := control.VerifyImportCheckpointSettlement(raw, signature, importAuthorityFrom(deployment), time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	if row == nil || !row.sameCommand(c) || row.state != "unknown" {
		return zero, ErrFenceReconciliation
	}
	var marker importClaimMarker
	if !canonicalDataJSON(row.receipt, &marker) || !validImportMarker(marker) || marker.EffectID != c.EffectID ||
		marker.AdmittedFence != c.InstallationFence || marker.GrantID != c.Payload.GrantID ||
		marker.RecoverySetID != c.Payload.RecoverySetID || marker.CheckpointID != c.Payload.CheckpointID ||
		marker.NamespaceID != c.Payload.NamespaceID || markerImportRoot(marker) != pin {
		return zero, ErrFenceReconciliation
	}
	if _, err := importResourceMatches(ctx, tx, c, marker, false); err != nil {
		return zero, err
	}
	if err := requireDataRootHistory(ctx, tx, pin); err != nil {
		return zero, err
	}
	return importIntentFrom(c, raw, signature, marker, row.receipt)
}

// settleImportCheckpoint records a native observation as the applied receipt and
// the present resource in one transaction. The observation must bind exactly the
// claimed grant, checkpoint, namespace, project and quota; anything else leaves
// the effect unknown. The caller holds the data execution gate.
func (s *Store) settleImportCheckpoint(ctx context.Context, intent importIntent, snapshot data.ImportSnapshot) (ImportCheckpointReceipt, error) {
	var zero ImportCheckpointReceipt
	p := intent.command
	if snapshot.NamespaceID != p.NamespaceID || snapshot.ReleaseSHA256 != p.ReleaseSHA256 ||
		snapshot.ManifestSHA256 != p.ManifestSHA256 || snapshot.ProjectID != intent.project ||
		snapshot.DataFormat != mustDataInt(p.DataFormat) || snapshot.ByteLimit != intent.quotaBytes ||
		snapshot.InodeLimit != intent.quotaInodes || snapshot.NamespaceDevice == 0 || snapshot.NamespaceInode == 0 ||
		snapshot.Entries < 0 || snapshot.UsedBytes < 0 || snapshot.UsedInodes < 0 ||
		snapshot.UsedBytes > snapshot.ByteLimit || snapshot.UsedInodes > snapshot.InodeLimit || snapshot.ObservedMS <= 0 {
		return zero, fmt.Errorf("import observation binding: %w", ErrFenceReconciliation)
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
	verified, err := control.VerifyImportCheckpointSettlement(intent.raw, intent.signature, authority, now)
	if err != nil {
		return zero, fmt.Errorf("import settlement command: %w", ErrFenceReconciliation)
	}
	c := verified.Command()
	if c != intent.full {
		return zero, ErrFenceReconciliation
	}
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	if row == nil || !row.sameCommand(c) || row.state != "unknown" || row.receipt != intent.markerRaw {
		return zero, fmt.Errorf("import settlement state: %w", ErrFenceReconciliation)
	}
	if _, err := importResourceMatches(ctx, tx, c, intent.marker, false); err != nil {
		return zero, err
	}
	if err := requireDataRootHistory(ctx, tx, intent.pin); err != nil {
		return zero, err
	}
	receipt := ImportCheckpointReceipt{
		Format: importReceiptFormat, Claim: intent.marker, DeploymentID: c.DeploymentID,
		ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID,
		TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
		RecoverySetID: p.RecoverySetID, GrantID: p.GrantID, CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID,
		ReleaseSHA256: p.ReleaseSHA256, ManifestSHA256: p.ManifestSHA256, ArchiveBytes: p.ArchiveBytes,
		DataFormat: p.DataFormat, WriterFence: p.WriterFence, ProjectID: intent.marker.ProjectID,
		NamespaceInode: strconv.FormatUint(snapshot.NamespaceInode, 10), Device: strconv.FormatUint(snapshot.NamespaceDevice, 10),
		QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes,
		Entries: strconv.FormatInt(snapshot.Entries, 10), UsedBytes: strconv.FormatInt(snapshot.UsedBytes, 10),
		UsedInodes: strconv.FormatInt(snapshot.UsedInodes, 10), State: "imported",
		ObservedMS: strconv.FormatInt(snapshot.ObservedMS, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	if !validImportReceipt(receipt) || !importReceiptMatchesCommand(receipt, c) {
		return zero, fmt.Errorf("import settlement receipt: %w", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET observed_state='present',revision=2,observed_ms=?,updated_ms=? WHERE id=? AND observed_state='unknown' AND revision=1 AND observed_ms IS NULL AND ownership_sha256=?`,
		now, now, intent.marker.ResourceID, intent.marker.OwnershipSHA256)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("import settlement resource rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		string(encoded), now, c.EffectID, intent.markerRaw)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("import settlement command rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

// ReadImportCheckpointReceipt returns the applied receipt after rechecking the
// current signed authority and that the namespace resource still agrees with it.
// Expiry governs dispatch, so a replay after the signed window still reads.
// ErrNoSuchEffect is returned only when the journal proves the effect was never
// claimed; every other failure leaves the effect's fate unknown.
func (s *Store) ReadImportCheckpointReceipt(ctx context.Context, raw, signature []byte) (ImportCheckpointReceipt, error) {
	receipt, absence, err := s.readImportCheckpointOrWaitForClaim(ctx, raw, signature, control.VerifyImportCheckpointSettlement)
	if err != nil {
		return ImportCheckpointReceipt{}, err
	}
	if absence != nil {
		return ImportCheckpointReceipt{}, ErrNoSuchEffect
	}
	return receipt, nil
}

// ReadImportCheckpointOrAbsence is the runner's read-receipt answer: the applied
// receipt, or an ImportCheckpointAbsence when the journal holds no command row for
// the effect identity. The command is verified under the read purpose: a signature
// made for an execute is not a read.
func (s *Store) ReadImportCheckpointOrAbsence(ctx context.Context, raw, signature []byte) (any, error) {
	receipt, absence, err := s.readImportCheckpointOrWaitForClaim(ctx, raw, signature, control.VerifyImportCheckpointRead)
	if err != nil {
		return nil, err
	}
	if absence != nil {
		return *absence, nil
	}
	return receipt, nil
}

// readImportCheckpointOrWaitForClaim is the read a caller outside a dispatch uses. A dispatch holds the
// data execution gate from before its claim commits until it has settled, so a read that finds no row
// may have overtaken a claim in progress. It therefore takes the gate before it reports an absence and
// looks again: a claim that was racing it is then visible and the answer is unknown, not absent.
func (s *Store) readImportCheckpointOrWaitForClaim(ctx context.Context, raw, signature []byte, verify importCommandVerifier) (ImportCheckpointReceipt, *ImportCheckpointAbsence, error) {
	receipt, absence, err := s.readImportCheckpoint(ctx, raw, signature, verify)
	if err != nil || absence == nil {
		return receipt, absence, err
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return ImportCheckpointReceipt{}, nil, err
	}
	defer s.releaseDataExecution()
	return s.readImportCheckpoint(ctx, raw, signature, verify)
}

// importCommandVerifier is the verification a read applies to the command it is given: the settlement
// check for the command already admitted for an effect, or the read check for a read-receipt.
type importCommandVerifier func(raw, signature []byte, authority control.Authority, nowMS int64) (control.VerifiedImportCheckpointCommand, error)

func (s *Store) readImportCheckpoint(ctx context.Context, raw, signature []byte, verify importCommandVerifier) (ImportCheckpointReceipt, *ImportCheckpointAbsence, error) {
	var zero ImportCheckpointReceipt
	if s.closing.Load() {
		return zero, nil, ErrImportCheckpointUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, nil, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, nil, err
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := verify(raw, signature, importAuthorityFrom(deployment), now)
	if err != nil {
		return zero, nil, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, nil, err
	}
	if row == nil {
		// Absent means no row under the effect id and none holding the same operation, stage and
		// effect number under another id; a sibling row is a conflict, not an absence.
		var siblings int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=? OR (operation_id=? AND stage=? AND effect_number=?)`,
			c.EffectID, c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil {
			return zero, nil, err
		}
		if siblings != 0 {
			return zero, nil, ErrCommandConflict
		}
		if err := tx.Commit(); err != nil {
			return zero, nil, err
		}
		absence := importAbsenceFor(c, now)
		return zero, &absence, nil
	}
	if !row.sameCommand(c) || row.state != "applied" {
		return zero, nil, ErrImportCheckpointUnavailable
	}
	receipt, err := ParseImportCheckpointReceipt([]byte(row.receipt))
	if err != nil || !importReceiptMatchesCommand(receipt, c) || receipt.Claim.AdmittedFence != c.InstallationFence {
		return zero, nil, ErrFenceReconciliation
	}
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	observedAt, err := importResourceMatches(ctx, tx, c, receipt.Claim, true)
	if err != nil || observedAt != settled {
		return zero, nil, ErrFenceReconciliation
	}
	if err := requireDataRootHistory(ctx, tx, markerImportRoot(receipt.Claim)); err != nil {
		return zero, nil, err
	}
	if err := tx.Commit(); err != nil {
		return zero, nil, err
	}
	return receipt, nil, nil
}

func importNativeRequest(intent importIntent) (data.ImportRequest, error) {
	p := intent.command
	sourceProject, err := strconv.ParseUint(p.SourceProjectID, 10, 32)
	if err != nil {
		return data.ImportRequest{}, ErrFenceReconciliation
	}
	return data.ImportRequest{
		CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID, ReleaseSHA256: p.ReleaseSHA256,
		ManifestSHA256: p.ManifestSHA256, ProjectID: intent.project, DataFormat: mustDataInt(p.DataFormat),
		ArchiveBytes: mustDataInt(p.ArchiveBytes), MaxArchiveBytes: maxCheckpointArchiveBytes,
		WriterFence: mustDataInt(p.WriterFence), QuotaBytes: intent.quotaBytes, QuotaInodes: intent.quotaInodes,
		Provenance: data.ImportProvenance{
			ProjectID: uint32(sourceProject), NamespaceInode: mustDataUint(p.SourceNamespaceInode),
			Device: mustDataUint(p.SourceDevice),
			Source: data.RootIdentity{FilesystemUUID: p.SourceFilesystemUUID, Inode: mustDataUint(p.SourceRootInode)},
		},
	}, nil
}

// DispatchImportCheckpoint is the trusted infrastructure orchestrator for one
// signed import. The configured roots and pins never come from the command.
// Applied work replays its receipt without touching a native root. Unknown work
// is reconciled by read-only verification and is never imported a second time.
// An error may follow durable or physical effects and never authorizes a retry
// under another effect identity or namespace.
func (s *Store) DispatchImportCheckpoint(ctx context.Context, raw, signature []byte, dataRoot *data.Root, dataPin data.RootIdentity, checkpointRoot *data.Root, checkpointPin data.RootIdentity) (ImportCheckpointReceipt, error) {
	var zero ImportCheckpointReceipt
	if dataRoot == nil || checkpointRoot == nil || s.closing.Load() ||
		!validDataRoot(dataPin) || !validDataRoot(checkpointPin) || dataPin == checkpointPin ||
		len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrImportCheckpointUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	state, err := s.importState(ctx, raw, signature)
	if err != nil {
		return zero, fmt.Errorf("import state: %w", err)
	}
	if state == "applied" {
		receipt, err := s.readImportReceiptHeld(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		if markerImportRoot(receipt.Claim) != dataPin {
			return zero, ErrFenceReconciliation
		}
		return receipt, nil
	}
	if err := requireNativeRoots(ctx, dataRoot, dataPin, checkpointRoot, checkpointPin); err != nil {
		return zero, err
	}
	if state == "unknown" {
		intent, err := s.loadUnknownImport(ctx, raw, signature, dataPin)
		if err != nil {
			return zero, fmt.Errorf("reload unknown import: %w", err)
		}
		request, err := importNativeRequest(intent)
		if err != nil {
			return zero, err
		}
		// An effect that is already claimed is taken to a definite end whatever happens to the connection.
		ctx, cancel := detachedImportContext(ctx, request.ArchiveBytes)
		defer cancel()
		observation, err := dataRoot.VerifyImportedCheckpoint(ctx, dataPin, checkpointRoot, checkpointPin, request)
		if err != nil {
			return zero, fmt.Errorf("reconcile import: %w", err)
		}
		snapshot, err := observation.Snapshot()
		if err != nil {
			return zero, fmt.Errorf("reconcile import observation: %w", err)
		}
		return s.settleImportCheckpoint(ctx, intent, snapshot)
	}
	intent, replay, err := s.beginImportCheckpoint(ctx, raw, signature, dataPin)
	if err != nil {
		return zero, fmt.Errorf("begin import: %w", err)
	}
	if replay {
		return s.readImportReceiptHeld(ctx, raw, signature)
	}
	request, err := importNativeRequest(intent)
	if err != nil {
		return zero, err
	}
	// The claim is committed: from here the import runs to a definite end, applied or failed, detached
	// from the connection that asked for it. The Portal that sent the command may have timed out and
	// gone; it settles the effect later by reading the receipt.
	ctx, cancel := detachedImportContext(ctx, request.ArchiveBytes)
	defer cancel()
	observation, err := dataRoot.ImportCheckpoint(ctx, dataPin, checkpointRoot, checkpointPin, request)
	if err != nil {
		return zero, fmt.Errorf("import checkpoint: %w", err)
	}
	snapshot, err := observation.Snapshot()
	if err != nil {
		return zero, fmt.Errorf("import observation: %w", err)
	}
	return s.settleImportCheckpoint(ctx, intent, snapshot)
}

func requireNativeRoots(ctx context.Context, dataRoot *data.Root, dataPin data.RootIdentity, checkpointRoot *data.Root, checkpointPin data.RootIdentity) error {
	actual, err := dataRoot.Identity(ctx)
	if err != nil {
		return err
	}
	if actual != dataPin {
		return data.ErrUnsafe
	}
	actual, err = checkpointRoot.Identity(ctx)
	if err != nil {
		return err
	}
	if actual != checkpointPin {
		return data.ErrUnsafe
	}
	return nil
}

// importState classifies the effect without writing: "" when no row exists.
func (s *Store) importState(ctx context.Context, raw, signature []byte) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return "", err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return "", err
	}
	verified, err := control.VerifyImportCheckpointSettlement(raw, signature, importAuthorityFrom(deployment), time.Now().UTC().UnixMilli())
	if err != nil {
		return "", err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", nil
	}
	if !row.sameCommand(c) {
		return "", ErrCommandConflict
	}
	if row.state != "applied" && row.state != "unknown" {
		return "", ErrFenceReconciliation
	}
	return row.state, nil
}

// loadImportedStartSource admits restore-start from an applied import receipt.
// The receipt is the only evidence: it binds the namespace, checkpoint, release,
// data format, writer fence, project, inode, device, quota and data root of this
// host, and every one of them must equal the signed start payload. Lineage
// fields that name the old host's start and data receipts are opaque here; no
// row in this journal can corroborate them.
func loadImportedStartSource(ctx context.Context, tx *sql.Tx, c control.UpdateStartCommand, registrationEffect string) (updateStartSource, error) {
	var zero updateStartSource
	p := c.Payload
	fail := func(reason string) (updateStartSource, error) {
		return zero, fmt.Errorf("%w: imported start source %s", ErrFenceReconciliation, reason)
	}
	if c.Kind != "restore-start" {
		return fail("kind")
	}
	row, err := loadImportRow(ctx, tx, p.SourceCheckpointEffectID)
	if err != nil || row == nil {
		return fail("effect")
	}
	if row.state != "applied" || row.installation != c.InstallationID || row.operation != p.SourceCheckpointOperationID ||
		row.operation == c.OperationID || row.kind != "import-checkpoint" ||
		candidateHash([]byte(row.receipt)) != p.SourceCheckpointReceiptSHA256 {
		return fail("command")
	}
	receipt, err := ParseImportCheckpointReceipt([]byte(row.receipt))
	if err != nil || receipt.EffectID != p.SourceCheckpointEffectID || receipt.PayloadSHA256 != row.payload ||
		receipt.InstallationID != c.InstallationID || receipt.DeploymentID != c.DeploymentID ||
		receipt.ControllerEpoch != c.ControllerEpoch || receipt.OperationID != row.operation ||
		receipt.TargetGeneration != strconv.FormatInt(row.generation, 10) ||
		receipt.Claim.AdmittedFence != strconv.FormatInt(row.fence, 10) {
		return fail("receipt")
	}
	checkpointWriter := p.SourceWriterFence
	if p.CheckpointWriterFence != "" {
		checkpointWriter = p.CheckpointWriterFence
	}
	root := markerImportRoot(receipt.Claim)
	if receipt.CheckpointID != p.CheckpointID || receipt.NamespaceID != p.NamespaceID ||
		receipt.ReleaseSHA256 != p.SourceReleaseSHA256 || receipt.ManifestSHA256 != p.CheckpointManifestSHA256 ||
		receipt.ArchiveBytes != p.CheckpointArchiveBytes || receipt.DataFormat != p.DataFormat ||
		receipt.WriterFence != checkpointWriter || receipt.ProjectID != p.ProjectID ||
		receipt.NamespaceInode != p.NamespaceInode || receipt.Device != p.Device ||
		receipt.QuotaBytes != p.QuotaBytes || receipt.QuotaInodes != p.QuotaInodes {
		return fail("provenance")
	}
	if root.FilesystemUUID != p.RootFilesystemUUID || strconv.FormatUint(root.Inode, 10) != p.RootInode {
		return fail("root")
	}
	resource := control.ImportCheckpointCommand{
		InstallationID: receipt.InstallationID, ControllerEpoch: receipt.ControllerEpoch,
		TargetGeneration: receipt.TargetGeneration, InstallationFence: receipt.Claim.AdmittedFence,
		Payload: control.ImportCheckpointPayload{
			NamespaceID: receipt.NamespaceID, ReleaseSHA256: receipt.ReleaseSHA256, WriterFence: receipt.WriterFence,
		},
	}
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	if observed, err := importResourceMatches(ctx, tx, resource, receipt.Claim, true); err != nil || observed != settled {
		return fail("namespace resource")
	}
	if err := requireDataRootHistory(ctx, tx, root); err != nil {
		return zero, err
	}
	var pending, running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>?
		AND (?='' OR effect_id<>?) AND state IN ('prepared','unknown')`,
		c.InstallationID, c.EffectID, registrationEffect, registrationEffect).Scan(&pending); err != nil || pending != 0 {
		return fail("pending command")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources
		WHERE data_namespace_id=? AND kind='container' AND id<>? AND observed_state NOT IN ('stopped','absent')`,
		p.NamespaceID, p.ResourceID).Scan(&running); err != nil || running != 0 {
		return fail("running container")
	}
	return updateStartSource{
		imported: &receipt, checkpointRaw: []byte(row.receipt),
		checkpointSHA: p.SourceCheckpointReceiptSHA256, checkpointData: mustDataInt(receipt.ArchiveBytes),
	}, nil
}

// readImportReceiptHeld reads a receipt while the caller already holds the data execution gate.
func (s *Store) readImportReceiptHeld(ctx context.Context, raw, signature []byte) (ImportCheckpointReceipt, error) {
	receipt, absence, err := s.readImportCheckpoint(ctx, raw, signature, control.VerifyImportCheckpointSettlement)
	if err != nil {
		return ImportCheckpointReceipt{}, err
	}
	if absence != nil {
		return ImportCheckpointReceipt{}, ErrNoSuchEffect
	}
	return receipt, nil
}

// The hard bound on a claimed import. The connection that carried the command is bounded at two minutes
// and is not what bounds the work: a claimed import is copied, verified and settled for as long as its
// archive needs, up to this limit, so a slow disk does not turn a half-written namespace into an
// unknown effect. It is generous on purpose: five minutes plus two seconds for each MiB of archive,
// about 22 minutes for the largest archive (512 MiB). The Portal polls by read-receipt for the length of
// this bound.
const (
	importCheckpointBaseBound   = 5 * time.Minute
	importCheckpointPerMiBBound = 2 * time.Second
)

// ImportCheckpointBound is the longest a claimed import of an archive of this size may run.
func ImportCheckpointBound(archiveBytes int64) time.Duration {
	if archiveBytes < 0 {
		archiveBytes = 0
	}
	mib := (archiveBytes + (1 << 20) - 1) >> 20
	return importCheckpointBaseBound + time.Duration(mib)*importCheckpointPerMiBBound
}

// detachedImportContext carries the work of a claimed import: it keeps the parent's values, drops its
// cancellation and deadline, and applies the import's own bound.
func detachedImportContext(parent context.Context, archiveBytes int64) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), ImportCheckpointBound(archiveBytes))
}
