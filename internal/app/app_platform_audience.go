package app

import (
	"database/sql"
	"errors"
)

type platformAudienceOU struct {
	id      int64
	created string
}

func platformAudienceChain(e migExec, account platformActorAccount) ([]platformAudienceOU, error) {
	// Older OU row helpers may omit the principal advisory guard. Pin their
	// existing topology/creation identities through this read transaction too.
	if e.s.driver == "postgres" {
		rows, err := e.query(`SELECT id FROM user_groups ORDER BY id FOR SHARE`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	snapshot, err := e.s.loadOUPolicySnapshotFrom(e.ex)
	if err != nil {
		return nil, err
	}
	group := account.groupID
	if group == 0 {
		group = snapshot.graph.defaultID
	}
	chain, err := snapshot.graph.actualChain(group)
	if err != nil {
		return nil, err
	}
	result := make([]platformAudienceOU, 0, len(chain))
	for _, id := range chain {
		created := snapshot.graph.nodes[id].created
		if created != "" {
			result = append(result, platformAudienceOU{id, created})
		}
	}
	return result, nil
}
func platformLaunchAudience(e migExec, account platformActorAccount, installationID string) error {
	chain, err := platformAudienceChain(e, account)
	if err != nil {
		return err
	}
	return platformAudienceGrant(e, account, installationID, chain)
}
func platformAudienceGrant(e migExec, account platformActorAccount, installationID string, chain []platformAudienceOU) error {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	var grant string
	err := e.queryRow(`SELECT id FROM app_platform_access_grants WHERE installation_id=? AND principal_type='user' AND username=? AND account_created=? AND account_generation=? LIMIT 1`+lock, installationID, account.username, account.created, account.generation).Scan(&grant)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Actual ancestors only; Default policy fallback never creates an audience.
	for _, group := range chain {
		err = e.queryRow(`SELECT id FROM app_platform_access_grants WHERE installation_id=? AND principal_type='ou' AND group_id=? AND group_created=? LIMIT 1`+lock, installationID, group.id, group.created).Scan(&grant)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return errAppPlatformLaunchUnavailable
}
