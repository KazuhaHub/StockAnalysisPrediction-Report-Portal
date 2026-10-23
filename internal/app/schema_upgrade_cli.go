package app

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// SchemaUpgradeCommand is the explicit, offline path for owner-approved schema changes. The default
// inspects only; it neither creates a config nor runs initialization or an automatic migration.
func SchemaUpgradeCommand(cfgPath string, args []string) (string, error) {
	apply := len(args) == 1 && args[0] == "--apply"
	if len(args) > 0 && !apply {
		return "", fmt.Errorf("usage: report-portal schema-upgrade [--apply]")
	}
	c, err := config.LoadConfig(cfgPath)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	source := c.DBSource()
	if c.DBDriver == "sqlite" || c.DBDriver == "" {
		var uri *url.URL
		if strings.HasPrefix(source, "file:") {
			uri, err = url.Parse(source)
		} else {
			var path string
			path, err = filepath.Abs(source)
			uri = &url.URL{Scheme: "file", Path: path}
		}
		if err != nil {
			return "", err
		}
		query := uri.Query()
		query.Set("mode", "ro")
		uri.RawQuery = query.Encode()
		source = uri.String()
	}
	st, err := connectStore(c.DBDriver, source)
	if err != nil {
		return "", err
	}
	defer st.Close()
	state, err := st.classifySchema()
	if err != nil {
		return "", err
	}
	if state != schemaCurrent {
		return "", fmt.Errorf("schema-upgrade requires an existing supported baseline database; use normal initialization for a new database")
	}
	if err := st.verifyBaseSchema(); err != nil {
		return "", err
	}
	known := map[string]bool{}
	var applied []migration
	var pending []string
	for _, m := range st.migrations() {
		known[m.id] = true
		if st.GetSetting(migrationLedgerKey(m.id), "") == "" {
			pending = append(pending, m.id)
		} else {
			applied = append(applied, m)
		}
	}
	if err := st.refuseUnknownLedgerRows(known); err != nil {
		return "", err
	}
	if err := st.verifyMigrationProducts(st.poolExec(), applied); err != nil {
		return "", err
	}
	if !apply {
		return fmt.Sprintf("dry run: pending schema steps [%s]; no changes applied. Stop the portal and take a backup with the previous release before schema-upgrade --apply.\n", strings.Join(pending, ", ")), nil
	}
	st.Close()
	upgraded, err := openStoreForMode(c.DBDriver, c.DBSource(), true)
	if err != nil {
		return "", err
	}
	defer upgraded.Close()
	return fmt.Sprintf("schema upgrade complete: steps [%s] verified. Restart the portal with this release.\n", strings.Join(pending, ", ")), nil
}
