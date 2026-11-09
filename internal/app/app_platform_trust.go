package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformTrustConflict = errors.New("application publisher trust changed or conflicts")

type platformPublisherChange struct {
	kind, publisherID, keyID              string
	expectedRevision, expectedKeyRevision int64
	enabled                               bool
	namespaces                            []appplatform.PublisherNamespace
	key                                   *appplatform.PublisherKey
}
type platformPublisherTrustRevision struct {
	publisherID, keyID    string
	revision, keyRevision int64
	quarantined           int
}

// Private administration repository. The HTTP surface must supply current
// administrator/session/enrollment/origin checks and exact typed inputs.
func (s *Server) changePlatformPublisherTrust(session platformActorSession, change platformPublisherChange) (platformPublisherTrustRevision, error) {
	return s.changePlatformPublisherTrustContext(context.Background(), session, change)
}

func (s *Server) changePlatformPublisherTrustContext(ctx context.Context, session platformActorSession, change platformPublisherChange) (platformPublisherTrustRevision, error) {
	var result platformPublisherTrustRevision
	err := s.withPlatformTrustTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		index := -1
		for i, p := range trust.Publishers {
			if p.ID == change.publisherID {
				index = i
				break
			}
		}
		registering := change.kind == "register"
		if registering {
			if index != -1 || change.expectedRevision != 0 || change.key == nil || change.keyID != "" || change.expectedKeyRevision != 0 {
				return errAppPlatformTrustConflict
			}
			trust.Publishers = append(trust.Publishers, appplatform.TrustedPublisher{ID: change.publisherID, Enabled: change.enabled, Revision: "1", Namespaces: change.namespaces})
			index = len(trust.Publishers) - 1
		} else if index < 0 || change.expectedRevision <= 0 || change.expectedRevision == math.MaxInt64 || trust.Publishers[index].Revision != strconv.FormatInt(change.expectedRevision, 10) {
			return errAppPlatformTrustConflict
		}
		publisher := &trust.Publishers[index]
		var changedKey *appplatform.PublisherKey
		switch change.kind {
		case "register", "add_key":
			if change.key == nil || change.key.Revision != "1" || change.key.RevokedMS != nil || change.keyID != "" || change.expectedKeyRevision != 0 {
				return errAppPlatformTrustConflict
			}
			if !registering && (change.enabled || change.namespaces != nil) {
				return errAppPlatformTrustConflict
			}
			for _, key := range publisher.Keys {
				if key.ID == change.key.ID {
					return errAppPlatformTrustConflict
				}
			}
			publisher.Keys = append(publisher.Keys, *change.key)
			changedKey = &publisher.Keys[len(publisher.Keys)-1]
		case "policy":
			if change.key != nil || change.keyID != "" || change.expectedKeyRevision != 0 {
				return errAppPlatformTrustConflict
			}
			publisher.Enabled, publisher.Namespaces = change.enabled, change.namespaces
		case "revoke_key":
			if change.key != nil || change.enabled || change.namespaces != nil || change.expectedKeyRevision <= 0 || change.expectedKeyRevision == math.MaxInt64 {
				return errAppPlatformTrustConflict
			}
			for i := range publisher.Keys {
				if publisher.Keys[i].ID == change.keyID {
					changedKey = &publisher.Keys[i]
					break
				}
			}
			if changedKey == nil || changedKey.Revision != strconv.FormatInt(change.expectedKeyRevision, 10) {
				return errAppPlatformTrustConflict
			}
			if changedKey.RevokedMS != nil {
				result = platformPublisherTrustRevision{publisherID: publisher.ID, keyID: changedKey.ID, revision: change.expectedRevision, keyRevision: change.expectedKeyRevision}
				_, err = platformRefreshActor(e, session, actor, control)
				return err
			}
			revoked := strconv.FormatInt(control.nowMS, 10)
			changedKey.RevokedMS = &revoked
			changedKey.Revision = strconv.FormatInt(change.expectedKeyRevision+1, 10)
		default:
			return errAppPlatformTrustConflict
		}
		if !registering {
			publisher.Revision = strconv.FormatInt(change.expectedRevision+1, 10)
		}
		if err := appplatform.ValidatePublisherTrust(trust); err != nil {
			return errAppPlatformTrustConflict
		}
		namespaces := publisher.Namespaces
		if namespaces == nil {
			namespaces = []appplatform.PublisherNamespace{}
		}
		namespaceJSON, err := json.Marshal(namespaces)
		if err != nil {
			return err
		}
		if len(namespaceJSON) > 8192 {
			return errAppPlatformTrustConflict
		}
		revision, _ := strconv.ParseInt(publisher.Revision, 10, 64)
		if registering {
			_, err = e.exec(`INSERT INTO app_platform_publishers(id,namespaces_json,enabled,revision,created_ms,updated_ms) VALUES(?,?,?,?,?,?)`, publisher.ID, string(namespaceJSON), boolInt(publisher.Enabled), revision, control.nowMS, control.nowMS)
		} else {
			var updated sql.Result
			updated, err = e.exec(`UPDATE app_platform_publishers SET namespaces_json=?,enabled=?,revision=?,updated_ms=? WHERE id=? AND revision=?`, string(namespaceJSON), boolInt(publisher.Enabled), revision, control.nowMS, publisher.ID, change.expectedRevision)
			if err == nil {
				n, countErr := updated.RowsAffected()
				if countErr != nil {
					return countErr
				}
				if n != 1 {
					return errAppPlatformTrustConflict
				}
			}
		}
		if err != nil {
			return err
		}
		result = platformPublisherTrustRevision{publisherID: publisher.ID, revision: revision}
		if changedKey != nil {
			keyRevision, _ := strconv.ParseInt(changedKey.Revision, 10, 64)
			result.keyID, result.keyRevision = changedKey.ID, keyRevision
			if change.kind == "revoke_key" {
				var updated sql.Result
				updated, err = e.exec(`UPDATE app_platform_publisher_keys SET revoked_ms=?,revision=?,updated_ms=? WHERE publisher_id=? AND key_id=? AND revision=? AND revoked_ms IS NULL`, control.nowMS, keyRevision, control.nowMS, publisher.ID, changedKey.ID, change.expectedKeyRevision)
				if err == nil {
					n, countErr := updated.RowsAffected()
					if countErr != nil {
						return countErr
					}
					if n != 1 {
						return errAppPlatformTrustConflict
					}
				}
			} else {
				public, _ := base64.RawURLEncoding.Strict().DecodeString(changedKey.PublicKey)
				before, _ := strconv.ParseInt(changedKey.NotBeforeMS, 10, 64)
				var after any
				if changedKey.NotAfterMS != nil {
					n, _ := strconv.ParseInt(*changedKey.NotAfterMS, 10, 64)
					after = n
				}
				_, err = e.exec(`INSERT INTO app_platform_publisher_keys(publisher_id,key_id,algorithm,public_key,not_before_ms,not_after_ms,revoked_ms,revision,created_ms,updated_ms) VALUES(?,?,?,?,?,?,NULL,1,?,?)`, publisher.ID, changedKey.ID, changedKey.Algorithm, public, before, after, control.nowMS, control.nowMS)
			}
			if err != nil {
				return err
			}
		}
		if _, err := e.exec(`UPDATE app_platform_review_plans SET expires_ms=CASE WHEN expires_ms>? THEN ? ELSE expires_ms END,updated_ms=? WHERE publisher_id=? AND consumed_ms IS NULL`, control.nowMS, control.nowMS, control.nowMS, publisher.ID); err != nil {
			return err
		}
		quarantined := 0
		if change.kind == "policy" || change.kind == "revoke_key" {
			quarantined, err = platformQuarantinePublisherInstallations(
				e, actor, control, publisher.ID, trust)
			if err != nil {
				return err
			}
		}
		result.quarantined = quarantined
		detail, _ := json.Marshal(struct {
			Kind        string `json:"kind"`
			Revision    int64  `json:"revision"`
			KeyID       string `json:"key_id,omitempty"`
			KeyRevision int64  `json:"key_revision,omitempty"`
			Quarantined int    `json:"quarantined,omitempty"`
		}{change.kind, revision, result.keyID, result.keyRevision, quarantined})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.publisher_trust_changed','app_platform_publisher',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, publisher.ID, string(detail))
		if err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformPublisherTrustRevision{}, err
	}
	return result, nil
}

