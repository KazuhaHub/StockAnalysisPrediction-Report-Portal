//go:build linux || darwin

package journal

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commandsSQL(version int) string {
	for _, product := range schemaProducts(version) {
		if product.name == "runner_commands" {
			return product.sql
		}
	}
	return ""
}

// The remove-image kind belongs to the v3 declaration of runner_commands only. Version 1 and version 2 stay the
// frozen products older files are recognised against.
func TestRemoveImageKindIsDeclaredByV3Only(t *testing.T) {
	if !strings.Contains(commandsSQL(3), "'load-image','remove-image'") {
		t.Fatalf("v3 runner_commands does not declare remove-image beside load-image: %s", commandsSQL(3))
	}
	for _, version := range []int{1, 2} {
		if strings.Contains(commandsSQL(version), "remove-image") {
			t.Fatalf("v%d runner_commands declares remove-image", version)
		}
	}
	if !strings.Contains(commandsSQL(2), "'load-image')") {
		t.Fatal("the frozen v2 kind list changed")
	}
	// Only the kind list differs between each earlier and the current v3 declaration.
	earlier := earlierV3CommandsSQL()
	if len(earlier) != 2 || earlier[0] == commandsSQL(3) || earlier[1] == commandsSQL(3) || earlier[0] == earlier[1] {
		t.Fatal("the earlier v3 declarations are not two distinct shapes older than v3")
	}
	if strings.Replace(commandsSQL(3), ",'remove-image','delete-checkpoint'", "", 1) != earlier[0] {
		t.Fatal("the first earlier v3 declaration differs from v3 by more than the remove-image and delete-checkpoint kinds")
	}
	if strings.Replace(commandsSQL(3), ",'delete-checkpoint'", "", 1) != earlier[1] {
		t.Fatal("the second earlier v3 declaration differs from v3 by more than the delete-checkpoint kind")
	}
}

// The delete-checkpoint kind was folded into the v3 declaration after remove-image (owner decision 2026-11-16).
func TestDeleteCheckpointKindIsDeclaredByV3Only(t *testing.T) {
	if !strings.Contains(commandsSQL(3), "'load-image','remove-image','delete-checkpoint'") {
		t.Fatalf("v3 runner_commands does not declare delete-checkpoint after remove-image: %s", commandsSQL(3))
	}
	for _, version := range []int{1, 2} {
		if strings.Contains(commandsSQL(version), "delete-checkpoint") {
			t.Fatalf("v%d runner_commands declares delete-checkpoint", version)
		}
	}
}

func TestFreshJournalAcceptsDeleteCheckpointKind(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := insertDeployment(store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(importKindCommand, fixtureID(210), fixtureID(310), fixtureID(5), "delete-checkpoint", strings.Repeat("c", 64), fixtureID(2)); err != nil {
		t.Fatalf("delete-checkpoint refused by a fresh journal: %v", err)
	}
	if _, err := store.db.Exec(importKindCommand, fixtureID(211), fixtureID(311), fixtureID(5), "delete-checkpoints", strings.Repeat("c", 64), fixtureID(2)); err == nil {
		t.Fatal("a fresh journal admitted an arbitrary kind")
	}
	if _, err := inspect(context.Background(), store.db); err != nil {
		t.Fatalf("delete-checkpoint row fails inspection: %v", err)
	}
}

func TestFreshJournalAcceptsRemoveImageKind(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := insertDeployment(store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(importKindCommand, fixtureID(200), fixtureID(300), fixtureID(5), "remove-image", strings.Repeat("c", 64), fixtureID(2)); err != nil {
		t.Fatalf("remove-image refused by a fresh journal: %v", err)
	}
	if _, err := store.db.Exec(importKindCommand, fixtureID(201), fixtureID(301), fixtureID(5), "remove-images", strings.Repeat("c", 64), fixtureID(2)); err == nil {
		t.Fatal("a fresh journal admitted an arbitrary kind")
	}
	if _, err := inspect(context.Background(), store.db); err != nil {
		t.Fatalf("remove-image row fails inspection: %v", err)
	}
}

func TestV1AndV2UpgradesProduceTheCurrentV3Shape(t *testing.T) {
	for name, build := range map[string]func(*testing.T) string{
		"v1": v1Journal,
		"v2": func(t *testing.T) string {
			directory, db := newV2Journal(t)
			if err := insertDeployment(db); err != nil {
				t.Fatal(err)
			}
			insertV2Load(t, db, 40, 71, "applied", `{"claim":{"config_digest":"`+strings.Repeat("1", 64)+`","layers":"`+strings.Repeat("4", 64)+`:1000"}}`)
			db.Close()
			return directory
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := build(t)
			raw, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(importKindCommand, fixtureID(250), fixtureID(350), fixtureID(5), "remove-image", strings.Repeat("c", 64), fixtureID(2)); err == nil {
				t.Fatalf("%s journal admitted remove-image before the upgrade", name)
			}
			if _, err := raw.Exec(importKindCommand, fixtureID(253), fixtureID(353), fixtureID(5), "delete-checkpoint", strings.Repeat("c", 64), fixtureID(2)); err == nil {
				t.Fatalf("%s journal admitted delete-checkpoint before the upgrade", name)
			}
			raw.Close()
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var got string
			if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='runner_commands'`).Scan(&got); err != nil || strings.TrimSpace(got) != commandsSQL(3) {
				t.Fatalf("upgraded runner_commands is not the v3 product: %v\n%s", err, got)
			}
			if _, err := store.db.Exec(importKindCommand, fixtureID(251), fixtureID(351), fixtureID(5), "remove-image", strings.Repeat("c", 64), fixtureID(2)); err != nil {
				t.Fatalf("remove-image refused after the upgrade: %v", err)
			}
			if _, err := store.db.Exec(importKindCommand, fixtureID(252), fixtureID(352), fixtureID(5), "delete-checkpoint", strings.Repeat("c", 64), fixtureID(2)); err != nil {
				t.Fatalf("delete-checkpoint refused after the upgrade: %v", err)
			}
			if _, err := inspect(context.Background(), store.db); err != nil {
				t.Fatalf("upgraded journal fails inspection: %v", err)
			}
		})
	}
}

// A journal at either earlier v3 shape (before remove-image, and before delete-checkpoint) was never shipped to a
// persistent runner, so it is not converted: it is refused with a typed incompatibility that names the remedy, and
// the file is not touched.
func TestEarlierV3ShapesAreRefusedWithATypedError(t *testing.T) {
	for index, shape := range earlierV3Shapes() {
		t.Run([]string{"before-remove-image", "before-delete-checkpoint"}[index], func(t *testing.T) {
			refuseEarlierV3Shape(t, shape)
		})
	}
}

func refuseEarlierV3Shape(t *testing.T, shape []schemaProduct) {
	directory := privateDirectory(t)
	path := filepath.Join(directory, journalName)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL fixture profile: %s %v", mode, err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, product := range shape {
		if _, err := tx.Exec(product.sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := insertDeployment(db); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrEarlierV3Journal) || !errors.Is(err, ErrIncompatible) {
		t.Fatalf("earlier v3 journal opened or was refused untyped: %v", err)
	}
	if !strings.Contains(err.Error(), "fresh journal") {
		t.Fatalf("the refusal does not tell the operator to start from a fresh journal: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the refusal rewrote the earlier v3 journal")
	}
	// A v3 journal that merely drifted is still the plain incompatibility, not the earlier-shape error.
	if errors.Is(ErrIncompatible, ErrEarlierV3Journal) {
		t.Fatal("ErrIncompatible must not match the earlier-shape error")
	}
}
