package app

import (
	"context"
	"database/sql"
	"strings"
)

func (s *Store) grantedVersionsFrom(ex execer, sub ouSubject, snapshot ouPolicySnapshot) ([]string, error) {
	chain, err := snapshot.graph.policyChain(sub.EffectiveOU)
	if err != nil {
		return nil, err
	}
	args := []any{userPrincipal(sub.Username)}
	for _, id := range chain {
		args = append(args, groupPrincipal(id))
	}
	rows, err := ex.Query(s.bind(`SELECT g.principal,g.version FROM version_grants g
		LEFT JOIN report_versions v ON v.name=g.version WHERE g.principal IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`) ORDER BY COALESCE(v.ord,0),g.version`), args...)
	if err != nil {
		return nil, err
	}
	values := map[string][]string{}
	for rows.Next() {
		var principal, version string
		if err := rows.Scan(&principal, &version); err != nil {
			rows.Close()
			return nil, err
		}
		values[principal] = append(values[principal], version)
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	var out []string
	if own := values[userPrincipal(sub.Username)]; len(own) > 0 {
		out = own
	} else {
		resolved := nearestOUPolicy(chain, []string(nil), func(id int64) *[]string {
			v := values[groupPrincipal(id)]
			if len(v) == 0 {
				return nil
			}
			return &v
		})
		out = resolved.Effective
	}
	return out, nil
}

// GrantedVersionsErr reads subject, ancestry and grants on one snapshot. Empty is a successful
// default-deny answer; a failed nearer read cannot be mistaken for a request to inherit.
func (s *Store) GrantedVersionsErr(username string) ([]string, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sub, snapshot, err := s.loadOUPolicySubjectFrom(tx, username)
	if err != nil {
		return nil, err
	}
	out, err := s.grantedVersionsFrom(tx, sub, snapshot)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
