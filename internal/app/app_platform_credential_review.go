package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

const platformCredentialReviewFormat = "portal.credential-rotation-review/1-dev.1"

type platformCredentialBindingImpact struct {
	ID           string `json:"id"`
	Installation string `json:"installation_id"`
	Revision     int64  `json:"revision"`
	State        string `json:"state"`
}

type platformCredentialReviewPlan struct {
	Format             string                            `json:"format"`
	Action             string                            `json:"action"`
	CredentialID       string                            `json:"credential_id"`
	CredentialLabel    string                            `json:"credential_label"`
	CredentialRevision int64                             `json:"credential_revision"`
	ValueRevision      int64                             `json:"value_revision"`
	Bindings           []platformCredentialBindingImpact `json:"bindings"`
	ActorAuthority     string                            `json:"actor_authority"`
}

type platformCredentialReview struct {
	ID, SHA256, JSON string
	ExpiresMS        int64
	Direct           bool
	Affected         []platformCredentialBindingImpact
}

func parsePlatformCredentialReview(raw string) (platformCredentialReviewPlan, error) {
	var plan platformCredentialReviewPlan
	if len(raw) == 0 || len(raw) > 65536 || json.Unmarshal([]byte(raw), &plan) != nil {
		return plan, errAppPlatformCredentialUnavailable
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) || plan.Format != platformCredentialReviewFormat || (plan.Action != "replace" && plan.Action != "revoke") || !platformValidUUID(plan.CredentialID) || !validPlatformCredentialLabel(plan.CredentialLabel) || plan.CredentialRevision < 1 || plan.ValueRevision < 1 || !platformValidSHA(plan.ActorAuthority) || len(plan.Bindings) > 256 {
		return platformCredentialReviewPlan{}, errAppPlatformCredentialUnavailable
	}
	seen := map[string]bool{}
	for i, binding := range plan.Bindings {
		if !platformValidUUID(binding.ID) || !platformValidUUID(binding.Installation) || binding.Revision < 1 || (binding.State != "active" && binding.State != "review_required") || seen[binding.ID] || i > 0 && plan.Bindings[i-1].ID >= binding.ID {
			return platformCredentialReviewPlan{}, errAppPlatformCredentialUnavailable
		}
		seen[binding.ID] = true
	}
	return plan, nil
}

