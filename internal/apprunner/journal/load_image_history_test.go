//go:build linux || darwin

package journal

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// loadImageRowState, when set, makes the shared fixtures add one load-image row ("applied" or "unknown") for the
// installation of their first command, so every command family's admission runs with a load beside it.
var loadImageRowState string

// imageRowKind is the kind of the row injectLoadImageRow adds: a load by default, or a removal.
var imageRowKind = "load-image"

func injectLoadImageRow(t *testing.T, s *Store) {
	t.Helper()
	if loadImageRowState == "" {
		return
	}
	receipt := "{}"
	if _, err := s.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		SELECT ?,operation_id,installation_id,'stage',?,?,payload_sha256,target_generation,controller_epoch,admitted_fence,?,?,NULL,created_ms,updated_ms FROM runner_commands WHERE NOT EXISTS(SELECT 1 FROM runner_commands WHERE kind=?) ORDER BY rowid LIMIT 1`,
		"70000000-0000-4000-8000-0000000000aa", map[string]int{"load-image": 1, "remove-image": 2}[imageRowKind], imageRowKind, loadImageRowState, receipt, imageRowKind); err != nil {
		t.Fatal(err)
	}
}

// Fails when a command family refuses to start because an applied or unknown load-image row sits in the
// installation's history: a load touches only daemon image storage, never data, fences or containers.
func TestEveryCommandFamilyAdmitsAfterAnAppliedOrUnknownLoadImageRow(t *testing.T) {
	for _, state := range []string{"applied", "unknown"} {
		for name, run := range map[string]func(*testing.T){
			"writer chain through candidate claim": func(t *testing.T) { writableClaimFixture(t) },
			"data reservation":                     func(t *testing.T) { reservedClaimFixture(t) },
			"writable registration":                func(t *testing.T) { writableRegistrationClaimFixture(t) },
			"checkpoint":                           func(t *testing.T) { appliedCheckpointFixture(t) },
			"update start":                         func(t *testing.T) { updateStartJournalFixture(t) },
			"managed removal":                      func(t *testing.T) { newManagedRemovalFixture(t) },
			"legacy candidate":                     func(t *testing.T) { readyCandidateFixture(t) },
		} {
			t.Run(state+"/"+name, func(t *testing.T) {
				loadImageRowState = state
				defer func() { loadImageRowState = "" }()
				run(t)
			})
		}
	}
}

// The same, for a removal: an applied or unknown remove-image row touches only daemon image storage, so it never
// blocks a start-bearing command family of its installation. Fails if a reader stops ignoring it.
func TestEveryCommandFamilyAdmitsAfterAnAppliedOrUnknownRemoveImageRow(t *testing.T) {
	for _, state := range []string{"applied", "unknown"} {
		for name, run := range map[string]func(*testing.T){
			"writer chain through candidate claim": func(t *testing.T) { writableClaimFixture(t) },
			"data reservation":                     func(t *testing.T) { reservedClaimFixture(t) },
			"writable registration":                func(t *testing.T) { writableRegistrationClaimFixture(t) },
			"checkpoint":                           func(t *testing.T) { appliedCheckpointFixture(t) },
			"update start":                         func(t *testing.T) { updateStartJournalFixture(t) },
			"managed removal":                      func(t *testing.T) { newManagedRemovalFixture(t) },
			"legacy candidate":                     func(t *testing.T) { readyCandidateFixture(t) },
		} {
			t.Run(state+"/"+name, func(t *testing.T) {
				loadImageRowState, imageRowKind = state, "remove-image"
				defer func() { loadImageRowState, imageRowKind = "", "load-image" }()
				run(t)
			})
		}
	}
}

// Fails if a journal reader that counts or inspects other command rows of an installation stops ignoring
// load-image rows. The test reads the source: every statement that counts, pending-checks or takes a floor over
// the installation's rows must carry the exclusion, so a new reader cannot silently reintroduce the blocker.
func TestInstallationWideCommandReadersIgnoreLoadImageRows(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	sql := regexp.MustCompile("`[^`]*runner_commands[^`]*`")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "load_image.go" || f == "remove_image.go" || f == "enrollment.go" || f == "store.go" {
			continue
		}
		raw, _ := os.ReadFile(f)
		for _, q := range sql.FindAllString(string(raw), -1) {
			if strings.Contains(q, "INSERT INTO") || strings.Contains(q, "UPDATE runner_commands") || !strings.Contains(q, "installation_id=?") {
				continue
			}
			wide := strings.Contains(q, "COUNT(*)") || strings.Contains(q, "MAX(")
			if !wide || (strings.Contains(q, "load-image") && strings.Contains(q, "remove-image")) {
				continue
			}
			// Statements already narrowed to one kind or one operation stage are not installation-wide.
			if regexp.MustCompile(`kind\s*(=|IN)`).MatchString(q) || strings.Contains(q, "operation_id=?") || strings.Contains(q, "effect_id=?") {
				continue
			}
			t.Errorf("%s: an installation-wide runner_commands reader does not ignore load-image rows:\n%s", f, q)
		}
	}
}
