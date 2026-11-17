package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var errAppPlatformRuntimeConflict = errors.New("application runtime registration requires explicit reconciliation")

// Public enrollment material only. This is not runner enrollment evidence, an
// enabled binding, a signing grant or physical compatibility/readiness proof.
type platformRuntimeRegistration struct {
	id, deploymentID, controllerEpoch, fingerprint string
	controlRevision, revision                      int64
	publicKey                                      []byte
}

// preparePlatformRuntimeBinding creates one disabled initial local binding. Only
// an explicitly initialized current administrator can prepare it. Exact pending
// retries preserve the key; restored, corrupt, enabled or changed identities are
// never silently repaired, rotated, adopted or enrolled. No startup/HTTP caller is
// connected. The existing DEK seals the seed with a binding-specific purpose.
func (s *Server) preparePlatformRuntimeBinding(session platformActorSession, deployment string) (platformRuntimeRegistration, error) {
	return s.preparePlatformRuntimeBindingMode(session, deployment, false)
}

// preparePlatformReplacementRuntime enrolls the runtime of a fresh host on a restored, paused Portal
// (doc 168, step 10). It runs only in recovery mode and only when every existing binding is retired
// (disabled, no key, no pin), which is exactly what a restore leaves behind. A live or pending old
// binding is never retired here: that is a decision for revocation, not a side effect of enrollment.
func (s *Server) preparePlatformReplacementRuntime(session platformActorSession, deployment string) (platformRuntimeRegistration, error) {
	return s.preparePlatformRuntimeBindingMode(session, deployment, true)
}

func (s *Server) preparePlatformRuntimeBindingMode(session platformActorSession, deployment string, replacement bool) (platformRuntimeRegistration, error) {
	var zero, result platformRuntimeRegistration
	if !platformValidUUID(deployment) {
		return zero, errAppPlatformRuntimeConflict
	}
	run := s.withPlatformTrustTransaction
	if replacement {
		run = s.withPlatformRecoveryTransaction
	}
	err := run(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		// withAppPlatformActor has loaded the DEK before opening this transaction;
		// this cached read performs no nested connection acquisition.
		dek, err := s.dek()
		if err != nil {
			return err
		}
		lock := ""
		if s.st.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		row, found, retiredIDs, err := platformActiveRuntime(e, lock != "")
		if err != nil {
			return err
		}
		existing, enabled, fingerprint, sealed, created, updated := row.platformRuntimeRegistration, row.enabled, row.pin, row.sealed, row.created, row.updated
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		// Ordinary enrollment starts from nothing; a replacement starts from retired rows only.
		if replacement != (len(retiredIDs) > 0) {
			return errAppPlatformRuntimeConflict
		}
		if found {
			if existing.deploymentID != deployment || !platformValidUUID(existing.id) || existing.controllerEpoch != control.epoch || existing.controlRevision != 1 || existing.revision != 1 || enabled != 0 || !fingerprint.Valid || !platformValidSHA(fingerprint.String) || !sealed.Valid || !strings.HasPrefix(sealed.String, sealPrefix) || created < 0 || updated < created || updated > control.nowMS {
				return errAppPlatformRuntimeConflict
			}
			seed, err := aesOpen(dek, strings.TrimPrefix(sealed.String, sealPrefix), aad("app-platform", "runtime-control:"+existing.id))
			if err != nil || len(seed) != ed25519.SeedSize {
				return errAppPlatformRuntimeConflict
			}
			existing.publicKey = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
			sum := sha256.Sum256(existing.publicKey)
			existing.fingerprint = hex.EncodeToString(sum[:])
			if existing.fingerprint != fingerprint.String {
				return errAppPlatformRuntimeConflict
			}
			if _, err := platformRefreshActor(e, session, actor, control); err != nil {
				return err
			}
			result = existing
			return nil
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return err
		}
		public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		sum := sha256.Sum256(public)
		candidate := platformRuntimeRegistration{id: id, deploymentID: deployment, controllerEpoch: control.epoch, controlRevision: 1, revision: 1, publicKey: public, fingerprint: hex.EncodeToString(sum[:])}
		ciphertext, err := aesSeal(dek, seed, aad("app-platform", "runtime-control:"+id))
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_runtime_bindings(id,deployment_id,controller_epoch,control_revision,enabled,verification_key_fingerprint,sealed_control_key,revision,created_ms,updated_ms) VALUES(?,?,?,1,0,?,?,1,?,?)`, id, deployment, control.epoch, candidate.fingerprint, sealPrefix+ciphertext, control.nowMS, control.nowMS); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			DeploymentID string   `json:"deployment_id"`
			Fingerprint  string   `json:"verification_key_fingerprint"`
			Retired      []string `json:"retired_runtime_ids,omitempty"`
		}{deployment, candidate.fingerprint, retiredIDs})
		action := "app_platform.runtime_prepared"
		if replacement {
			action = "app_platform.runtime_replacement_prepared"
		}
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_runtime',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, action, id, string(detail)); err != nil {
			return err
		}
		// Audit/row locks can outlive the authenticated session. Nothing may commit or
		// export enrollment material under expired, paused or changed authority.
		if _, err := platformRefreshActor(e, session, actor, control); err != nil {
			return err
		}
		result = candidate
		return nil
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}

// platformRuntimeRow is a runtime binding row as stored. It carries the stored key material and pin
// beside the registration so each caller can run its own checks on them.
type platformRuntimeRow struct {
	platformRuntimeRegistration
	enabled          int
	pin, sealed      sql.NullString
	created, updated int64
}

// platformActiveRuntime is the one place that reads which runtime binding is in force (doc 168,
// resolution 2). A restore retires every binding (disabled, no key, no pin) and a replacement is
// enrolled beside them, so the table may hold several rows but at most one that is not retired.
// found reports whether that one exists; it may still be pending or otherwise unfit, and the caller
// decides. More than one live row is an unresolved identity, never a choice. retired lists the ids
// of the retired rows, in id order, so a replacement can record what it replaces.
func platformActiveRuntime(e migExec, forUpdate bool) (row platformRuntimeRow, found bool, retired []string, err error) {
	lock := ""
	if forUpdate && e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	rows, err := e.query(`SELECT id,deployment_id,controller_epoch,control_revision,revision,enabled,verification_key_fingerprint,sealed_control_key,created_ms,updated_ms FROM app_platform_runtime_bindings ORDER BY id` + lock)
	if err != nil {
		return row, false, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var current platformRuntimeRow
		if err := rows.Scan(&current.id, &current.deploymentID, &current.controllerEpoch, &current.controlRevision, &current.revision, &current.enabled, &current.pin, &current.sealed, &current.created, &current.updated); err != nil {
			return row, false, nil, err
		}
		if current.enabled == 0 && !current.sealed.Valid && !current.pin.Valid {
			retired = append(retired, current.id)
			continue
		}
		if found {
			return platformRuntimeRow{}, false, nil, errAppPlatformRuntimeConflict
		}
		row, found = current, true
	}
	if err := rows.Err(); err != nil {
		return platformRuntimeRow{}, false, nil, err
	}
	return row, found, retired, nil
}
