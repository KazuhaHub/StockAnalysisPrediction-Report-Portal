package app

import (
	"database/sql"
	"errors"
)

// Restore and machine tokens (doc 183 section 6.5). A token that is revoked in the live database
// before a restore stays revoked afterwards, with the earlier revocation time: a backup must not
// bring a revoked credential back. Tokens created after the backup are lost; the restore summary
// says how many.

type platformMachineTokenFloor struct {
	id, installation, handle, label, secret, routes, cidrs, createdBy string
	perMinute, policyRevision, revision, expires, created, updated    int64
	revoked                                                           sql.NullInt64
	lastUsed                                                          sql.NullInt64
}

func capturePlatformMachineTokens(e migExec, prior *platformRestorePrior) error {
	rows, err := e.query(`SELECT id,installation_id,handle,label,secret_sha256,routes_json,source_cidrs_json,created_by,per_minute,policy_revision,revision,expires_ms,created_ms,updated_ms,revoked_ms,last_used_ms FROM app_platform_machine_tokens ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f platformMachineTokenFloor
		var revoked sql.NullInt64
		if err := rows.Scan(&f.id, &f.installation, &f.handle, &f.label, &f.secret, &f.routes, &f.cidrs, &f.createdBy, &f.perMinute, &f.policyRevision, &f.revision, &f.expires, &f.created, &f.updated, &revoked, &f.lastUsed); err != nil {
			return err
		}
		f.revoked = revoked
		prior.machineTokens = append(prior.machineTokens, f)
	}
	return rows.Err()
}

// restorePlatformMachineTokenFloors re-applies every live revocation over the restored table and
// returns how many live tokens the restored database does not hold.
func restorePlatformMachineTokenFloors(e migExec, prior platformRestorePrior, now int64) (int, error) {
	lost := 0
	for _, f := range prior.machineTokens {
		var installation string
		err := e.queryRow(`SELECT installation_id FROM app_platform_machine_tokens WHERE id=?`, f.id).Scan(&installation)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if !f.revoked.Valid {
				// Only an active token has to be recreated: a revoked one has nothing left to authorise.
				lost++
				continue
			}
			// The token is not in the backup. Its floor is kept when its installation is, and
			// dropped with the installation when it is not: nothing is left for it to authorise.
			var present int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_installations WHERE id=?`, f.installation).Scan(&present); err != nil {
				return 0, err
			}
			if present == 0 {
				continue
			}
			if _, err := e.exec(`INSERT INTO app_platform_machine_tokens(id,installation_id,handle,label,secret_sha256,routes_json,per_minute,source_cidrs_json,state,policy_revision,created_by,revision,expires_ms,created_ms,updated_ms,revoked_ms,last_used_ms)
				VALUES(?,?,?,?,?,?,?,?,'revoked',?,?,?,?,?,?,?,?)`, f.id, f.installation, f.handle, f.label, f.secret, f.routes, f.perMinute, f.cidrs, f.policyRevision, f.createdBy, f.revision, f.expires, f.created, f.updated, f.revoked.Int64, f.lastUsed); err != nil {
				return 0, err
			}
		case err != nil:
			return 0, err
		case f.revoked.Valid:
			if _, err := e.exec(`UPDATE app_platform_machine_tokens SET state='revoked',
				revision=CASE WHEN revision<? THEN ? ELSE revision END,
				revoked_ms=CASE WHEN revoked_ms IS NULL OR revoked_ms>? THEN ? ELSE revoked_ms END,
				updated_ms=CASE WHEN updated_ms<? THEN ? ELSE updated_ms END WHERE id=?`,
				f.revision, f.revision, f.revoked.Int64, f.revoked.Int64, now, now, f.id); err != nil {
				return 0, err
			}
		}
	}
	return lost, nil
}
