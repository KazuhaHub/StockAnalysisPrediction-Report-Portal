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
	var zero, result platformRuntimeRegistration
	if !platformValidUUID(deployment) {
		return zero, errAppPlatformRuntimeConflict
	}
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
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
		rows, err := e.query(`SELECT id,deployment_id,controller_epoch,control_revision,revision,enabled,verification_key_fingerprint,sealed_control_key,created_ms,updated_ms FROM app_platform_runtime_bindings ORDER BY id LIMIT 2` + lock)
		if err != nil {
			return err
		}
		var existing platformRuntimeRegistration
		var enabled int
		var fingerprint, sealed sql.NullString
		var created, updated int64
		count := 0
		for rows.Next() {
			count++
			if err := rows.Scan(&existing.id, &existing.deploymentID, &existing.controllerEpoch, &existing.controlRevision, &existing.revision, &enabled, &fingerprint, &sealed, &created, &updated); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if count > 1 {
			return errAppPlatformRuntimeConflict
		}
		if count == 1 {
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
			DeploymentID string `json:"deployment_id"`
			Fingerprint  string `json:"verification_key_fingerprint"`
		}{deployment, candidate.fingerprint})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.runtime_prepared','app_platform_runtime',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, id, string(detail)); err != nil {
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
