package app

import "fmt"

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
	m := migration{id: "0025", tables: []string{table}, restore: restoreRule{olderDumpOK: true}, verify: verifyPlatformAppAdminsSchema}
	for _, c := range []string{"installation_id", "username", "account_created", "granted_by", "granted_at"} {
		m.columns = append(m.columns, colRef{table, c})
	}
	m.up = func(e migExec) error {
		_, err := e.exec(platformAppAdminsDDL)
		return err
	}
	return m
}

// verifyPlatformAppAdminsSchema refuses a pre-existing table of another shape: CREATE TABLE IF NOT EXISTS
// would leave it alone and the step would record itself over it. The shape is the approved one: five
// NOT NULL columns of the declared types, primary key (installation_id, username) in that order, nothing else.
func verifyPlatformAppAdminsSchema(e migExec) error {
	query := `SELECT name,UPPER(type),"notnull",pk FROM pragma_table_info('app_platform_app_admins')`
	if e.s.driver == "postgres" {
		query = `SELECT c.column_name,UPPER(c.data_type),CASE WHEN c.is_nullable='NO' THEN 1 ELSE 0 END,
			COALESCE((SELECT k.ordinal_position FROM information_schema.table_constraints t
			JOIN information_schema.key_column_usage k ON k.constraint_name=t.constraint_name
			AND k.table_schema=t.table_schema AND k.table_name=t.table_name
			WHERE t.table_schema='public' AND t.table_name=c.table_name
			AND t.constraint_type='PRIMARY KEY' AND k.column_name=c.column_name),0)
			FROM information_schema.columns c WHERE c.table_schema='public' AND c.table_name='app_platform_app_admins'`
	}
	rows, err := e.query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := map[string]string{"installation_id": "TEXT:1:1", "username": "TEXT:1:2", "account_created": "TEXT:1:0", "granted_by": "TEXT:1:0", "granted_at": "BIGINT:1:0"}
	n := 0
	for rows.Next() {
		var name, typ string
		var required, key int
		if err := rows.Scan(&name, &typ, &required, &key); err != nil {
			return err
		}
		n++
		if want[name] != fmt.Sprintf("%s:%d:%d", typ, required, key) {
			return fmt.Errorf("app_platform_app_admins has an incompatible definition of %s", name)
		}
		delete(want, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(want) != 0 || n != 5 {
		return fmt.Errorf("app_platform_app_admins does not have exactly the approved columns")
	}
	return nil
}
