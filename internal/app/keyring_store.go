package app

import (
	"database/sql"
	"errors"
)

var errKeyringIncomplete = errors.New("keyring is incomplete; restore its matching salt and wrapped key from a compatible backup")

type keyringRecord struct {
	salt, wrapped string
	exists        bool
}

// Read both values from one statement's snapshot. Two independent setting reads
// could observe absence before a concurrent creation and presence after it.
func readStoredKeyring(e migExec) (keyringRecord, error) {
	rows, err := e.query(`SELECT k,v FROM meta WHERE k IN (?,?)`, setKeyringSalt, setKeyringDEK)
	if err != nil {
		return keyringRecord{}, err
	}
	defer rows.Close()
	var salt, wrapped sql.NullString
	var hasSalt, hasWrapped bool
	for rows.Next() {
		var key string
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return keyringRecord{}, err
		}
		if key == setKeyringSalt {
			salt, hasSalt = value, true
		} else {
			wrapped, hasWrapped = value, true
		}
	}
	if err := rows.Err(); err != nil {
		return keyringRecord{}, err
	}
	if !hasSalt && !hasWrapped {
		return keyringRecord{}, nil
	}
	if !hasSalt || !hasWrapped || !salt.Valid || !wrapped.Valid || salt.String == "" || wrapped.String == "" {
		return keyringRecord{}, errKeyringIncomplete
	}
	return keyringRecord{salt: salt.String, wrapped: wrapped.String, exists: true}, nil
}

// SaveKeyring atomically creates the pair or keeps the existing complete pair.
// A creator must re-read the winning pair before using any locally generated DEK.
func (s *Store) SaveKeyring(salt, wrappedDEK string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		// Separate from the OU lifecycle lock. Keyring callers must obtain the
		// data key before starting an application/OU mutation transaction.
		_, err = e.exec(`SELECT pg_advisory_xact_lock(780717)`)
	} else {
		_, err = e.exec(`UPDATE meta SET v=v WHERE k='schema_version'`)
	}
	if err != nil {
		return err
	}
	stored, err := readStoredKeyring(e)
	if err != nil {
		return err
	}
	if !stored.exists {
		if salt == "" || wrappedDEK == "" {
			return errKeyringIncomplete
		}
		if _, err := e.exec(`INSERT INTO meta(k,v) VALUES(?,?),(?,?)`, setKeyringSalt, salt, setKeyringDEK, wrappedDEK); err != nil {
			return err
		}
	}
	return tx.Commit()
}