// Trust mutation first makes all existing reviews unusable. Any currently
// running installation whose signed release no longer verifies is then moved
// through the already-reviewed disable authority path in the same transaction:
// launch/broker authority is denied immediately, code and data are retained,
// and managed runtimes receive the existing stop scheduler work.
func platformQuarantinePublisherInstallations(
	e migExec,
	actor platformActorFacts,
	control platformActorControl,
	publisherID string,
	trust appplatform.PublisherTrust,
) (int, error) {
	type candidate struct {
		installation, descriptor, signature string
	}
	rows, err := e.query(`SELECT i.id,r.descriptor,r.signature
		FROM app_platform_installations i
		JOIN app_platform_activations a
		  ON a.installation_id=i.id AND a.state='active'
		JOIN app_platform_releases r ON r.sha256=a.release_sha256
		WHERE i.publisher_id=? AND i.desired_state='enabled'
		  AND i.phase='ready'
		ORDER BY i.id LIMIT 257`, publisherID)
	if err != nil {
		return 0, err
	}
	var candidates []candidate
	for rows.Next() {
		var value candidate
		if err := rows.Scan(
			&value.installation, &value.descriptor, &value.signature); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(candidates) > 256 {
		return 0, errAppPlatformTrustConflict
	}
	quarantined := 0
	for _, candidate := range candidates {
		if _, err := appplatform.VerifyPublisherDescriptor(
			[]byte(candidate.descriptor), []byte(candidate.signature), trust,
			strconv.FormatInt(control.nowMS, 10)); err == nil {
			continue
		}
		p, err := platformLoadRemovalProjection(
			e, candidate.installation, appplatform.ActionDisable,
			actor, control)
		if errors.Is(err, errAppPlatformOperationConflict) {
			// An already-pending operation is fenced by current trust. Do not
			// create a second active operation for the same installation.
			continue
		}
		if err != nil {
			return 0, err
		}
		// An installation that holds a load with no verdict cannot take a new operation yet. That must not abort
		// the revocation of every other installation: it is skipped, the reason is recorded, and launches stay
		// fenced by current trust, as for an operation that is already pending.
		if err := platformRefuseWhileLoadUnsettled(e, p.InstallationID); errors.Is(err, errAppPlatformOperationConflict) {
			detail, _ := json.Marshal(struct {
				Installation string `json:"installation_id"`
				Reason       string `json:"reason"`
			}{p.InstallationID, "unsettled_load_image"})
			if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
				VALUES(?,?,?,'app_platform.trust_quarantine_deferred','app_platform_installation',?,?,'')`,
				time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, p.InstallationID, string(detail)); err != nil {
				return 0, err
			}
			continue
		} else if err != nil {
			return 0, err
		}
		if err := admitPlatformTrustQuarantine(
			e, actor, control, p); err != nil {
			return 0, err
		}
		quarantined++
	}
	return quarantined, nil
}

func admitPlatformTrustQuarantine(
	e migExec,
	actor platformActorFacts,
	control platformActorControl,
	p platformRemovalProjection,
) error {
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 65536 ||
		control.nowMS > math.MaxInt64-900000 {
		return errAppPlatformPlanChanged
	}
	planID, err := platformNewUUID()
	if err != nil {
		return err
	}
	operationID, err := platformNewUUID()
	if err != nil {
		return err
	}
	planSHA := platformDigest(raw)
	expires := control.nowMS + 900000
	idempotency := platformDigest(
		[]byte("portal-app-trust-quarantine/v1:" + planID))
	requestFrame, _ := json.Marshal(
		[]string{"portal-operation-commit/v1", planID, planSHA})
	request := platformDigest(requestFrame)
	var source any
	if p.SourceReleaseSHA256 != "" {
		source = p.SourceReleaseSHA256
	}
	if _, err := e.exec(`INSERT INTO app_platform_review_plans(
		id,installation_id,app_id,publisher_id,action,
		source_release_sha256,target_release_sha256,installation_revision,
		policy_revision,trust_revision,controller_epoch,actor_scope,username,
		account_created,account_generation,authority_fingerprint,plan_sha256,
		plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,'disable',?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		planID, p.InstallationID, p.AppID, p.PublisherID, source,
		p.InstallationRevision, p.PolicyRevision, p.TrustRevision,
		p.ControllerEpoch, actor.scope, actor.username, actor.created,
		actor.generation, actor.authorityFingerprint, planSHA, string(raw),
		expires, control.nowMS, control.nowMS, control.nowMS); err != nil {
		return err
	}
	updated, err := e.exec(`UPDATE app_platform_installations
		SET desired_state='disabled',phase='disabled',generation=?,
		revision=revision+1,control_fence=control_fence+1,updated_ms=?
		WHERE id=? AND revision=? AND generation=? AND controller_epoch=?`,
		p.TargetGeneration, control.nowMS, p.InstallationID,
		p.InstallationRevision, p.Generation, control.epoch)
	if err := platformLeaseCAS(updated, err); err != nil {
		return err
	}
	if err := invalidatePlatformTasksForInstallation(
		e, p.InstallationID, "publisher_revoked", control.nowMS); err != nil {
		return err
	}
	if err := cancelPlatformEventSubscriptionsForInstallation(
		e, p.InstallationID, "paused", control.nowMS); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_activations
		SET state='retired',updated_ms=?
		WHERE installation_id=? AND state='active'`,
		control.nowMS, p.InstallationID); err != nil {
		return err
	}
	if p.Data != nil {
		updated, err := e.exec(`UPDATE app_platform_data_namespaces
			SET writer_fence=writer_fence+1,revision=revision+1,updated_ms=?
			WHERE id=? AND owner_installation_id=? AND revision=?
			  AND writer_fence=?`,
			control.nowMS, p.Data.ID, p.InstallationID,
			p.Data.Revision, p.Data.WriterFence)
		if err := platformLeaseCAS(updated, err); err != nil {
			return err
		}
	}
	if err := insertPlatformOperation(e, platformOperationRow{
		ID: operationID, InstallationID: p.InstallationID, ReviewPlanID: planID, ActorScope: actor.scope,
		IdempotencySHA: idempotency, RequestSHA: request, Action: "disable", Managed: p.Managed,
		SourceGeneration: p.Generation, TargetGeneration: p.TargetGeneration, Stage: "queued", StateRevision: 1,
		ControllerEpoch: control.epoch, NowMS: control.nowMS}); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Plan       string `json:"plan"`
		Action     string `json:"action"`
		Generation int64  `json:"generation"`
		Trigger    string `json:"trigger"`
	}{
		Plan: planID, Action: "disable", Generation: p.TargetGeneration,
		Trigger: "publisher_trust_quarantine",
	})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,?,?,'app_platform.operation_admitted',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339),
		actor.username, actor.ouID, operationID, string(detail)); err != nil {
		return err
	}
	return nil
}

func lockPlatformPublisherTrust(e migExec) error {
	if e.s.driver == "postgres" {
		_, err := e.exec(`LOCK TABLE app_platform_publishers,app_platform_publisher_keys IN SHARE ROW EXCLUSIVE MODE`)
		return err
	}
	_, err := e.exec(`UPDATE app_platform_publishers SET revision=revision WHERE 0`)
	return err
}

// The caller holds the trust transaction guard, so the publisher/key snapshot
// cannot mix policy revisions or miss a concurrent namespace insertion.
func loadPlatformPublisherTrust(e migExec) (appplatform.PublisherTrust, error) {
	var trust appplatform.PublisherTrust
	rows, err := e.query(`SELECT id,namespaces_json,enabled,revision FROM app_platform_publishers ORDER BY id LIMIT 129`)
	if err != nil {
		return trust, err
	}
	for rows.Next() {
		var p appplatform.TrustedPublisher
		var namespaces string
		var enabled int
		var revision int64
		if err := rows.Scan(&p.ID, &namespaces, &enabled, &revision); err != nil {
			rows.Close()
			return trust, err
		}
		p.Namespaces, err = appplatform.ParsePublisherNamespaces([]byte(namespaces))
		if err != nil {
			rows.Close()
			return trust, errAppPlatformTrustConflict
		}
		if enabled != 0 && enabled != 1 {
			rows.Close()
			return trust, errAppPlatformTrustConflict
		}
		p.Enabled, p.Revision = enabled == 1, strconv.FormatInt(revision, 10)
		trust.Publishers = append(trust.Publishers, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return trust, err
	}
	rows, err = e.query(`SELECT publisher_id,key_id,algorithm,public_key,not_before_ms,not_after_ms,revoked_ms,revision FROM app_platform_publisher_keys ORDER BY publisher_id,key_id LIMIT 257`)
	if err != nil {
		return trust, err
	}
	for rows.Next() {
		var publisher string
		var key appplatform.PublisherKey
		var public []byte
		var before, revision int64
		var after, revoked sql.NullInt64
		if err := rows.Scan(&publisher, &key.ID, &key.Algorithm, &public, &before, &after, &revoked, &revision); err != nil {
			rows.Close()
			return trust, err
		}
		key.PublicKey, key.NotBeforeMS, key.Revision = base64.RawURLEncoding.EncodeToString(public), strconv.FormatInt(before, 10), strconv.FormatInt(revision, 10)
		if after.Valid {
			v := strconv.FormatInt(after.Int64, 10)
			key.NotAfterMS = &v
		}
		if revoked.Valid {
			v := strconv.FormatInt(revoked.Int64, 10)
			key.RevokedMS = &v
		}
		found := false
		for i := range trust.Publishers {
			if trust.Publishers[i].ID == publisher {
				trust.Publishers[i].Keys = append(trust.Publishers[i].Keys, key)
				found = true
				break
			}
		}
		if !found {
			rows.Close()
			return trust, errAppPlatformTrustConflict
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return trust, err
	}
	if err := appplatform.ValidatePublisherTrust(trust); err != nil {
		return trust, errAppPlatformTrustConflict
	}
	return trust, nil
}

// These callbacks perform database-only work. Retrying a rolled-back contention
// failure must never repeat downloads, physical effects or external notifications.
func (s *Server) withPlatformTrustTransaction(session platformActorSession, fn func(migExec, platformActorFacts, platformActorControl) error) error {
	return s.withPlatformTrustTransactionContext(context.Background(), session, fn)
}

func (s *Server) withPlatformTrustTransactionContext(ctx context.Context, session platformActorSession, fn func(migExec, platformActorFacts, platformActorControl) error) error {
	for attempt := 0; ; attempt++ {
		err := s.withAppPlatformActorContext(ctx, session, false, fn)
		if err == nil || !isContentionErr(err) || attempt == 5 {
			return err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
