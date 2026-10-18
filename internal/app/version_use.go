package app

import (
	"database/sql"
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

// RecordUserVersionUse records current only when it is a formal release newer than the account's
// prior high-water mark. The returned boolean is true exactly for that transition, which lets the
// caller surface release notes once per account rather than once per browser.
func (s *Store) RecordUserVersionUse(username, current string) (bool, error) {
	if !version.IsReleaseTag(current) {
		return false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	query := `SELECT COALESCE(last_used_version,'') FROM users WHERE username=?`
	if s.driver == "postgres" {
		query += ` FOR UPDATE`
	}
	var previous string
	if err := tx.QueryRow(s.bind(query), username).Scan(&previous); err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("record version use for missing user %q", username)
		}
		return false, err
	}
	if version.IsReleaseTag(previous) && !releaseTagLess(previous, current) {
		return false, nil
	}
	if _, err := tx.Exec(s.bind(`UPDATE users SET last_used_version=? WHERE username=?`), current, username); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
