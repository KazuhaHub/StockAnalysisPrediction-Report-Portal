package app

import "math"

// Runs inside current-admin reviewed uninstall admission, after installation
// intent/generation denial and before operation/audit commit. The caller holds
// the principal and installation locks. Shared credential custody is untouched.
func revokePlatformUninstallSecretBindings(e migExec, installation string, now int64) (int, error) {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	rows, err := e.query(`SELECT revision FROM app_platform_secret_bindings WHERE installation_id=? AND state<>'revoked' ORDER BY id`+lock, installation)
	if err != nil {
		return 0, err
	}
	count := 0
	for rows.Next() {
		var revision int64
		if err := rows.Scan(&revision); err != nil {
			rows.Close()
			return 0, err
		}
		count++
		if count > 32 || revision < 1 || revision == math.MaxInt64 {
			rows.Close()
			return 0, errAppPlatformCredentialUnavailable
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	updated, err := e.exec(`UPDATE app_platform_secret_bindings SET state='revoked',revision=revision+1,revoked_ms=?,updated_ms=CASE WHEN updated_ms<? THEN ? ELSE updated_ms END WHERE installation_id=? AND state<>'revoked'`, now, now, now, installation)
	if err != nil {
		return 0, err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != int64(count) {
		return 0, errAppPlatformCredentialUnavailable
	}
	return count, nil
}
