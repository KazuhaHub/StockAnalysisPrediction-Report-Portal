package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

var ErrWritableCandidateClaimUnavailable = errors.New("writable candidate claim is unavailable")
var ErrWritableCandidateUncertain = errors.New("writable candidate invocation requires reconciliation")

const writableCandidatePendingPrefix = "writable-candidate-pending/v1/"

// WritableCandidateReservation is durable metadata, not a container or a live
// writer/start permit. It retains the exact identity already bound by the writer.
type WritableCandidateReservation struct {
	ResourceID, InstallationID, NamespaceID, EffectID, OwnershipSHA256 string
	DataReceiptSHA256, WriterReceiptSHA256                             string
	Generation, ControlFence, WriterFence                              int64
	Replayed                                                           bool
}
type writableCandidateFacts struct {
	verified              control.VerifiedWritableCandidateCommand
	stored                admittedCommand
	authority             control.Authority
	raw, signature        []byte
	source                writableCandidateSource
	resource              *dataResource
	receipt               string
	created, updated, now int64
	started               time.Time
}

func finalWritableCandidateCheck(ctx context.Context, f writableCandidateFacts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if now < f.now {
		return control.ErrInvalidCommand
	}
	_, err := control.VerifyWritableCandidate(f.raw, f.signature, f.authority, now)
	return err
}

