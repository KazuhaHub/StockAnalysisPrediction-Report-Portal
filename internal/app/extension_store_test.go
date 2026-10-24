package app

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestExtensionConnectionLifecycle(t *testing.T) {
	testExtensionConnectionLifecycle(t, newTestStore(t))
}
func TestPostgresExtensionConnectionLifecycle(t *testing.T) {
	testExtensionConnectionLifecycle(t, pgStore(t, "app_backend_connections", "app_files", "apps"))
}
func testExtensionConnectionLifecycle(t *testing.T, st *Store) {
	t.Helper()
	c := extensionConnection{AppID: "status", InstanceID: "first", Label: "Status", BaseURL: "https://status.example", Audience: "status", Secret: "sealed", Policy: `{}`, Enabled: true}
	if err := st.saveExtensionConnection(c, 0); !errors.Is(err, errExtensionMissingApp) {
		t.Fatalf("missing installation: %v", err)
	}
	a := App{ID: "status", Name: "Status", Entry: "index.html", Version: "0.1.0"}
	if err := st.InstallApp(a, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.saveExtensionConnection(c, 0); err != nil {
		t.Fatal(err)
	}
	got, err := st.extensionConnection("status")
	if err != nil || got.Revision != 1 || got.CreatedAt == 0 {
		t.Fatalf("connection=%+v err=%v", got, err)
	}
	if err := st.saveExtensionConnection(c, 0); !errors.Is(err, errExtensionConflict) {
		t.Fatalf("stale create: %v", err)
	}
	c = got
	c.Label = "Changed"
	c.InstanceID = "cannot-replace"
	if err := st.saveExtensionConnection(c, 1); !errors.Is(err, errExtensionConflict) {
		t.Fatalf("instance overwrite: %v", err)
	}
	c.InstanceID = got.InstanceID
	if err := st.saveExtensionConnection(c, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.saveExtensionConnection(c, 1); !errors.Is(err, errExtensionConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if err := st.InstallApp(a, nil); err != nil {
		t.Fatal(err)
	}
	reinstalled, err := st.extensionConnection("status")
	if err != nil {
		t.Fatal(err)
	}
	if reinstalled.Enabled || reinstalled.Secret != "" || reinstalled.InstanceID == got.InstanceID || reinstalled.Revision != 3 {
		t.Fatalf("reinstall kept authority: %+v", reinstalled)
	}
	if err := st.DeleteApp("status"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.extensionConnection("status"); !errors.Is(err, errExtensionNotFound) {
		t.Fatalf("uninstall retained secret: %v", err)
	}
}

func TestExtensionConnectionRestoreRequiresPairing(t *testing.T) {
	st := newTestStore(t)
	if err := st.InstallApp(App{ID: "status", Name: "Status", Entry: "index.html"}, nil); err != nil {
		t.Fatal(err)
	}
	c := extensionConnection{AppID: "status", InstanceID: "original", Label: "Status", BaseURL: "https://status.example", Audience: "status", Secret: "sealed", Policy: `{}`, Enabled: true}
	if err := st.saveExtensionConnection(c, 0); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, st)
	if _, err := st.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	got, err := st.extensionConnection("status")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Secret != "" || got.InstanceID == c.InstanceID || got.UpdatedAt < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("unsafe restored registration: %+v", got)
	}
	if _, err := st.restoreFrom(bytes.NewReader(preMigrationDump(t)), true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.extensionConnection("status"); !errors.Is(err, errExtensionNotFound) {
		t.Fatalf("older dump retained connection: %v", err)
	}
}

func TestExtensionMigrationDeclaresAllColumns(t *testing.T) {
	st := newTestStore(t)
	if !recorded(st, "0008") {
		t.Fatal("connection migration not recorded")
	}
	if _, err := st.exec(`ALTER TABLE app_backend_connections DROP COLUMN label`); err != nil {
		t.Fatal(err)
	}
	if err := st.runMigrations(); err == nil {
		t.Fatal("partial connection schema accepted")
	}
}
