package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
)

// RawMessage distinguishes an old caller omitting the field from an explicit inheritance choice.
func parseForecastScopeInput(raw json.RawMessage, isDefault bool) (*string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if isDefault {
			v := scopeNone
			return &v, nil
		}
		return nil, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("invalid forecast scope")
	}
	switch v {
	case scopeAll, scopeOwn, scopeNone:
		return &v, nil
	default:
		return nil, fmt.Errorf("invalid forecast scope")
	}
}

func (s *Store) saveGroupForecastScope(id int64, value *string) (map[string]any, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := "SELECT forecast_scope FROM user_groups WHERE id=?"
	if s.driver == "postgres" {
		query += " FOR UPDATE"
	}
	var before sql.NullString
	if err := tx.QueryRow(s.bind(query), id).Scan(&before); err != nil {
		return nil, err
	}
	var old, next any
	if before.Valid {
		old = before.String
	}
	if value != nil {
		next = *value
	}
	if old == next {
		return nil, tx.Commit()
	}
	if _, err := tx.Exec(s.bind("UPDATE user_groups SET forecast_scope=? WHERE id=?"), next, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"from": old, "to": next}, nil
}
