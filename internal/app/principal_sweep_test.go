package app

import (
	"fmt"
	"strings"
	"testing"
)

// A principal table is one keyed by "who" — `g:<id>` for an OU, `u:<name>` for an account. There
// are three of them now (report_viewers and version_grants from ADR 0024, announcement_grants from
// ADR 0025) and the same rule holds for all of them: when the thing a principal NAMES is deleted,
// its rows must go too.
//
// The rule is not tidiness, it is disclosure, and it is disclosure in both directions. Usernames
// are reusable — delete an account and the same name can register again — and OU ids are assigned
// by the database, so a deleted one comes back around. A row left behind is silently inherited by
// whoever holds that name or that id next: reports they were never granted, versions they cannot
// see, announcements addressed to somebody who left.
//
// So this test does not check the three tables that exist today. It reads the schema, finds every
// table with a `principal` column, seeds a row in each, and deletes the account and the OU. A
// fourth principal table added later fails here on the day it is added, rather than on the day
// somebody notices the wrong person is reading something.

// Principal discovery includes migration products as well as the frozen baseline. Inspect the
// initialized catalog so a newly added grant table cannot escape lifecycle coverage.
func principalColumnDefinitions(t *testing.T, s *Store, table string) []schemaCol {
	t.Helper()
	rows, err := s.query("SELECT name,type FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []schemaCol
	for rows.Next() {
		var c schemaCol
		if err := rows.Scan(&c.name, &c.def); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func principalTables(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	tables, err := s.backupTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		for _, c := range principalColumnDefinitions(t, s, table) {
			if c.name == "principal" {
				out = append(out, table)
				break
			}
		}
	}
	return out
}

func seedPrincipalRow(t *testing.T, s *Store, table, principal string) {
	t.Helper()
	var names []string
	var args []any
	for _, c := range principalColumnDefinitions(t, s, table) {
		names = append(names, c.name)
		switch {
		case c.name == "principal":
			args = append(args, principal)
		case strings.Contains(strings.ToUpper(c.def), "INT"):
			args = append(args, 1)
		default:
			args = append(args, "x")
		}
	}
	q := fmt.Sprintf("INSERT INTO %s(%s) VALUES(%s)", table, strings.Join(names, ","), strings.TrimSuffix(strings.Repeat("?,", len(args)), ","))
	if _, err := s.exec(q, args...); err != nil {
		t.Fatalf("seed %s: %v [%s]", table, err, q)
	}
}

func principalRows(t *testing.T, s *Store, table, principal string) int {
	t.Helper()
	var n int
	s.queryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE principal=?", table), principal).Scan(&n)
	return n
}

func TestEveryPrincipalTableIsSweptOnDelete(t *testing.T) {
	st := newTestStore(t)
	tables := principalTables(t, st)
	if len(tables) < 4 {
		t.Fatalf("found %v principal tables; expected at least report_viewers, version_grants and "+
			"announcement_grants — has the schema or parseCreateTable changed?", tables)
	}

	t.Run("deleting an account", func(t *testing.T) {
		if err := st.UpsertUser(User{Username: "leaver", PasswordHash: "h", Role: "user", Active: true}); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		principal := userPrincipal("leaver")
		for _, table := range tables {
			seedPrincipalRow(t, st, table, principal)
		}
		if err := st.DeleteUser("leaver"); err != nil {
			t.Fatalf("DeleteUser: %v", err)
		}
		for _, table := range tables {
			if n := principalRows(t, st, table, principal); n != 0 {
				t.Errorf("%d row(s) in %s outlived the account they name — the next holder of "+
					"that username inherits them. Add the sweep to Store.DeleteUser.", n, table)
			}
		}
	})

	t.Run("deleting an OU", func(t *testing.T) {
		id, err := st.CreateUserGroup("被删的组", "", 0)
		if err != nil {
			t.Fatalf("seed OU: %v", err)
		}
		principal := groupPrincipal(id)
		for _, table := range tables {
			seedPrincipalRow(t, st, table, principal)
		}
		if _, _, err := st.DeleteUserGroup(id); err != nil {
			t.Fatalf("DeleteUserGroup: %v", err)
		}
		for _, table := range tables {
			if n := principalRows(t, st, table, principal); n != 0 {
				t.Errorf("%d row(s) in %s outlived the OU they name — ids are reused, so the next "+
					"OU inherits them. Add the sweep to Store.DeleteUserGroup.", n, table)
			}
		}
	})
}