func (s *Server) previewPlatformCredentialChange(session platformActorSession, id string, revision, valueRevision int64, action string) (platformCredentialReview, error) {
	var result platformCredentialReview
	if !platformValidUUID(id) || revision < 1 || valueRevision < 1 || (action != "replace" && action != "revoke") {
		return result, errPlatformManagementInput
	}
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		rows, err := e.query(`SELECT id,installation_id,revision,state FROM app_platform_secret_bindings WHERE credential_id=? AND state<>'revoked' ORDER BY id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b platformCredentialBindingImpact
			if err := rows.Scan(&b.ID, &b.Installation, &b.Revision, &b.State); err != nil {
				rows.Close()
				return err
			}
			if b.Revision < 1 || b.Revision == math.MaxInt64 {
				rows.Close()
				return errAppPlatformCredentialUnavailable
			}
			result.Affected = append(result.Affected, b)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(result.Affected) > 256 {
			return errAppPlatformCredentialUnavailable
		}
		if len(result.Affected) == 0 {
			var count int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_credentials WHERE id=? AND state='active' AND revision=? AND value_revision=?`, id, revision, valueRevision).Scan(&count); err != nil || count != 1 {
				return errAppPlatformCredentialUnavailable
			}
			result.Direct = true
			return nil
		}
		installationIDs := make([]string, 0, len(result.Affected))
		seenInstallations := map[string]bool{}
		for _, binding := range result.Affected {
			if !seenInstallations[binding.Installation] {
				seenInstallations[binding.Installation] = true
				installationIDs = append(installationIDs, binding.Installation)
			}
		}
		sort.Strings(installationIDs)
		for _, installation := range installationIDs {
			var current int64
			if err := e.queryRow(`SELECT revision FROM app_platform_installations WHERE id=? AND desired_state<>'removed'`+lock, installation).Scan(&current); err != nil || current < 1 {
				return errAppPlatformCredentialUnavailable
			}
		}
		var label, state string
		var currentRevision, currentValueRevision int64
		if err := e.queryRow(`SELECT label,state,revision,value_revision FROM app_platform_credentials WHERE id=?`+lock, id).Scan(&label, &state, &currentRevision, &currentValueRevision); err != nil || state != "active" || currentRevision != revision || currentValueRevision != valueRevision {
			return errAppPlatformCredentialUnavailable
		}
		representative := result.Affected[0].Installation
		var appID, publisherID, release, epoch string
		if err := e.queryRow(`SELECT i.app_id,i.publisher_id,a.release_sha256,i.controller_epoch FROM app_platform_installations i JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active' WHERE i.id=?`, representative).Scan(&appID, &publisherID, &release, &epoch); err != nil || !platformValidSHA(release) || !platformValidUUID(epoch) {
			return errAppPlatformCredentialUnavailable
		}
		plan := platformCredentialReviewPlan{Format: platformCredentialReviewFormat, Action: action, CredentialID: id, CredentialLabel: label, CredentialRevision: revision, ValueRevision: valueRevision, Bindings: result.Affected, ActorAuthority: actor.authorityFingerprint}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 {
			return errAppPlatformCredentialUnavailable
		}
		planID, err := platformNewUUID()
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		expires := control.nowMS + platformSecretReviewLifetime.Milliseconds()
		digest := platformDigest(raw)
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'update',?,NULL,1,1,1,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			planID, representative, appID, publisherID, release, epoch, actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result.ID, result.SHA256, result.JSON, result.ExpiresMS = planID, digest, string(raw), expires
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	return result, err
}

