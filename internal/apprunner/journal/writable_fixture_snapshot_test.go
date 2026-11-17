//go:build linux || darwin

package journal

import (
	"encoding/json"
	"testing"
)

func writableFixtureJournalSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	snapshot := map[string][][]any{}
	for _, table := range []string{"runner_deployment", "runner_commands", "runner_resources"} {
		rows, err := s.db.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot[table] = append(snapshot[table], values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return candidateHash(encoded)
}
