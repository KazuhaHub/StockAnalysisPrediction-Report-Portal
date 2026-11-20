package app

// Step 0022: the operations a credential binding may carry (docs 177, 178 and 180).
//
// It widens the operation list of app_platform_secret_bindings by three values together,
// acs3_hmac_sha256, pg_password and ingress_hmac_sha256. The widening is a strict superset of the list
// 0016 created, so no row changes and no table is rebuilt. Only acs3_hmac_sha256 is usable today; the
// other two are representable so that their own steps need no further schema change.

// platformLegacySecretBindingOperations is the operation list migration 0016 created. It is frozen.
func platformLegacySecretBindingOperations() string {
	return platformEnum("operation", "http_bearer", "http_api_key", "raw_read")
}

// platformSecretBindingOperations is the current operation list of secret_bindings.
func platformSecretBindingOperations() string {
	return platformEnum("operation", "http_bearer", "http_api_key", "raw_read", "acs3_hmac_sha256", "pg_password", "ingress_hmac_sha256")
}

// platformSecretBindingOperationCheckName is the PostgreSQL constraint carrying the operation list.
func platformSecretBindingOperationCheckName() (string, error) {
	return platformCheckName(platformCredentialTables(), "secret_bindings", platformSecretBindingOperations())
}

// setPlatformSecretBindingOperationCheck replaces the operation list from with to on secret_bindings,
// in place (see rewritePlatformChecks).
func setPlatformSecretBindingOperationCheck(e migExec, from, to string) error {
	name, err := platformSecretBindingOperationCheckName()
	if err != nil {
		return err
	}
	return rewritePlatformChecks(e, []platformCheckTarget{{"secret_bindings", name}}, from, to)
}

func verifyPlatformSecretOperationSchema(e migExec) error {
	return verifyPlatformSchemaProducts(e, platformCredentialTables(), platformCredentialIndexes())
}

func migration0022() migration {
	m := migration{id: "0022", restore: restoreRule{olderDumpOK: true}, verify: verifyPlatformSecretOperationSchema}
	m.up = func(e migExec) error {
		return setPlatformSecretBindingOperationCheck(e, platformLegacySecretBindingOperations(), platformSecretBindingOperations())
	}
	return m
}
