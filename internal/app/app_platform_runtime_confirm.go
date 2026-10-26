package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Operator bootstrap configuration only; never deserialize a browser/app request
// into an endpoint or observation. These are not application manifest fields.
type platformRuntimeEndpoint struct {
	path                         string
	controllerUID, controllerGID uint32
}
type platformRuntimeObservation struct {
	controller journal.ControllerEnrollment
	receivedAt time.Time
}

// This entry point validates current administration before contacting the fixed
// endpoint, performs network I/O without database locks, then rechecks all state
// transactionally. It confirms enrollment authority, not native compatibility or
// app execution readiness. Managed admission still refuses unsupported runtimes.
func (s *Server) confirmPlatformRuntimeBinding(ctx context.Context, session platformActorSession, expected platformRuntimeRegistration, endpoint platformRuntimeEndpoint) (platformRuntimeRegistration, error) {
	if endpoint.path == "" || endpoint.controllerUID == 0 || endpoint.controllerGID == 0 || endpoint.controllerUID == ^uint32(0) || endpoint.controllerGID == ^uint32(0) {
		return platformRuntimeRegistration{}, errAppPlatformRuntimeConflict
	}
	expected.publicKey = bytes.Clone(expected.publicKey)
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if _, err := s.platformRuntimeForConfirmation(e, expected, control); err != nil {
			return err
		}
		_, err := platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRuntimeRegistration{}, err
	}
	observation, err := platformObserveRuntimeEndpoint(ctx, endpoint)
	if err != nil {
		return platformRuntimeRegistration{}, err
	}
	if err := ctx.Err(); err != nil {
		return platformRuntimeRegistration{}, err
	}
	return s.confirmPlatformObservedRuntime(session, expected, observation)
}

func (s *Server) platformRuntimeForConfirmation(e migExec, expected platformRuntimeRegistration, control platformActorControl) (platformRuntimeRegistration, error) {
	var zero, current platformRuntimeRegistration
	if !platformValidUUID(expected.id) || !platformValidUUID(expected.deploymentID) || expected.controllerEpoch != control.epoch || expected.controlRevision != 1 || expected.revision != 1 || len(expected.publicKey) != ed25519.PublicKeySize {
		return zero, errAppPlatformRuntimeConflict
	}
	lock := ""
	if s.st.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var enabled int
	var pin, sealed sql.NullString
	var created, updated int64
	err := e.queryRow(`SELECT id,deployment_id,controller_epoch,control_revision,revision,enabled,verification_key_fingerprint,sealed_control_key,created_ms,updated_ms FROM app_platform_runtime_bindings WHERE id=?`+lock, expected.id).Scan(&current.id, &current.deploymentID, &current.controllerEpoch, &current.controlRevision, &current.revision, &enabled, &pin, &sealed, &created, &updated)
	if err != nil {
		return zero, platformLeaseReadError(err)
	}
	var total int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_runtime_bindings`).Scan(&total); err != nil {
		return zero, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	if total != 1 || updated > now {
		return zero, errAppPlatformRuntimeConflict
	}
	if current.deploymentID != expected.deploymentID || current.controllerEpoch != expected.controllerEpoch || current.controlRevision != expected.controlRevision || !((current.revision == 1 && enabled == 0) || (current.revision == 2 && enabled == 1)) || !pin.Valid || pin.String != expected.fingerprint || !sealed.Valid || !strings.HasPrefix(sealed.String, sealPrefix) || created < 0 || updated < created {
		return zero, errAppPlatformRuntimeConflict
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
	}
	seed, err := aesOpen(dek, strings.TrimPrefix(sealed.String, sealPrefix), aad("app-platform", "runtime-control:"+expected.id))
	if err != nil || len(seed) != ed25519.SeedSize {
		return zero, errAppPlatformRuntimeConflict
	}
	current.publicKey = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(current.publicKey)
	current.fingerprint = hex.EncodeToString(sum[:])
	if current.fingerprint != expected.fingerprint || !bytes.Equal(current.publicKey, expected.publicKey) {
		return zero, errAppPlatformRuntimeConflict
	}
	return current, nil
}
func validPlatformRuntimeObservation(expected platformRuntimeRegistration, observation platformRuntimeObservation) bool {
	age := time.Since(observation.receivedAt)
	c := observation.controller
	return !observation.receivedAt.IsZero() && age >= 0 && age <= transport.MaxEnrollmentAge && c.DeploymentID == expected.deploymentID && c.ControllerEpoch == expected.controllerEpoch && c.ControlRevision == expected.controlRevision && c.Fingerprint == expected.fingerprint && bytes.Equal(c.PublicKey, expected.publicKey)
}

// Repository seam for trusted native observation only. Tests may synthesize this
// private value to exercise database fences; management handlers must use the
// endpoint entry point above and must never accept an observation from a body.
func (s *Server) confirmPlatformObservedRuntime(session platformActorSession, expected platformRuntimeRegistration, observation platformRuntimeObservation) (platformRuntimeRegistration, error) {
	var result, zero platformRuntimeRegistration
	expected.publicKey = bytes.Clone(expected.publicKey)
	observation.controller.PublicKey = bytes.Clone(observation.controller.PublicKey)
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		current, err := s.platformRuntimeForConfirmation(e, expected, control)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if !validPlatformRuntimeObservation(expected, observation) {
			return errAppPlatformRuntimeConflict
		}
		if current.revision == 1 {
			changed, err := e.exec(`UPDATE app_platform_runtime_bindings SET enabled=1,revision=2,updated_ms=? WHERE id=? AND revision=1 AND enabled=0 AND controller_epoch=? AND control_revision=1`, control.nowMS, expected.id, control.epoch)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			detail, _ := json.Marshal(struct {
				DeploymentID string `json:"deployment_id"`
				Fingerprint  string `json:"verification_key_fingerprint"`
				Evidence     string `json:"evidence"`
			}{expected.deploymentID, expected.fingerprint, "local-enrollment/1-dev.1"})
			if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.runtime_confirmed','app_platform_runtime',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, expected.id, string(detail)); err != nil {
				return err
			}
			current.revision = 2
		}
		// A blocked audit/row write can stale both the session and local peer proof.
		if _, err := platformRefreshActor(e, session, actor, control); err != nil {
			return err
		}
		if !validPlatformRuntimeObservation(expected, observation) {
			return errAppPlatformRuntimeConflict
		}
		result = current
		return nil
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}
