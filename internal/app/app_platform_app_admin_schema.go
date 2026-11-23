package app

// Step 0025: app administrators of an application's own admin page (doc 191, approved by the owner).
//
// One additive table, exactly as approved. account_created is users.created_at at the time of the
// grant: a recreated account with the same username has another created_at and so does not inherit
// the delegation. There is no foreign key; the rows of an installation are deleted with the
// installation (deletePlatformAppRows) and a user's rows with the user (Store.DeleteUser).
const platformAppAdminsDDL = `CREATE TABLE IF NOT EXISTS app_platform_app_admins(
	installation_id TEXT NOT NULL,
	username TEXT NOT NULL,
	account_created TEXT NOT NULL,
	granted_by TEXT NOT NULL,
	granted_at BIGINT NOT NULL,
	PRIMARY KEY(installation_id, username))`

// migration0025 adds the table and nothing else. A dump from before it has no such table: restoring it
// leaves the table empty, so an older dump is allowed.
func migration0025() migration {
	const table = platformPrefix + "app_admins"
	m := migration{id: "0025", tables: []string{table}, restore: restoreRule{olderDumpOK: true}}
	for _, c := range []string{"installation_id", "username", "account_created", "granted_by", "granted_at"} {
		m.columns = append(m.columns, colRef{table, c})
	}
	m.up = func(e migExec) error {
		_, err := e.exec(platformAppAdminsDDL)
		return err
	}
	return m
}
