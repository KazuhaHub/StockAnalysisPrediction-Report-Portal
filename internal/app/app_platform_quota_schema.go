package app

// Step 0024: the per-installation data quota (doc 185 section 11, approved by the owner on 2026-11-20).
//
// It gives app_platform_data_namespaces a quota_inodes column beside quota_bytes, and widens two stored
// lists on review_plans and operations: the action list gains set_data_quota and import_data, and the
// operations stage list gains import. Every widening is a strict superset of the list before it, so no
// row changes and no table is rebuilt. import_data and the import stage are representable only: their
// executor is a later phase, and nothing accepts a plan with that action yet.

// platformQuotaCheckTargets are the stored lists step 0024 widens, with the constraint that carries each.
func platformQuotaCheckTargets() (actions, stages []platformCheckTarget, err error) {
	for _, table := range platformActionCheckedTables {
		name, nameErr := platformActionCheckName(table)
		if nameErr != nil {
			return nil, nil, nameErr
		}
		actions = append(actions, platformCheckTarget{table, name})
	}
	name, err := platformCheckName(platformSchemaTables(), "operations", platformStages())
	if err != nil {
		return nil, nil, err
	}
	return actions, []platformCheckTarget{{"operations", name}}, nil
}

func setPlatformQuotaChecks(e migExec, fromActions, toActions, fromStages, toStages string) error {
	actions, stages, err := platformQuotaCheckTargets()
	if err != nil {
		return err
	}
	if err := rewritePlatformChecks(e, actions, fromActions, toActions); err != nil {
		return err
	}
	return rewritePlatformChecks(e, stages, fromStages, toStages)
}

// rewindPlatformQuotaChecks puts the two lists back to what they were before step 0024. Tests use it to
// build a pre-0024 database.
func rewindPlatformQuotaChecks(e migExec) error {
	return setPlatformQuotaChecks(e, platformActions(), platformActions0021(), platformStages(), platformLegacyStages())
}

func verifyPlatformQuotaSchema(e migExec) error {
	var tables []platformTable
	for _, t := range platformSchemaTables() {
		if t.name == "data_namespaces" || t.name == "review_plans" || t.name == "operations" {
			tables = append(tables, t)
		}
	}
	var indexes []platformIndex
	for _, i := range platformSchemaIndexes() {
		if i.table == "data_namespaces" || i.table == "review_plans" || i.table == "operations" {
			indexes = append(indexes, i)
		}
	}
	return verifyPlatformSchemaProducts(e, tables, indexes)
}

func migration0024() migration {
	m := migration{
		id:      "0024",
		restore: restoreRule{olderDumpOK: true},
		verify:  verifyPlatformQuotaSchema,
		columns: []colRef{{platformPrefix + "data_namespaces", "quota_inodes"}},
	}
	m.up = func(e migExec) error {
		if !e.columnExists(platformPrefix+"data_namespaces", "quota_inodes") {
			if _, err := e.exec("ALTER TABLE " + platformPrefix + "data_namespaces ADD COLUMN " + platformQuotaInodesColumnSQL(e.s.driver)); err != nil {
				return err
			}
			if e.s.driver == "postgres" {
				name, err := platformCheckName(platformSchemaTables(), "data_namespaces", platformQuotaInodesCheck("quota_inodes"))
				if err != nil {
					return err
				}
				if _, err := e.exec("ALTER TABLE " + platformPrefix + "data_namespaces ADD CONSTRAINT " + name + " CHECK (" + platformQuotaInodesCheck("quota_inodes") + ")"); err != nil {
					return err
				}
			}
		}
		return setPlatformQuotaChecks(e, platformActions0021(), platformActions(), platformLegacyStages(), platformStages())
	}
	return m
}
