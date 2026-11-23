package app

import "slices"

type platformAudienceOU struct {
	id      int64
	created string
}

// platformAudienceSnapshot loads the actual OU graph for audience decisions.
func platformAudienceSnapshot(e migExec) (ouPolicySnapshot, error) {
	// Older OU row helpers may omit the principal advisory guard. Pin their
	// existing topology/creation identities through this read transaction too.
	if e.s.driver == "postgres" {
		rows, err := e.query(`SELECT id FROM user_groups ORDER BY id FOR SHARE`)
		if err != nil {
			return ouPolicySnapshot{}, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return ouPolicySnapshot{}, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return ouPolicySnapshot{}, err
		}
	}
	return e.s.loadOUPolicySnapshotFrom(e.ex)
}

// platformAudienceChain is the actual OU chain with each OU's creation identity. It is what a viewer
// identity is derived from; it decides no access.
func platformAudienceChain(e migExec, account platformActorAccount) ([]platformAudienceOU, error) {
	snapshot, err := platformAudienceSnapshot(e)
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
		if created == "" {
			return nil, errAppPlatformLaunchUnavailable
		}
		result = append(result, platformAudienceOU{id, created})
	}
	return result, nil
}

// platformAccessChain is the policy chain (nearest OU first, the Default OU last) that governs an
// account. Every OU on it must carry a creation identity, as a viewer needs one, and a user without
// an OU is governed by the Default OU.
func platformAccessChain(e migExec, account platformActorAccount) ([]int64, error) {
	snapshot, err := platformAudienceSnapshot(e)
	if err != nil {
		return nil, err
	}
	group := account.groupID
	if group == 0 {
		group = snapshot.graph.defaultID
	}
	chain, err := snapshot.graph.policyChain(group)
	if err != nil {
		return nil, err
	}
	for _, id := range chain {
		if snapshot.graph.nodes[id].created == "" {
			return nil, errAppPlatformLaunchUnavailable
		}
	}
	return chain, nil
}

// platformLaunchAudience is the one access decision for a platform installation (doc 190, D7): the
// effective app_ou_access value of its key on the account's OU chain, closed when no row says
// otherwise. Administrators follow it like everyone else. The caller holds the OU mutation lock, which
// every write to app_ou_access also takes, so the rows read here cannot change under the decision.
func platformLaunchAudience(e migExec, account platformActorAccount, installationID string) error {
	chain, err := platformAccessChain(e, account)
	if err != nil {
		return err
	}
	return platformAudienceGrant(e, installationID, chain)
}

// platformAudienceGrant decides from an already resolved policy chain.
func platformAudienceGrant(e migExec, installationID string, chain []int64) error {
	key := platformAppKey(installationID)
	rows, err := txAppRows(e, key)
	if err != nil {
		return err
	}
	if !resolveAppAccessKey(key, chain, rows).Effective {
		return errAppPlatformLaunchUnavailable
	}
	return nil
}

// platformOpenInstallations is the sorted installation ids whose key is open on the chain, from one
// read of every platform row. Directory paging filters with it before LIMIT.
func platformOpenInstallations(e migExec, chain []int64) ([]string, error) {
	rows, err := e.query(`SELECT app_key,group_id,enabled FROM app_ou_access WHERE app_key LIKE 'platform:%' ORDER BY app_key`)
	if err != nil {
		return nil, err
	}
	byKey := map[string]map[int64]bool{}
	for rows.Next() {
		var key string
		var group int64
		var enabled int
		if err := rows.Scan(&key, &group, &enabled); err != nil {
			rows.Close()
			return nil, err
		}
		if byKey[key] == nil {
			byKey[key] = map[int64]bool{}
		}
		byKey[key][group] = enabled != 0
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var open []string
	for key, values := range byKey {
		id, ok := platformInstallationFromKey(key)
		if ok && resolveAppAccessKey(key, chain, values).Effective {
			open = append(open, id)
		}
	}
	slices.Sort(open)
	return open, nil
}

// deletePlatformAppRows removes the installation's position, OU access and app administrator rows. It runs in the same
// transaction that removes the installation, so no row outlives it.
func deletePlatformAppRows(e migExec, installation string) error {
	key := platformAppKey(installation)
	if _, err := e.exec(`DELETE FROM app_order WHERE app_key=?`, key); err != nil {
		return err
	}
	if _, err := e.exec(`DELETE FROM app_ou_access WHERE app_key=?`, key); err != nil {
		return err
	}
	return deletePlatformAppAdmins(e, installation)
}
