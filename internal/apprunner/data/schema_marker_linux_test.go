//go:build linux && (amd64 || arm64)

package data

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDirectory(t *testing.T, dir string) *os.File {
	t.Helper()
	folder, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	return folder
}

func TestSchemaMarkerSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "elsewhere"), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(dir, SchemaMarkerName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSchemaMarkerIn(openTestDirectory(t, dir)); !errors.Is(err, ErrMigrationUnsafe) {
		t.Fatalf("symlink marker read: %v", err)
	}
}

func TestSchemaMarkerReadStates(t *testing.T) {
	dir := t.TempDir()
	folder := openTestDirectory(t, dir)
	if schema, present, err := readSchemaMarkerIn(folder); err != nil || present || schema != 0 {
		t.Fatalf("absent marker: %d %v %v", schema, present, err)
	}
	path := filepath.Join(dir, SchemaMarkerName)
	if err := os.WriteFile(path, []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if schema, present, err := readSchemaMarkerIn(folder); err != nil || !present || schema != 3 {
		t.Fatalf("marker: %d %v %v", schema, present, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("1", 33)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSchemaMarkerIn(folder); !errors.Is(err, ErrMigrationUnsafe) {
		t.Fatal("oversized marker accepted", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSchemaMarkerIn(folder); !errors.Is(err, ErrMigrationUnsafe) {
		t.Fatal("directory marker accepted", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other"), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "other"), path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSchemaMarkerIn(folder); !errors.Is(err, ErrMigrationUnsafe) {
		t.Fatal("hard-linked marker accepted", err)
	}
}