// This stable ownership is tied to original receipt bytes and signed logical
// identity. Renewing the current control key/revision does not rewrite it.
func writableCandidateOwnership(f writableCandidateFacts) string {
	c := f.verified.Index()
	claim := struct{ Format, Deployment, Epoch, Effect, Resource, Installation, Operation, Namespace, Release, Generation, Fence, Writer, Payload, DataReceipt, WriterReceipt string }{"portal.runner.writable-candidate-ownership/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.EffectID, c.Payload.ResourceID, c.InstallationID, c.OperationID, c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, c.TargetGeneration, strconv.FormatInt(f.stored.fence, 10), c.Payload.WriterFence, c.PayloadSHA256, f.verified.Writer().Command().DataReceiptSHA256, f.verified.Command().WriterReceiptSHA256}
	raw, _ := json.Marshal(claim)
	return candidateHash(raw)
}

func reservedWritableCandidate(f writableCandidateFacts, r *dataResource) (WritableCandidateReservation, error) {
	var zero WritableCandidateReservation
	c := f.verified.Index()
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	if r == nil || r.id != c.Payload.ResourceID || r.installation != c.InstallationID || !r.namespace.Valid || r.namespace.String != c.Payload.DataNamespaceID || r.kind != "container" || r.native != writableCandidatePendingPrefix+c.Payload.ResourceID || !r.release.Valid || r.release.String != c.Payload.ReleaseSHA256 || r.generation != generation || r.epoch != c.ControllerEpoch || r.fence != f.stored.fence || r.writer != writer || r.desired != "present" || r.observed != "unknown" || r.ownership != writableCandidateOwnership(f) || r.revision != 1 || r.observedMS.Valid || r.created < f.created || r.updated != r.created || r.updated > f.now {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	return WritableCandidateReservation{ResourceID: r.id, InstallationID: r.installation, NamespaceID: r.namespace.String, EffectID: c.EffectID, OwnershipSHA256: r.ownership, DataReceiptSHA256: f.verified.Writer().Command().DataReceiptSHA256, WriterReceiptSHA256: f.verified.Command().WriterReceiptSHA256, Generation: generation, ControlFence: r.fence, WriterFence: r.writer, Replayed: true}, nil
}

// Exact current view. Pending and applied resources use separate strict
// validators. Only recovery may inspect an original expired unknown marker.
func (s *Store) beginWritableCandidate(ctx context.Context, raw, signature []byte, state string, receipt *string) (*sql.Tx, writableCandidateFacts, error) {
	return s.beginWritableCandidateView(ctx, raw, signature, state, receipt, false)
}
func (s *Store) beginWritableCandidateView(ctx context.Context, raw, signature []byte, state string, receipt *string, recovery bool) (*sql.Tx, writableCandidateFacts, error) {
	var zero writableCandidateFacts
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, zero, ErrWritableCandidateClaimUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
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
	d, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	a := control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
	started := time.Now()
	now := started.UnixMilli()
	v, err := control.VerifyWritableCandidate(raw, signature, a, now)
	if err != nil {
		return nil, zero, err
	}
	f, err := loadWritableCandidateFacts(ctx, tx, v, a, raw, signature, state, receipt, recovery, now, started, nil)
	if err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, f, nil
}

// Caller owns a current write-intent transaction and verified signed reference.
// Registration history is exempt only through its separately verified scope.
func loadWritableCandidateFacts(ctx context.Context, tx *sql.Tx, v control.VerifiedWritableCandidateCommand, a control.Authority, raw, signature []byte, state string, receipt *string, recovery bool, now int64, started time.Time, registration *control.VerifiedWritableBackendRegistrationCommand) (writableCandidateFacts, error) {
	var zero writableCandidateFacts
	c := v.Index()
	index := candidateIndex(c)
	stored, err := loadAdmittedCommand(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, index)
	if err != nil || !sameLogicalCommand(stored, index) || stored.state != state || stored.fence != fence || floor != fence {
		return zero, ErrFenceReconciliation
	}
	var current string
	var ack sql.NullInt64
	var created, updated int64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&current, &ack, &created, &updated); err != nil {
		return zero, err
	}
	if ack.Valid || (receipt != nil && *receipt != current) || created < 0 || updated < created || updated > now {
		return zero, ErrFenceReconciliation
	}
	switch state {
	case "prepared":
		if current != "{}" || updated != created {
			return zero, ErrFenceReconciliation
		}
	case "unknown": // Marker validation follows exact source/resource validation.
	case "applied":
		r, err := ParseWritableCandidateRunningReceipt([]byte(current))
		if err != nil {
			return zero, err
		}
		settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
		if updated != settled {
			return zero, ErrFenceReconciliation
		}
	default:
		return zero, ErrFenceReconciliation
	}
	source, err := loadWritableCandidateSourceCore(ctx, tx, v, now)
	if err != nil {
		return zero, err
	}
	settled, _ := strconv.ParseInt(source.writer.SettledMS, 10, 64)
	if created < settled {
		return zero, ErrFenceReconciliation
	}
	f := writableCandidateFacts{verified: v, stored: stored, authority: a, raw: raw, signature: signature, source: source, receipt: current, created: created, updated: updated, now: now, started: started}
	resource, err := existingCandidateResourcePrefix(ctx, tx, c, writableCandidatePendingPrefix)
	if err != nil {
		return zero, err
	}
	allowed := ""
	if resource != nil {
		if state == "applied" {
			r, err := ParseWritableCandidateRunningReceipt([]byte(current))
			if err != nil {
				return zero, err
			}
			if err := runningWritableCandidateResource(f, resource, r); err != nil {
				return zero, err
			}
		} else if _, err := reservedWritableCandidate(f, resource); err != nil {
			return zero, err
		}
		allowed = resource.id
	}
	if err := checkWritableCandidateHistoryForRegistration(ctx, tx, v, allowed, registration, now); err != nil {
		return zero, err
	}
	f.resource = resource
	if state == "applied" && resource == nil {
		return zero, ErrFenceReconciliation
	}
	if state == "unknown" {
		var marker writableCandidateClaimMarker
		matches := canonicalDataJSON(current, &marker) && writableCandidateMarkerMatches(marker, f)
		if recovery {
			matches = canonicalDataJSON(current, &marker) && writableCandidateMarkerIdentityMatches(marker, f) && marker.Preflight != nil
		}
		if resource == nil || !matches {
			return zero, ErrFenceReconciliation
		}
		claimed, _ := strconv.ParseInt(marker.ClaimedMS, 10, 64)
		expectedUpdated := claimed
		if marker.Preflight != nil {
			expectedUpdated, _ = strconv.ParseInt(marker.PreflightBoundMS, 10, 64)
		}
		if updated != expectedUpdated || claimed < resource.created {
			return zero, ErrFenceReconciliation
		}
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	return f, nil
}

func (s *Store) ReserveWritableCandidate(ctx context.Context, raw, signature []byte) (WritableCandidateReservation, error) {
	var zero WritableCandidateReservation
	empty := "{}"
	tx, f, err := s.beginWritableCandidate(ctx, raw, signature, "prepared", &empty)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	c := f.verified.Index()
	var result WritableCandidateReservation
	if f.resource != nil {
		result, err = reservedWritableCandidate(f, f.resource)
		if err != nil {
			return zero, err
		}
	} else {
		generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
		writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
		result = WritableCandidateReservation{ResourceID: c.Payload.ResourceID, InstallationID: c.InstallationID, NamespaceID: c.Payload.DataNamespaceID, EffectID: c.EffectID, OwnershipSHA256: writableCandidateOwnership(f), DataReceiptSHA256: f.verified.Writer().Command().DataReceiptSHA256, WriterReceiptSHA256: f.verified.Command().WriterReceiptSHA256, Generation: generation, ControlFence: f.stored.fence, WriterFence: writer}
		_, err = tx.ExecContext(ctx, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,?,?,?,?,'present','unknown',?,1,NULL,?,?)`, result.ResourceID, c.InstallationID, result.NamespaceID, writableCandidatePendingPrefix+result.ResourceID, c.Payload.ReleaseSHA256, generation, c.ControllerEpoch, f.stored.fence, writer, result.OwnershipSHA256, f.now, f.now)
		if err != nil {
			return zero, err
		}
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	return result, nil
}

// ReadWritableCandidateReservation is the specific current metadata reader for
// a prepared intent with an exact pending resource. Earlier no-resource readers
// remain strict and no executable handle is reconstructed here.
func (s *Store) ReadWritableCandidateReservation(ctx context.Context, raw, signature []byte) (WritableCandidateReservation, error) {
	var zero WritableCandidateReservation
	empty := "{}"
	tx, f, err := s.beginWritableCandidate(ctx, raw, signature, "prepared", &empty)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	result, err := reservedWritableCandidate(f, f.resource)
	if err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	return result, nil
}
