package app

import (
	"database/sql"
	"errors"
	"time"
)

var (
	errExtensionNotFound   = errors.New("extension connection not found")
	errExtensionMissingApp = errors.New("extension app is not installed")
	errExtensionConflict   = errors.New("extension connection revision conflict")
)

// extensionConnection is private storage. Administrative DTOs must omit Secret.
type extensionConnection struct {
	AppID, InstanceID, Label, BaseURL, Audience, Secret, Policy string
	Enabled                                                     bool
	Revision, CreatedAt, UpdatedAt                              int64
}

func migration0008() migration {
	columns := []colRef{}
	for _, name := range []string{"app_id", "instance_id", "label", "base_url", "audience", "secret", "policy", "enabled", "revision", "created_at", "updated_at"} {
		columns = append(columns, colRef{"app_backend_connections", name})
	}
	return migration{id: "0008", tables: []string{"app_backend_connections"}, columns: columns, restore: restoreRule{olderDumpOK: true}, up: func(e migExec) error {
		_, err := e.exec(`CREATE TABLE IF NOT EXISTS app_backend_connections (
   app_id TEXT PRIMARY KEY,instance_id TEXT NOT NULL,label TEXT NOT NULL,
   base_url TEXT NOT NULL,audience TEXT NOT NULL,secret TEXT NOT NULL,policy TEXT NOT NULL,
   enabled INTEGER NOT NULL DEFAULT 0,revision BIGINT NOT NULL DEFAULT 1,
   created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`)
		return err
	}}
}

func (s *Store) extensionConnection(id string) (extensionConnection, error) {
	var c extensionConnection
	var enabled int
	err := s.queryRow(`SELECT app_id,instance_id,label,base_url,audience,secret,policy,enabled,revision,created_at,updated_at FROM app_backend_connections WHERE app_id=?`, id).Scan(&c.AppID, &c.InstanceID, &c.Label, &c.BaseURL, &c.Audience, &c.Secret, &c.Policy, &enabled, &c.Revision, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, errExtensionNotFound
	}
	c.Enabled = enabled == 1
	return c, err
}

// saveExtensionConnection accepts validated, sealed input. No network or pooled
// keyring operation belongs inside this transaction (SQLite has one connection).
func (s *Store) saveExtensionConnection(c extensionConnection, expected int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	var installed string
	// Lock the installation row on PostgreSQL so uninstall cannot race a save.
	q := `SELECT id FROM apps WHERE id=?`
	if s.driver == "postgres" {
		q += ` FOR UPDATE`
	}
	if err := e.queryRow(q, c.AppID).Scan(&installed); errors.Is(err, sql.ErrNoRows) {
		return errExtensionMissingApp
	} else if err != nil {
		return err
	}
	if c.InstanceID == "" || c.Secret == "" || expected < 0 {
		return errExtensionConflict
	}
	now := time.Now().UTC().UnixMilli()
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	var result sql.Result
	if expected == 0 {
		result, err = e.exec(`INSERT INTO app_backend_connections(app_id,instance_id,label,base_url,audience,secret,policy,enabled,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,1,?,?) ON CONFLICT(app_id) DO NOTHING`, c.AppID, c.InstanceID, c.Label, c.BaseURL, c.Audience, c.Secret, c.Policy, enabled, now, now)
	} else {
		result, err = e.exec(`UPDATE app_backend_connections SET label=?,base_url=?,audience=?,secret=?,policy=?,enabled=?,revision=revision+1,updated_at=? WHERE app_id=? AND instance_id=? AND revision=?`, c.Label, c.BaseURL, c.Audience, c.Secret, c.Policy, enabled, now, c.AppID, c.InstanceID, expected)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errExtensionConflict
	}
	return tx.Commit()
}

func (s *Store) deleteExtensionConnection(id string, revision int64) error {
	result, err := s.exec(`DELETE FROM app_backend_connections WHERE app_id=? AND revision=?`, id, revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errExtensionConflict
	}
	return nil
}

// resetExtensionConnection destroys the old pairing before changing its namespace.
// Re-pairing deliberately creates a new identity seed; old grants become inert.
func resetExtensionConnection(e migExec, id string) error {
	_, err := e.exec(`UPDATE app_backend_connections SET enabled=0,secret='',instance_id=?,revision=revision+1,updated_at=? WHERE app_id=?`, randToken(), time.Now().UTC().UnixMilli(), id)
	return err
}

func resetRestoredExtensionConnections(e migExec) error {
	rows, err := e.query(`SELECT app_id FROM app_backend_connections`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := resetExtensionConnection(e, id); err != nil {
			return err
		}
	}
	return nil
}
