package app

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// SchemaUpgradeCommand is an optional offline tool; no deploy needs it. Every schema step is applied
// by ordinary startup (ADR 0035), so a release is a new binary and a restart. This command exists for
// an operator who wants to see what a start would apply, or to apply it while the portal is stopped.
//
// With no arguments it only inspects: it lists the pending steps, and it neither creates a config or
// a database nor writes to the one it reads (a SQLite file is opened read-only). With --apply it runs
// the same runner startup does, then verifies every step's declared products.
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
		return "", fmt.Errorf("schema-upgrade works on an existing supported database; a new database is created by normal startup")
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
	if len(pending) == 0 {
		if apply {
			return "no pending schema steps; nothing to apply.\n", nil
		}
		return "no pending schema steps; the database is up to date.\n", nil
	}
	list := strings.Join(pending, ", ")
	if !apply {
		return fmt.Sprintf("pending schema steps [%s]; nothing was changed. The portal applies them itself at its next start; to apply them offline instead, stop the portal and run schema-upgrade --apply.\n", list), nil
	}
	st.Close()
	upgraded, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return "", err
	}
	defer upgraded.Close()
	if err := upgraded.verifyMigrationProducts(upgraded.poolExec(), upgraded.migrations()); err != nil {
		return "", err
	}
	return fmt.Sprintf("schema upgrade complete: steps [%s] applied and verified.\n", list), nil
}