func (s *Server) consumePlatformCredentialReview(session platformActorSession, planID, planSHA string, value []byte) (platformCredentialMetadata, error) {
	var result platformCredentialMetadata
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) || len(value) > platformCredentialValueLimit {
		return result, errPlatformManagementInput
	}
	value = append([]byte(nil), value...)
	defer clear(value)
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var raw, digest, scope, username, created, authority string
		var expires int64
		var accountGeneration int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT plan_json,plan_sha256,expires_ms,consumed_ms,actor_scope,username,account_created,account_generation,authority_fingerprint FROM app_platform_review_plans WHERE id=?`+lock, planID).Scan(&raw, &digest, &expires, &consumed, &scope, &username, &created, &accountGeneration, &authority); err != nil {
			return errAppPlatformPlanChanged
		}
		control, err := platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if consumed.Valid || expires <= control.nowMS || accountGeneration < 0 || digest != planSHA || platformDigest([]byte(raw)) != planSHA || scope != actor.scope || username != actor.username || created != actor.created || accountGeneration != actor.generation || authority != actor.authorityFingerprint {
			return errAppPlatformPlanChanged
		}
		plan, err := parsePlatformCredentialReview(raw)
		if err != nil || plan.ActorAuthority != authority {
			return errAppPlatformPlanChanged
		}
		installationIDs := map[string]bool{}
		for _, b := range plan.Bindings {
			installationIDs[b.Installation] = true
		}
		orderedInstallations := make([]string, 0, len(installationIDs))
		for installation := range installationIDs {
			orderedInstallations = append(orderedInstallations, installation)
		}
		sort.Strings(orderedInstallations)
		for _, installation := range orderedInstallations {
			var current int64
			installationLock := ""
			if e.s.driver == "postgres" {
				installationLock = " FOR UPDATE"
			}
			if err := e.queryRow(`SELECT revision FROM app_platform_installations WHERE id=? AND desired_state<>'removed'`+installationLock, installation).Scan(&current); err != nil || current < 1 {
				return errAppPlatformPlanChanged
			}
		}
		var label, state string
		var revision, valueRevision, createdMS int64
		if err := e.queryRow(`SELECT label,state,revision,value_revision,created_ms FROM app_platform_credentials WHERE id=?`+lock, plan.CredentialID).Scan(&label, &state, &revision, &valueRevision, &createdMS); err != nil || state != "active" || label != plan.CredentialLabel || revision != plan.CredentialRevision || valueRevision != plan.ValueRevision {
			return errAppPlatformPlanChanged
		}
		for _, expected := range plan.Bindings {
			var currentRevision int64
			var currentState string
			if err := e.queryRow(`SELECT revision,state FROM app_platform_secret_bindings WHERE id=? AND installation_id=? AND credential_id=?`+lock, expected.ID, expected.Installation, plan.CredentialID).Scan(&currentRevision, &currentState); err != nil || currentRevision != expected.Revision || currentState != expected.State {
				return errAppPlatformPlanChanged
			}
		}
		if plan.Action == "replace" && len(value) == 0 {
			return errAppPlatformCredentialUnavailable
		}
		result = platformCredentialMetadata{ID: plan.CredentialID, Label: label, Revision: revision + 1, ValueRevision: valueRevision + 1, CreatedMS: createdMS, UpdatedMS: control.nowMS}
		if plan.Action == "revoke" {
			result.State = "revoked"
			if _, err := e.exec(`UPDATE app_platform_credentials SET state='revoked',sealed_value=NULL,revision=?,value_revision=?,updated_ms=?,revoked_ms=? WHERE id=?`, result.Revision, result.ValueRevision, control.nowMS, control.nowMS, result.ID); err != nil {
				return err
			}
			updated, err := e.exec(`UPDATE app_platform_secret_bindings SET state='revoked',revision=revision+1,updated_ms=?,revoked_ms=? WHERE credential_id=? AND state<>'revoked'`, control.nowMS, control.nowMS, result.ID)
			if err != nil {
				return err
			}
			if affected, err := updated.RowsAffected(); err != nil || affected != int64(len(plan.Bindings)) {
				return errAppPlatformPlanChanged
			}
		} else {
			result.State = "active"
			sealed, err := s.sealPlatformCredentialValue(result.ID, result.ValueRevision, value)
			if err != nil {
				return err
			}
			if _, err := e.exec(`UPDATE app_platform_credentials SET sealed_value=?,revision=?,value_revision=?,updated_ms=? WHERE id=?`, sealed, result.Revision, result.ValueRevision, control.nowMS, result.ID); err != nil {
				return err
			}
			updated, err := e.exec(`UPDATE app_platform_secret_bindings SET state='review_required',revision=revision+1,updated_ms=? WHERE credential_id=? AND state<>'revoked'`, control.nowMS, result.ID)
			if err != nil {
				return err
			}
			if affected, err := updated.RowsAffected(); err != nil || affected != int64(len(plan.Bindings)) {
				return errAppPlatformPlanChanged
			}
		}
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND plan_sha256=? AND expires_ms>?`, control.nowMS, control.nowMS, planID, planSHA, control.nowMS)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		detail, _ := json.Marshal(struct {
			Action        string `json:"action"`
			Revision      int64  `json:"revision"`
			ValueRevision int64  `json:"value_revision"`
			Bindings      int    `json:"bindings"`
		}{plan.Action, result.Revision, result.ValueRevision, len(plan.Bindings)})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.credential_changed','app_platform_credential',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, result.ID, string(detail)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformCredentialMetadata{}, err
	}
	return result, nil
}

func (s *Server) directPlatformCredentialChange(session platformActorSession, id string, revision, valueRevision int64, action string, value []byte) (platformCredentialMetadata, error) {
	change := platformCredentialChange{kind: action, id: id, revision: revision, valueRevision: valueRevision, value: value}
	if action == "replace" {
		return s.changePlatformCredential(session, change)
	}
	if action == "revoke" {
		return s.changePlatformCredential(session, change)
	}
	return platformCredentialMetadata{}, errors.New("invalid credential action")
}
