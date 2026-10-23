package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const setForecastFallback = "forecast_fallback"

// Unknown persisted values fail closed. Tier 1 has no alternate writer, including a stored "on"
// value left by a different build. The settings writer validates rather than silently normalizing.
func forecastFallbackMode(value string) string {
	if strings.TrimSpace(value) == "shadow" {
		return "shadow"
	}
	return "off"
}

// A setting that enables extra vendor requests and its transition audit commit together.
func (s *Store) saveForecastFallback(ctx context.Context, next string, entry AuditEntry) error {
	if next != "off" && next != "shadow" {
		return fmt.Errorf("fallback must be off or shadow")
	}
	for attempt := 0; ; attempt++ {
		err := s.saveForecastFallbackOnce(ctx, next, entry)
		if err == nil || !isContentionErr(err) || attempt == 5 {
			return err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) saveForecastFallbackOnce(ctx context.Context, next string, entry AuditEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO NOTHING`), setForecastFallback, "off"); err != nil {
		return err
	}
	query := `SELECT COALESCE(v,'off') FROM meta WHERE k=?`
	if s.driver == "postgres" {
		query += " FOR UPDATE"
	}
	var before string
	if err := tx.QueryRowContext(ctx, s.bind(query), setForecastFallback).Scan(&before); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE meta SET v=? WHERE k=?`), next, setForecastFallback); err != nil {
		return err
	}
	from := forecastFallbackMode(before)
	if from != next {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,?,?,?,?)`), nowStr(), entry.Actor, entry.ActorOU, AuditForecastFallback, "settings", setForecastFallback, auditJSON(map[string]any{"from": from, "to": next}), entry.IP); err != nil {
			return err
		}
	}
	return tx.Commit()
}
