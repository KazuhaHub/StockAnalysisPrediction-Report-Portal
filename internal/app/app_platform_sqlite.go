package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"

	"modernc.org/sqlite"
)

const portalSQLiteDriver = "report-portal-sqlite"

func init() {
	d := &sqlite.Driver{}
	d.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON", nil); err != nil {
			return err
		}
		rows, err := conn.QueryContext(context.Background(), "PRAGMA foreign_keys", nil)
		if err != nil {
			return err
		}
		defer rows.Close()
		values := make([]driver.Value, 1)
		if err := rows.Next(values); err != nil && err != io.EOF {
			return err
		}
		if values[0] != int64(1) {
			return fmt.Errorf("SQLite foreign-key enforcement is unavailable")
		}
		return nil
	})
	sql.Register(portalSQLiteDriver, d)
}
