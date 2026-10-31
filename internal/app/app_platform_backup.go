package app

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformRestorePublisher struct {
	id, namespaces             string
	revision, created, updated int64
}
type platformRestoreKey struct {
	publisher, id, algorithm            string
	public                              []byte
	notBefore                           int64
	notAfter                            sql.NullInt64
	revoked, revision, created, updated int64
}
type platformRestorePrior struct {
	publishers  []platformRestorePublisher
	keys        []platformRestoreKey
	credentials []platformRevokedCredential
	bindings    []platformRevokedSecretBinding
}

// Capture inside the destructive restore transaction, before clearing any rows.
// PostgreSQL table locks exclude concurrent control writes; restore remains an
// offline operator procedure, not an online data replacement API.
func capturePlatformRestorePrior(e migExec) (platformRestorePrior, error) {
	var prior platformRestorePrior
	if e.s.driver == "postgres" {
		names := append(migration0015().tables, migration0016().tables...)
		sort.Strings(names)
		if _, err := e.exec("LOCK TABLE " + strings.Join(names, ",") + " IN ACCESS EXCLUSIVE MODE"); err != nil {
			return prior, err
		}
	} else {
		if _, err := e.exec(`UPDATE app_platform_publishers SET revision=revision WHERE 0`); err != nil {
			return prior, err
		}
	}
	rows, err := e.query(`SELECT id,namespaces_json,revision,created_ms,updated_ms FROM app_platform_publishers
        WHERE enabled=0 OR id IN (SELECT publisher_id FROM app_platform_publisher_keys WHERE revoked_ms IS NOT NULL)`)
	if err != nil {
		return prior, err
	}
	for rows.Next() {
		var p platformRestorePublisher
		if err := rows.Scan(&p.id, &p.namespaces, &p.revision, &p.created, &p.updated); err != nil {
			rows.Close()
			return prior, err
		}
		prior.publishers = append(prior.publishers, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return prior, err
	}
	rows, err = e.query(`SELECT publisher_id,key_id,algorithm,public_key,not_before_ms,not_after_ms,revoked_ms,revision,created_ms,updated_ms FROM app_platform_publisher_keys WHERE revoked_ms IS NOT NULL`)
	if err != nil {
		return prior, err
	}
	for rows.Next() {
		var k platformRestoreKey
		if err := rows.Scan(&k.publisher, &k.id, &k.algorithm, &k.public, &k.notBefore, &k.notAfter, &k.revoked, &k.revision, &k.created, &k.updated); err != nil {
			rows.Close()
			return prior, err
		}
		prior.keys = append(prior.keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return prior, err
	}
	err = capturePlatformCredentialFloors(e, &prior)
	return prior, err
}

func applyPlatformRestore(e migExec, prior platformRestorePrior) error {
	now := time.Now().UTC().UnixMilli()
	for _, p := range prior.publishers {
		if _, err := e.exec(`INSERT INTO app_platform_publishers(id,namespaces_json,enabled,revision,created_ms,updated_ms) VALUES(?,?,0,?,?,?) ON CONFLICT(id) DO NOTHING`, p.id, p.namespaces, p.revision, p.created, p.updated); err != nil {
			return err
		}
	}
	for _, k := range prior.keys {
		var algorithm string
		var public []byte
		err := e.queryRow(`SELECT algorithm,public_key FROM app_platform_publisher_keys WHERE publisher_id=? AND key_id=?`, k.publisher, k.id).Scan(&algorithm, &public)
		if err == sql.ErrNoRows {
			if _, err := e.exec(`INSERT INTO app_platform_publisher_keys(publisher_id,key_id,algorithm,public_key,not_before_ms,not_after_ms,revoked_ms,revision,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, k.publisher, k.id, k.algorithm, k.public, k.notBefore, k.notAfter, k.revoked, k.revision, k.created, k.updated); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if algorithm != k.algorithm || !bytes.Equal(public, k.public) {
				return fmt.Errorf("restored publisher key conflicts with a retained revocation")
			}
			if _, err := e.exec(`UPDATE app_platform_publisher_keys SET revoked_ms=CASE WHEN revoked_ms IS NULL OR revoked_ms>? THEN ? ELSE revoked_ms END,
                revision=CASE WHEN revision<? THEN ? ELSE revision END,updated_ms=? WHERE publisher_id=? AND key_id=?`, k.revoked, k.revoked, k.revision, k.revision, now, k.publisher, k.id); err != nil {
				return err
			}
		}
	}
	if err := restorePlatformCredentialFloors(e, prior, now); err != nil {
		return err
	}
	if err := verifyPlatformRestoreArtifacts(e); err != nil {
		return err
	}
	var entities int
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_installations)+(SELECT COUNT(*) FROM app_platform_runtime_bindings)+(SELECT COUNT(*) FROM app_platform_publishers)+(SELECT COUNT(*) FROM app_platform_credentials)`).Scan(&entities); err != nil {
		return err
	}
	if entities == 0 {
		return nil
	}
	epoch, err := platformNewUUID()
	if err != nil {
		return err
	}
	statements := []struct {
		q    string
		args []any
	}{
		{`UPDATE app_platform_publishers SET enabled=0,revision=revision+1,updated_ms=?`, []any{now}},
		{`UPDATE app_platform_activations SET state='retired',updated_ms=?`, []any{now}},
		{`UPDATE app_platform_installations SET desired_state=CASE WHEN desired_state='removed' THEN 'removed' ELSE 'disabled' END,
            phase=CASE WHEN desired_state='removed' THEN 'removed' ELSE 'review_required' END,generation=generation+1,revision=revision+1,
            policy_revision=policy_revision+1,control_fence=control_fence+1,controller_epoch=?,updated_ms=?`, []any{epoch, now}},
		{`UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL,verification_key_fingerprint=NULL,
            controller_epoch=?,control_revision=control_revision+1,revision=revision+1,updated_ms=?`, []any{epoch, now}},
		{`UPDATE app_platform_operations SET stage='recovery_required',state_revision=state_revision+1,row_revision=row_revision+1,
            lease_owner=NULL,lease_until_ms=NULL,error_code='restore_review_required',completed_ms=?,updated_ms=? WHERE completed_ms IS NULL`, []any{now, now}},
		{`UPDATE app_platform_data_namespaces SET state='review_required',owner_installation_id=NULL,writer_fence=writer_fence+1,revision=revision+1,updated_ms=? WHERE state<>'deleted'`, []any{now}},
		{`UPDATE app_platform_recovery_sets SET state='review_required',controller_epoch=?,updated_ms=? WHERE state<>'deleted'`, []any{epoch, now}},
		{`UPDATE app_platform_review_plans SET expires_ms=CASE WHEN expires_ms>? THEN ? ELSE expires_ms END,updated_ms=?`, []any{now, now, now}},
		{`UPDATE app_platform_operation_effects SET state='unknown',updated_ms=? WHERE state='prepared'`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := e.exec(statement.q, statement.args...); err != nil {
			return err
		}
	}
	if err := e.setSetting("app_platform_controller_epoch", epoch); err != nil {
		return err
	}
	if err := e.setSetting("app_platform_restore_paused", "1"); err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?, 'system',0,'app_platform.restore_paused','app_platform',?,'{}','')`, time.Now().UTC().Format(time.RFC3339), epoch)
	return err
}

func verifyPlatformRestoreArtifacts(e migExec) error {
	rows, err := e.query(`SELECT sha256,archive_bytes,content FROM app_platform_frontend_artifacts`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var digest string
		var size int64
		var content []byte
		if err := rows.Scan(&digest, &size, &content); err != nil {
			rows.Close()
			return err
		}
		sum := sha256.Sum256(content)
		if size != int64(len(content)) || len(content) > appplatform.MaxFrontendArchiveBytes || hex.EncodeToString(sum[:]) != digest {
			rows.Close()
			return fmt.Errorf("restored frontend artifact integrity is invalid")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = e.query(`SELECT r.sha256,r.app_id,r.publisher_id,r.version,r.descriptor,r.frontend_sha256,r.managed,f.content
        FROM app_platform_releases r LEFT JOIN app_platform_frontend_artifacts f ON f.sha256=r.frontend_sha256`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var digest, id, publisher, version, frontend string
		var raw, content []byte
		var managed int
		if err := rows.Scan(&digest, &id, &publisher, &version, &raw, &frontend, &managed, &content); err != nil {
			return err
		}
		parsed, err := appplatform.ParseDescriptor(raw)
		if err != nil || parsed.SHA256 != digest || parsed.Manifest.ID != id || parsed.Manifest.Publisher != publisher || parsed.Manifest.Version != version || parsed.Manifest.Frontend.SHA256 != frontend || boolInt(parsed.Manifest.Backend != nil) != managed {
			return fmt.Errorf("restored application release integrity is invalid")
		}
		if _, err := appplatform.InspectFrontend(parsed.Manifest, content); err != nil {
			return fmt.Errorf("restored application frontend inspection failed")
		}
	}
	return rows.Err()
}

func platformNewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
