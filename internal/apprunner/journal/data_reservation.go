package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

var ErrDataReservationConflict = errors.New("runner data reservation conflicts with existing intent")

const dataProjectPrefix = "xfs-project/v1/"
const maxDataReservations = 65536
const maxDataProjectID = uint32(1<<32 - 2)

// DataReservation is durable intent only. Neither a new nor replayed result
// permits physical dispatch, writer access or activation. No provider is called.
type DataReservation struct {
	ResourceID, InstallationID, NamespaceID, EffectID, OwnershipSHA256 string
	ProjectID                                                          uint32
	Generation, ControlFence, WriterFence                              int64
	Replayed                                                           bool
}

func dataOwnership(c control.Command, project uint32) string {
	claim := struct {
		Format       string `json:"format"`
		Deployment   string `json:"deployment_id"`
		Epoch        string `json:"controller_epoch"`
		Revision     string `json:"control_revision"`
		Installation string `json:"installation_id"`
		Namespace    string `json:"namespace_id"`
		Native       string `json:"native_id"`
		Effect       string `json:"effect_id"`
		Payload      string `json:"payload_sha256"`
		Generation   string `json:"generation"`
		Fence        string `json:"admitted_fence"`
		Release      string `json:"release_sha256"`
		DataFormat   string `json:"data_format"`
		Bytes        string `json:"quota_bytes"`
		Inodes       string `json:"quota_inodes"`
		Writer       string `json:"writer_fence"`
	}{"portal.runner.data-ownership/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.InstallationID, c.Payload.DataNamespaceID, dataProjectPrefix + strconv.FormatUint(uint64(project), 10), c.EffectID, c.PayloadSHA256, c.TargetGeneration, c.InstallationFence, c.Payload.ReleaseSHA256, c.Payload.DataFormat, c.Payload.QuotaBytes, c.Payload.QuotaInodes, c.Payload.WriterFence}
	raw, _ := json.Marshal(claim)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func dataProjectID(native string) (uint32, bool) {
	if !strings.HasPrefix(native, dataProjectPrefix) {
		return 0, false
	}
	text := strings.TrimPrefix(native, dataProjectPrefix)
	number, err := strconv.ParseUint(text, 10, 32)
	if err != nil || number == 0 || number > uint64(maxDataProjectID) || strconv.FormatUint(number, 10) != text {
		return 0, false
	}
	return uint32(number), true
}
func allocateDataProject(ctx context.Context, tx *sql.Tx) (uint32, error) {
	rows, err := tx.QueryContext(ctx, `SELECT native_id FROM runner_resources WHERE kind='volume' LIMIT 65537`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var floor uint32
	count := 0
	for rows.Next() {
		count++
		if count >= maxDataReservations {
			return 0, ErrFenceReconciliation
		}
		var native string
		if err := rows.Scan(&native); err != nil {
			return 0, err
		}
		id, valid := dataProjectID(native)
		if !valid {
			return 0, ErrFenceReconciliation
		}
		if id > floor {
			floor = id
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if floor >= maxDataProjectID {
		return 0, ErrFenceReconciliation
	}
	return floor + 1, nil
}

type dataResource struct {
	id, installation, kind, native, epoch, desired, observed, ownership string
	namespace, release                                                  sql.NullString
	generation, fence, writer, revision, created, updated               int64
	observedMS                                                          sql.NullInt64
}

func existingDataResource(ctx context.Context, tx *sql.Tx, c control.Command) (*dataResource, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms FROM runner_resources WHERE id=? OR data_namespace_id=? OR (installation_id=? AND kind='volume') LIMIT 2`, c.Payload.DataNamespaceID, c.Payload.DataNamespaceID, c.InstallationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result *dataResource
	for rows.Next() {
		if result != nil {
			return nil, ErrDataReservationConflict
		}
		r := new(dataResource)
		if err := rows.Scan(&r.id, &r.installation, &r.namespace, &r.kind, &r.native, &r.release, &r.generation, &r.epoch, &r.fence, &r.writer, &r.desired, &r.observed, &r.ownership, &r.revision, &r.observedMS, &r.created, &r.updated); err != nil {
			return nil, err
		}
		result = r
	}
	return result, rows.Err()
}

// ReservePrepareData requires a currently verified envelope for an already
// admitted prepared command. It reserves one fresh namespace/project in existing
// resource rows; uncertain/executed work must instead enter reconciliation.
func (s *Store) ReservePrepareData(ctx context.Context, raw, signature []byte) (DataReservation, error) {
	var zero DataReservation
	tx, facts, err := s.beginDataCommand(ctx, raw, signature, "prepared", "{}")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	c, stored, now := facts.command, facts.stored, facts.now
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	result := DataReservation{ResourceID: c.Payload.DataNamespaceID, InstallationID: c.InstallationID, NamespaceID: c.Payload.DataNamespaceID, EffectID: c.EffectID, Generation: generation, ControlFence: stored.fence, WriterFence: writer}
	existing, err := existingDataResource(ctx, tx, c)
	if err != nil {
		return zero, err
	}
	if existing != nil {
		result, err = reservedDataResource(facts, existing)
		if err != nil {
			return zero, err
		}
	} else {
		id, err := allocateDataProject(ctx, tx)
		if err != nil {
			return zero, err
		}
		result.ProjectID = id
		result.OwnershipSHA256 = dataOwnership(c, id)
		_, err = tx.ExecContext(ctx, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'volume',?,?,?,?,?,?,'present','unknown',?,1,NULL,?,?)`, result.ResourceID, c.InstallationID, result.NamespaceID, dataProjectPrefix+strconv.FormatUint(uint64(id), 10), c.Payload.ReleaseSHA256, generation, c.ControllerEpoch, stored.fence, writer, result.OwnershipSHA256, now, now)
		if err != nil {
			return zero, err
		}
	}
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

// These checks are shared by reservation, durable claiming and the one-shot
// invocation gate. expectedReceipt is constructed only by this package.
type dataCommandFacts struct {
	command        control.Command
	stored         admittedCommand
	authority      control.Authority
	raw, signature []byte
	now            int64
}

func (s *Store) beginDataCommand(ctx context.Context, raw, signature []byte, expectedState, expectedReceipt string) (*sql.Tx, dataCommandFacts, error) {
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, dataCommandFacts{}, control.ErrInvalidCommand
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dataCommandFacts{}, err
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return nil, dataCommandFacts{}, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, dataCommandFacts{}, err
	}
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.Verify(raw, signature, authority, now)
	if err != nil {
		return nil, dataCommandFacts{}, err
	}
	c := verified.Command()
	quotaBytes, _ := strconv.ParseInt(c.Payload.QuotaBytes, 10, 64)
	if quotaBytes%512 != 0 {
		return nil, dataCommandFacts{}, ErrDataReservationConflict
	}
	stored, err := loadAdmittedCommand(ctx, tx, c.EffectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, dataCommandFacts{}, ErrDataReservationConflict
	}
	if err != nil {
		return nil, dataCommandFacts{}, err
	}
	if !sameLogicalCommand(stored, c) {
		return nil, dataCommandFacts{}, ErrDataReservationConflict
	}
	if stored.state != expectedState {
		return nil, dataCommandFacts{}, ErrFenceReconciliation
	}
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, c)
	if err != nil {
		return nil, dataCommandFacts{}, err
	}
	if fence != stored.fence || floor != stored.fence {
		return nil, dataCommandFacts{}, ErrFenceReconciliation
	}
	var receipt string
	var ack sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&receipt, &ack); err != nil {
		return nil, dataCommandFacts{}, err
	}
	if receipt != expectedReceipt || ack.Valid {
		return nil, dataCommandFacts{}, ErrFenceReconciliation
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND state IN ('prepared','unknown')`, c.InstallationID, c.EffectID).Scan(&pending); err != nil {
		return nil, dataCommandFacts{}, err
	}
	if pending != 0 {
		return nil, dataCommandFacts{}, ErrFenceReconciliation
	}
	ok = true
	return tx, dataCommandFacts{command: c, stored: stored, authority: authority, raw: raw, signature: signature, now: now}, nil
}
func finalDataCommandCheck(ctx context.Context, facts dataCommandFacts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if now < facts.now {
		return control.ErrInvalidCommand
	}
	_, err := control.Verify(facts.raw, facts.signature, facts.authority, now)
	return err
}
func reservedDataResource(facts dataCommandFacts, existing *dataResource) (DataReservation, error) {
	var zero DataReservation
	if existing == nil {
		return zero, ErrDataReservationConflict
	}
	c, stored, now := facts.command, facts.stored, facts.now
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	result := DataReservation{ResourceID: c.Payload.DataNamespaceID, InstallationID: c.InstallationID, NamespaceID: c.Payload.DataNamespaceID, EffectID: c.EffectID, Generation: generation, ControlFence: stored.fence, WriterFence: writer}
	r := existing
	id, valid := dataProjectID(r.native)
	if !valid || r.id != result.ResourceID || r.installation != result.InstallationID || !r.namespace.Valid || r.namespace.String != result.NamespaceID || r.kind != "volume" || !r.release.Valid || r.release.String != c.Payload.ReleaseSHA256 || r.generation != generation || r.epoch != c.ControllerEpoch || r.fence != stored.fence || r.writer != writer || r.desired != "present" || r.observed != "unknown" || r.ownership != dataOwnership(c, id) || r.revision != 1 || r.observedMS.Valid || r.created < 0 || r.created > now || r.created != r.updated {
		return zero, ErrDataReservationConflict
	}
	result.ProjectID, result.Replayed = id, true
	result.OwnershipSHA256 = dataOwnership(c, id)
	return result, nil
}
