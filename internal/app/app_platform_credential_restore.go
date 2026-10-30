package app

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

type platformRevokedCredential struct {
	id, label                                          string
	valueRevision, revision, created, updated, revoked int64
}
type platformRevokedSecretBinding struct {
	id, installation, name, credential, operation, policy, review      string
	valueRevision, policyRevision, revision, created, updated, revoked int64
}

func capturePlatformCredentialFloors(e migExec, prior *platformRestorePrior) error {
	rows, err := e.query(`SELECT id,label,value_revision,revision,created_ms,updated_ms,revoked_ms FROM app_platform_credentials WHERE state='revoked' ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c platformRevokedCredential
		if err := rows.Scan(&c.id, &c.label, &c.valueRevision, &c.revision, &c.created, &c.updated, &c.revoked); err != nil {
			rows.Close()
			return err
		}
		prior.credentials = append(prior.credentials, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = e.query(`SELECT id,installation_id,name,credential_id,operation,policy_json,review_plan_id,credential_value_revision,policy_revision,revision,created_ms,updated_ms,revoked_ms FROM app_platform_secret_bindings WHERE state='revoked' ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b platformRevokedSecretBinding
		if err := rows.Scan(&b.id, &b.installation, &b.name, &b.credential, &b.operation, &b.policy, &b.review, &b.valueRevision, &b.policyRevision, &b.revision, &b.created, &b.updated, &b.revoked); err != nil {
			return err
		}
		prior.bindings = append(prior.bindings, b)
	}
	return rows.Err()
}

func restorePlatformCredentialFloors(e migExec, prior platformRestorePrior, now int64) error {
	for _, c := range prior.credentials {
		if _, err := e.exec(`INSERT INTO app_platform_credentials(id,label,state,sealed_value,value_revision,revision,created_ms,updated_ms,revoked_ms) VALUES(?,?,'revoked',NULL,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, c.id, c.label, c.valueRevision, c.revision, c.created, c.updated, c.revoked); err != nil {
			return err
		}
		if _, err := e.exec(`UPDATE app_platform_credentials SET state='revoked',sealed_value=NULL,value_revision=CASE WHEN value_revision<? THEN ? ELSE value_revision END,revision=CASE WHEN revision<? THEN ? ELSE revision END,revoked_ms=CASE WHEN revoked_ms IS NULL OR revoked_ms>? THEN ? ELSE revoked_ms END,updated_ms=CASE WHEN updated_ms<? THEN ? ELSE updated_ms END WHERE id=?`, c.valueRevision, c.valueRevision, c.revision, c.revision, c.revoked, c.revoked, now, now, c.id); err != nil {
			return err
		}
	}
	for _, b := range prior.bindings {
		var id string
		err := e.queryRow(`SELECT id FROM app_platform_secret_bindings WHERE id=?`, b.id).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			// Missing parents or a conflicting installation/name must refuse the whole
			// restore. Inventing a review or dropping a known floor would grant authority.
			if _, err := e.exec(`INSERT INTO app_platform_secret_bindings(id,installation_id,name,credential_id,credential_value_revision,operation,state,policy_json,policy_revision,review_plan_id,revision,created_ms,updated_ms,revoked_ms) VALUES(?,?,?,?,?,?,'revoked',?,?,?,?,?,?,?)`, b.id, b.installation, b.name, b.credential, b.valueRevision, b.operation, b.policy, b.policyRevision, b.review, b.revision, b.created, b.updated, b.revoked); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := e.exec(`UPDATE app_platform_secret_bindings SET state='revoked',revision=CASE WHEN revision<? THEN ? ELSE revision END,revoked_ms=CASE WHEN revoked_ms IS NULL OR revoked_ms>? THEN ? ELSE revoked_ms END,updated_ms=CASE WHEN updated_ms<? THEN ? ELSE updated_ms END WHERE id=?`, b.revision, b.revision, b.revoked, b.revoked, now, now, b.id); err != nil {
			return err
		}
	}
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_credentials`).Scan(&count); err != nil {
		return err
	}
	if count > 256 {
		return fmt.Errorf("credential restore exceeds retained custody capacity")
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM (SELECT installation_id FROM app_platform_secret_bindings GROUP BY installation_id HAVING COUNT(*)>32) AS over_capacity`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("credential restore exceeds retained binding capacity")
	}
	for _, table := range []string{"app_platform_credentials", "app_platform_secret_bindings"} {
		if err := e.queryRow(`SELECT COUNT(*) FROM `+table+` WHERE state<>'revoked' AND revision=?`, int64(math.MaxInt64)).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("credential restore revision exhausted")
		}
		// Value revision is authenticated AAD. Pausing changes authority revision,
		// never the ciphertext identity without resealing the value.
		if _, err := e.exec(`UPDATE `+table+` SET state='review_required',revision=revision+1,updated_ms=CASE WHEN updated_ms<? THEN ? ELSE updated_ms END WHERE state<>'revoked'`, now, now); err != nil {
			return err
		}
	}
	return nil
}
