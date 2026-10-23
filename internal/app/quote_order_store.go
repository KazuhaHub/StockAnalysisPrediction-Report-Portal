package app

import (
	"context"
	"fmt"
	"strings"
)

func effectiveQuoteOrder(raw string) []string {
	names, _ := quoteParseOrder(raw)
	if len(names) == 0 {
		return quoteShippedOrder()
	}
	return names
}

// Source order governs durable settlement, so its transition and dedicated audit are atomic.
// The initial insert acquires SQLite's write lock before reading; Postgres locks the existing
// settings row explicitly. All transaction work uses tx, never the single-connection pool.
func (s *Store) saveQuoteOrder(ctx context.Context, raw string, entry AuditEntry) error {
	names, unknown := quoteParseOrder(raw)
	if unknown != "" {
		return fmt.Errorf("unknown quote source %q", unknown)
	}
	raw = strings.Join(names, ",")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO NOTHING`), setQuoteSourceOrder, ""); err != nil {
		return err
	}
	q := `SELECT v FROM meta WHERE k=?`
	if s.driver == "postgres" {
		q += " FOR UPDATE"
	}
	var before string
	if err := tx.QueryRowContext(ctx, s.bind(q), setQuoteSourceOrder).Scan(&before); err != nil {
		return err
	}
	from, to := effectiveQuoteOrder(before), effectiveQuoteOrder(raw)
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE meta SET v=? WHERE k=?`), raw, setQuoteSourceOrder); err != nil {
		return err
	}
	if strings.Join(from, ",") != strings.Join(to, ",") {
		detail := auditJSON(map[string]any{"from": from, "to": to})
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,?,?,?,?)`),
			nowStr(), entry.Actor, entry.ActorOU, AuditQuoteOrder, "settings", setQuoteSourceOrder, detail, entry.IP); err != nil {
			return err
		}
	}
	return tx.Commit()
}
