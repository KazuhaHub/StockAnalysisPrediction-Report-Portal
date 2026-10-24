package app

import "database/sql"

// SetPrimaryGroups validates the complete batch before its first membership write. Missing
// accounts are refused, so a stale batch cannot report success for a partial destination move.
func (s *Store) SetPrimaryGroups(usernames []string, groupID int64) (map[string]int64, error) {
	before := map[string]int64{}
	err := s.withOUMutation(func(e migExec) error {
		before = map[string]int64{}
		snapshot, err := s.loadOUPolicySnapshotFrom(e.ex)
		if err != nil {
			return err
		}
		effective := groupID
		if effective == 0 {
			effective = snapshot.graph.defaultID
		}
		if _, err := snapshot.graph.policyChain(effective); err != nil {
			return err
		}
		for _, username := range usernames {
			var current int64
			if err := e.queryRow(`SELECT COALESCE(group_id,0) FROM users WHERE username=?`, username).Scan(&current); err != nil {
				return err
			}
			if current != groupID {
				before[username] = current
			}
		}
		var value any
		if groupID != 0 {
			value = groupID
		}
		for username := range before {
			result, err := e.exec(`UPDATE users SET group_id=? WHERE username=?`, value, username)
			if err != nil {
				return err
			}
			if n, err := result.RowsAffected(); err != nil {
				return err
			} else if n != 1 {
				return sql.ErrNoRows
			}
		}
		return nil
	})
	return before, err
}

func (s *Store) createOUAccount(username, hash, role, display, email, expiry string, groupID int64) error {
	return s.withOUMutation(func(e migExec) error {
		snapshot, err := s.loadOUPolicySnapshotFrom(e.ex)
		if err != nil {
			return err
		}
		effective := groupID
		if effective == 0 {
			effective = snapshot.graph.defaultID
		}
		if _, err := snapshot.graph.policyChain(effective); err != nil {
			return err
		}
		var group, expires any
		if groupID != 0 {
			group = groupID
		}
		if expiry != "" {
			expires = expiry
		}
		_, err = e.exec(`INSERT INTO users(username,password_hash,role,created_at,session_rev,login_history_initialized,
			display_name,email,expires_at,group_id) VALUES(?,?,?,?,?,1,?,?,?,?)`,
			username, hash, role, nowStr(), newSessionEpoch(), display, email, expires, group)
		return err
	})
}
