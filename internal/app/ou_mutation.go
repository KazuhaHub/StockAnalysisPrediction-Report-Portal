package app

import (
	"context"
	"time"
)

// lockOUMutation serializes topology, membership and entitlement mutations with OU deletion.
// It must run before the first read; a precheck outside this boundary cannot prevent a race.
func lockOUMutation(e migExec) error {
	if e.s.driver == "postgres" {
		_, err := e.exec(`SELECT pg_advisory_xact_lock(780716)`)
		return err
	}
	_, err := e.exec(`UPDATE meta SET v=v WHERE k='schema_version'`)
	return err
}

func (s *Store) withOUMutation(fn func(migExec) error) error {
	for attempt := 0; ; attempt++ {
		err := s.withOUMutationOnce(fn)
		if err == nil || !isContentionErr(err) || attempt == 5 {
			return err
		}
		time.Sleep(time.Duration(10<<attempt) * time.Millisecond)
	}
}

func (s *Store) withOUMutationOnce(fn func(migExec) error) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	if err := fn(e); err != nil {
		return err
	}
	return tx.Commit()
}
