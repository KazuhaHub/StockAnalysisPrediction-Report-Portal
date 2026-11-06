package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"time"
)

var (
	ErrNotEnrolled        = errors.New("runner controller is not enrolled")
	ErrEnrollmentConflict = errors.New("runner controller enrollment conflicts with existing identity or work")
	ErrInvalidEnrollment  = errors.New("invalid runner controller enrollment")
)

type ControllerEnrollment struct {
	DeploymentID, ControllerEpoch, Fingerprint string
	ControlRevision                            int64
	PublicKey                                  []byte
}
type Deployment struct {
	ControllerEnrollment
	Revision, CreatedMS, UpdatedMS int64
}

var enrollmentUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func validEnrollment(request ControllerEnrollment) bool {
	if !enrollmentUUID.MatchString(request.DeploymentID) || !enrollmentUUID.MatchString(request.ControllerEpoch) || request.ControlRevision < 1 || len(request.PublicKey) != ed25519.PublicKeySize {
		return false
	}
	sum := sha256.Sum256(request.PublicKey)
	return request.Fingerprint == hex.EncodeToString(sum[:])
}

type deploymentQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readControllerDeployment(ctx context.Context, query deploymentQuerier) (Deployment, error) {
	var deployment Deployment
	var algorithm string
	err := query.QueryRowContext(ctx, `SELECT deployment_id,controller_epoch,control_revision,verification_algorithm,verification_key,key_fingerprint,revision,created_ms,updated_ms FROM runner_deployment WHERE singleton=1`).Scan(&deployment.DeploymentID, &deployment.ControllerEpoch, &deployment.ControlRevision, &algorithm, &deployment.PublicKey, &deployment.Fingerprint, &deployment.Revision, &deployment.CreatedMS, &deployment.UpdatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotEnrolled
	}
	if err != nil {
		return Deployment{}, err
	}
	if algorithm != "Ed25519" || !validEnrollment(deployment.ControllerEnrollment) || deployment.Revision < 1 || deployment.CreatedMS < 0 || deployment.UpdatedMS < deployment.CreatedMS {
		return Deployment{}, ErrInvalidEnrollment
	}
	return deployment, nil
}

// ControllerDeployment reads the explicit enrolled identity. Opening a journal
// never calls EnrollController or generates/replaces a controller key.
func (s *Store) ControllerDeployment(ctx context.Context) (Deployment, error) {
	return readControllerDeployment(ctx, s.db)
}

// EnrollController is an internal runtime-administrator primitive, not a control
// socket handler or startup hook. The caller must provide an independently pinned
// public identity. This only admits the first controller to an otherwise empty
// journal; rotation/restore recovery cannot overwrite it through this method.
func (s *Store) EnrollController(ctx context.Context, request ControllerEnrollment) (Deployment, bool, error) {
	request.PublicKey = bytes.Clone(request.PublicKey)
	if !validEnrollment(request) {
		return Deployment{}, false, ErrInvalidEnrollment
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, false, err
	}
	defer tx.Rollback()
	// SQLite write intent precedes any identity/resource read. A second open owner
	// is already excluded by the journal's filesystem guard; same-owner calls also
	// serialize through the database, including an initially empty table.
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return Deployment{}, false, err
	}
	current, err := readControllerDeployment(ctx, tx)
	if err == nil {
		if current.DeploymentID != request.DeploymentID || current.ControllerEpoch != request.ControllerEpoch || current.ControlRevision != request.ControlRevision || current.Fingerprint != request.Fingerprint || !bytes.Equal(current.PublicKey, request.PublicKey) {
			return Deployment{}, false, ErrEnrollmentConflict
		}
		if err := tx.Commit(); err != nil {
			return Deployment{}, false, err
		}
		return current, false, nil
	}
	if !errors.Is(err, ErrNotEnrolled) {
		return Deployment{}, false, err
	}
	var work int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runner_commands)+(SELECT COUNT(*) FROM runner_resources)+(SELECT COUNT(*) FROM runner_checkpoints)`).Scan(&work); err != nil {
		return Deployment{}, false, err
	}
	if work != 0 {
		return Deployment{}, false, ErrEnrollmentConflict
	}
	now := time.Now().UTC().UnixMilli()
	if now < 0 {
		return Deployment{}, false, ErrInvalidEnrollment
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runner_deployment(singleton,deployment_id,controller_epoch,control_revision,verification_algorithm,verification_key,key_fingerprint,revision,created_ms,updated_ms) VALUES(1,?,?,?,'Ed25519',?,?,1,?,?)`, request.DeploymentID, request.ControllerEpoch, request.ControlRevision, request.PublicKey, request.Fingerprint, now, now)
	if err != nil {
		return Deployment{}, false, err
	}
	result, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return Deployment{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, false, err
	}
	return result, true, nil
}
