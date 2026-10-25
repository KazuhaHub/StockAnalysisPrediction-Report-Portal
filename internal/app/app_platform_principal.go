package app

import "time"

// The caller holds the existing principal-lifecycle lock. Collect references
// before CASCADE/SET NULL erases them, then lock installation rows in ID order.
type platformPrincipalImpact struct{ installations, plans []string }

func appPlatformPrincipalImpacts(e migExec, username string, groupID int64) (*platformPrincipalImpact, error) {
	q := `SELECT installation_id FROM app_platform_access_grants WHERE username=?
        UNION SELECT installation_id FROM app_platform_review_plans WHERE username=? AND installation_id IS NOT NULL ORDER BY installation_id`
	args := []any{username, username}
	if username == "" {
		q = `SELECT DISTINCT installation_id FROM app_platform_access_grants WHERE group_id=? ORDER BY installation_id`
		args = []any{groupID}
	}
	rows, err := e.query(q, args...)
	if err != nil {
		return nil, err
	}
	impact := &platformPrincipalImpact{}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if username != "" {
		rows, err := e.query(`SELECT id FROM app_platform_review_plans WHERE username=? ORDER BY id`, username)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			impact.plans = append(impact.plans, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if e.s.driver == "postgres" {
		for _, id := range ids {
			var locked string
			if err := e.queryRow(`SELECT id FROM app_platform_installations WHERE id=? FOR UPDATE`, id).Scan(&locked); err != nil {
				return nil, err
			}
		}
	}
	impact.installations = ids
	return impact, nil
}

func invalidateAppPlatformPrincipal(e migExec, impact *platformPrincipalImpact) error {
	now := time.Now().UTC().UnixMilli()
	for _, id := range impact.installations {
		if _, err := e.exec(`UPDATE app_platform_installations SET policy_revision=policy_revision+1,revision=revision+1,updated_ms=? WHERE id=?`, now, id); err != nil {
			return err
		}
		if _, err := e.exec(`UPDATE app_platform_activations SET policy_revision=(SELECT policy_revision FROM app_platform_installations WHERE id=?),updated_ms=? WHERE installation_id=? AND state='active'`, id, now, id); err != nil {
			return err
		}
		if _, err := e.exec(`UPDATE app_platform_review_plans SET expires_ms=CASE WHEN expires_ms>? THEN ? ELSE expires_ms END,updated_ms=? WHERE installation_id=?`, now, now, now, id); err != nil {
			return err
		}
	}
	for _, id := range impact.plans {
		if _, err := e.exec(`UPDATE app_platform_review_plans SET expires_ms=CASE WHEN expires_ms>? THEN ? ELSE expires_ms END,updated_ms=? WHERE id=?`, now, now, now, id); err != nil {
			return err
		}
	}
	return nil
}
