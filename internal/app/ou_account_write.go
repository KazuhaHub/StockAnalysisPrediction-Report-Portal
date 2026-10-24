package app

import "strings"

type ouAccountPatch struct {
	Role         *string `json:"role"`
	Password     string  `json:"password"`
	DisplayName  *string `json:"display_name"`
	Email        *string `json:"email"`
	Active       *bool   `json:"active"`
	PrimaryGroup *int64  `json:"primary_group"`
	ExpiresAt    *string `json:"expires_at"`
}

// saveOUAccount applies a validated partial account edit under the membership writer lock.
// Password contains a prepared hash; plaintext validation and hashing happen before the transaction.
func (s *Store) saveOUAccount(name, actor, today string, in ouAccountPatch) ([]map[string]any, error) {
	var changes []map[string]any
	err := s.withOUMutation(func(e migExec) error {
		changes = nil
		u, err := scanUser(e.queryRow(`SELECT `+userCols+` FROM users u WHERE u.username=?`, name).Scan)
		if err != nil {
			return err
		}
		var admins int
		if err := e.queryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&admins); err != nil {
			return err
		}
		protected := name == actor || (u.IsAdmin() && admins <= 1)
		if in.ExpiresAt != nil && *in.ExpiresAt != "" && today > *in.ExpiresAt && protected {
			return invalidOU("invalid_expiry", "cannot set an already-passed expiry on your own or the last admin account")
		}
		var groupBefore int64
		if in.PrimaryGroup != nil {
			snapshot, err := s.loadOUPolicySnapshotFrom(e.ex)
			if err != nil {
				return err
			}
			dest := *in.PrimaryGroup
			if dest == 0 {
				dest = snapshot.graph.defaultID
			}
			if _, err := snapshot.graph.policyChain(dest); err != nil {
				return err
			}
			if err := e.queryRow(`SELECT COALESCE(group_id,0) FROM users WHERE username=?`, name).Scan(&groupBefore); err != nil {
				return err
			}
		}
		var sets []string
		var args []any
		set := func(column string, value any) {
			sets = append(sets, column+"=?")
			args = append(args, value)
		}
		if in.Role != nil {
			role := validRole(*in.Role)
			if u.IsAdmin() && admins <= 1 {
				role = "admin"
			}
			if role != u.Role {
				set("role", role)
				changes = append(changes, map[string]any{"field": "role", "from": u.Role, "to": role})
			}
		}
		if in.Password != "" {
			set("password_hash", in.Password)
			sets = append(sets, "session_rev=COALESCE(session_rev,0)+1")
		}
		if in.DisplayName != nil && strings.TrimSpace(*in.DisplayName) != u.DisplayName {
			set("display_name", strings.TrimSpace(*in.DisplayName))
		}
		if in.Email != nil && strings.TrimSpace(*in.Email) != u.Email {
			set("email", strings.TrimSpace(*in.Email))
		}
		if in.Active != nil && (*in.Active || !protected) && *in.Active != u.Active {
			set("active", boolInt(*in.Active))
		}
		if in.ExpiresAt != nil && *in.ExpiresAt != u.ExpiresAt {
			var expiry any
			if *in.ExpiresAt != "" {
				expiry = *in.ExpiresAt
			}
			set("expires_at", expiry)
		}
		if in.PrimaryGroup != nil && *in.PrimaryGroup != groupBefore {
			var group any
			if *in.PrimaryGroup != 0 {
				group = *in.PrimaryGroup
			}
			set("group_id", group)
			changes = append(changes, map[string]any{"field": "primary_group", "from": groupBefore, "to": *in.PrimaryGroup})
		}
		if len(sets) == 0 {
			return nil
		}
		args = append(args, name)
		_, err = e.exec(`UPDATE users SET `+strings.Join(sets, ",")+` WHERE username=?`, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}
