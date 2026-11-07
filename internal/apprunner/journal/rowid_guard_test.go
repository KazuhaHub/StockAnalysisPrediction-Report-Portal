package journal

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Supersession of a migration is decided by the rowid of the command rows (see rowOrder). A rowid only grows while
// rows are only ever added: a delete of the newest row, an INSERT OR REPLACE and VACUUM can each hand a number out
// again or renumber rows. No runner code may do any of them to the commands table.
func TestNoRunnerCodeRenumbersTheCommandsTable(t *testing.T) {
	forbidden := regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+runner_commands\b|\bREPLACE\s+INTO\s+runner_commands\b|\bINSERT\s+OR\s+REPLACE\s+INTO\s+runner_commands\b|\bVACUUM\b`)
	root := filepath.Join("..")
	found := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Only statements count: a comment saying what must never happen is not one.
		code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(raw), "")
		if loc := forbidden.FindStringIndex(code); loc != nil {
			t.Errorf("%s renumbers or removes command rows: %q", path, code[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 20 {
		t.Fatalf("the scan saw %d runner files; it is not looking where the code is", found)
	}
}

// The guard sees the statements it exists for.
func TestRowidGuardPatternMatchesTheForbiddenStatements(t *testing.T) {
	forbidden := regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+runner_commands\b|\bREPLACE\s+INTO\s+runner_commands\b|\bINSERT\s+OR\s+REPLACE\s+INTO\s+runner_commands\b|\bVACUUM\b`)
	for _, statement := range []string{"DELETE FROM runner_commands WHERE 1", "delete  from\nrunner_commands", "REPLACE INTO runner_commands(a)", "INSERT OR REPLACE INTO runner_commands", "VACUUM", "vacuum into 'x'"} {
		if !forbidden.MatchString(statement) {
			t.Fatalf("the guard misses %q", statement)
		}
	}
	if forbidden.MatchString("DELETE FROM runner_resources") {
		t.Fatal("the guard flags another table")
	}
}
