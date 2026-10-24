package app

import "fmt"

func migration0011() migration {
	return migration{
		id:      "0011",
		columns: []colRef{{table: "group_target_policies", name: "group_id"}},
		tables:  []string{"group_target_policies"},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			_, err := e.exec(`CREATE TABLE IF NOT EXISTS group_target_policies(group_id BIGINT NOT NULL PRIMARY KEY)`)
			return err
		},
		verify: verifyOUTargetPolicyHeader,
	}
}

func verifyOUTargetPolicyHeader(e migExec) error {
	query := `SELECT name,UPPER(type),"notnull",pk FROM pragma_table_info('group_target_policies')`
	if e.s.driver == "postgres" {
		query = `SELECT c.column_name,UPPER(c.data_type),CASE WHEN c.is_nullable='NO' THEN 1 ELSE 0 END,
			COALESCE((SELECT k.ordinal_position FROM information_schema.table_constraints t
			JOIN information_schema.key_column_usage k ON k.constraint_name=t.constraint_name
			AND k.table_schema=t.table_schema AND k.table_name=t.table_name
			WHERE t.table_schema='public' AND t.table_name=c.table_name
			AND t.constraint_type='PRIMARY KEY' AND k.column_name=c.column_name),0)
			FROM information_schema.columns c WHERE c.table_schema='public' AND c.table_name='group_target_policies'`
	}
	rows, err := e.query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name, typ string
		var required, key int
		if err := rows.Scan(&name, &typ, &required, &key); err != nil {
			return err
		}
		if name != "group_id" || typ != "BIGINT" || required != 1 || key != 1 {
			return fmt.Errorf("group_target_policies has an incompatible identity key")
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("group_target_policies must have exactly one identity column")
	}
	return nil
}
