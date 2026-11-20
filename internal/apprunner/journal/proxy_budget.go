package journal

import (
	"context"
	"errors"
	"regexp"
)

// The daily byte budget of the reviewed forward proxy (doc 184 section 6), kept in the journal so that a runner
// restart does not give every installation a fresh day. A row is the bytes an installation moved through the
// proxy, both directions, in one UTC day (days since the epoch). It is accounting, never authority.

var errProxyBudget = errors.New("invalid proxy budget row")

var proxyBudgetID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ProxyBudgetUsed is the bytes counted for the installation on utcDay, zero when none.
func (s *Store) ProxyBudgetUsed(ctx context.Context, installation string, utcDay int64) (int64, error) {
	if s.closing.Load() || !proxyBudgetID.MatchString(installation) || utcDay < 0 {
		return 0, errProxyBudget
	}
	var used int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT bytes FROM runner_proxy_budget WHERE installation_id=? AND utc_day=?),0)`, installation, utcDay).Scan(&used)
	return used, err
}

// AddProxyBudget counts n more bytes for the installation on utcDay.
func (s *Store) AddProxyBudget(ctx context.Context, installation string, utcDay, n int64) error {
	if s.closing.Load() || !proxyBudgetID.MatchString(installation) || utcDay < 0 || n < 0 {
		return errProxyBudget
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runner_proxy_budget(installation_id,utc_day,bytes) VALUES(?,?,?)
		ON CONFLICT(installation_id,utc_day) DO UPDATE SET bytes=bytes+excluded.bytes`, installation, utcDay, n)
	return err
}

// PruneProxyBudget deletes the rows of every day before keepFrom, so the table holds a few days however long the
// runner has run.
func (s *Store) PruneProxyBudget(ctx context.Context, keepFrom int64) error {
	if s.closing.Load() || keepFrom < 0 {
		return errProxyBudget
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM runner_proxy_budget WHERE utc_day<?`, keepFrom)
	return err
}
