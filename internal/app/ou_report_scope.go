package app

import (
	"context"
	"database/sql"
	"strings"
)

// viewerScopeSnapshot resolves restriction, audience and version visibility from one read
// transaction. Helpers receive that same transaction and never reacquire the SQLite pool.
func (s *Store) viewerScopeSnapshot(user string) (*ownerScope, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sub, snapshot, err := s.loadOUPolicySubjectFrom(tx, user)
	if err != nil {
		return nil, err
	}
	if can(sub.Role, PermManage) {
		return nil, tx.Commit()
	}
	policies, err := snapshot.resolve(sub.EffectiveOU)
	if err != nil {
		return nil, err
	}
	if !sub.Restricted && !policies.Restricted.Effective {
		return nil, tx.Commit()
	}
	chain, err := snapshot.graph.policyChain(sub.EffectiveOU)
	if err != nil {
		return nil, err
	}
	scope := &ownerScope{self: userPrincipal(user)}
	scope.principals = []string{scope.self, groupPrincipal(snapshot.graph.defaultID)}
	for _, id := range chain {
		if id == snapshot.graph.defaultID {
			continue
		}
		p, err := snapshot.resolve(id)
		if err != nil {
			return nil, err
		}
		if p.Restricted.Effective {
			scope.principals = append(scope.principals, groupPrincipal(id))
		}
	}
	versions, err := s.grantedVersionsFrom(tx, sub, snapshot)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return scope, tx.Commit()
	}
	args := make([]any, len(versions))
	for i, name := range versions {
		args[i] = name
	}
	rows, err := tx.Query(s.bind(`SELECT name,COALESCE(visibility,'owner') FROM report_versions WHERE name IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`), args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		var visibility Visibility
		if err := rows.Scan(&name, &visibility); err != nil {
			rows.Close()
			return nil, err
		}
		switch visibility {
		case VisibilityAll:
			scope.versAll = append(scope.versAll, name)
		case VisibilityGroup:
			scope.versGroup = append(scope.versGroup, name)
		default:
			scope.versOwner = append(scope.versOwner, name)
		}
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	return scope, tx.Commit()
}
