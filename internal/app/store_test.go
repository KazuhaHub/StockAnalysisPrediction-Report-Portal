package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestStore opens a fresh in-memory sqlite Store. In-memory (rather than a temp-dir
// file) means there's no directory for t.TempDir to RemoveAll while the just-closed
// sqlite connection is still releasing its files — a race that flaked on Linux CI with
// "directory not empty". OpenStore sets MaxOpenConns=1, so every query shares the one
// connection's in-memory database, and separate stores stay isolated.
//
// Initializing a store runs the whole baseline schema and every migration: about 8ms normally but
// about 320ms under -race, where the transpiled SQLite engine pays for every memory access, and
// hundreds of tests open one or more. So the first call initializes a store through OpenStore and
// keeps its serialized image, and every call restores that image into a new in-memory database.
// Initialization only writes the database (no Store field or package state), so the restored store
// is the one OpenStore would have returned. Tests of initialization itself (baselines, migrations,
// startup refusals) call OpenStore directly and still run the full path.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	img, err := testStoreImage()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	st, err := connectStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := withSQLiteConn(st.db, func(c sqliteImageConn) error { return c.Deserialize(img) }); err != nil {
		st.Close()
		t.Fatalf("restore the initialized test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

var testStoreTemplate struct {
	once sync.Once
	img  []byte
	err  error
}

func testStoreImage() ([]byte, error) {
	testStoreTemplate.once.Do(func() {
		st, err := OpenStore("sqlite", ":memory:")
		if err != nil {
			testStoreTemplate.err = err
			return
		}
		defer st.Close()
		testStoreTemplate.err = withSQLiteConn(st.db, func(c sqliteImageConn) error {
			img, err := c.Serialize()
			testStoreTemplate.img = img
			return err
		})
	})
	return testStoreTemplate.img, testStoreTemplate.err
}

// sqliteImageConn is the part of the modernc driver connection that copies a whole database.
type sqliteImageConn interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

// withSQLiteConn runs f on the pool's single connection, the one every later query of an in-memory
// store uses (connectStore caps SQLite at one connection).
func withSQLiteConn(db *sql.DB, f func(sqliteImageConn) error) error {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		c, ok := dc.(sqliteImageConn)
		if !ok {
			return fmt.Errorf("sqlite driver connection %T cannot copy a database", dc)
		}
		return f(c)
	})
}

// Links carry an optional icon name (chosen in the admin UI) that must survive
// add + edit round-trips.
func TestLinkIconRoundTrip(t *testing.T) {
	st := newTestStore(t)

	if err := st.AddLink("GitHub", "https://github.com", "github", true, 0, 0); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	ls := st.Links()
	if len(ls) != 1 {
		t.Fatalf("Links len = %d, want 1", len(ls))
	}
	if ls[0].Icon != "github" || !ls[0].NewTab || ls[0].GroupID != 0 || !ls[0].Visible {
		t.Errorf("got Icon=%q NewTab=%v GroupID=%v Visible=%v, want github/true/0/true", ls[0].Icon, ls[0].NewTab, ls[0].GroupID, ls[0].Visible)
	}

	// Editing label/URL/icon/newTab/visible preserves the row and updates the fields (visible off).
	if err := st.UpdateLinkFields(ls[0].ID, "GH", "https://gh.io", "book", false, false); err != nil {
		t.Fatalf("UpdateLinkFields: %v", err)
	}
	ls = st.Links()
	if len(ls) != 1 {
		t.Fatalf("Links len = %d, want 1", len(ls))
	}
	if ls[0].Icon != "book" || ls[0].Label != "GH" || ls[0].URL != "https://gh.io" || ls[0].NewTab || ls[0].Visible {
		t.Errorf("after edit = %+v, want {Label:GH URL:https://gh.io Icon:book NewTab:false Visible:false}", ls[0])
	}
}

// Entry-button groups: create/list/update, assign links to a group + order, and deleting a
// group returns its links to the top level (ungrouped).
func TestLinkGroups(t *testing.T) {
	st := newTestStore(t)
	gid, err := st.AddLinkGroup("决策分析", "popover", true, "bulb", 0)
	if err != nil {
		t.Fatalf("AddLinkGroup: %v", err)
	}
	gs := st.LinkGroups()
	if len(gs) != 1 || gs[0].Mode != "popover" || !gs[0].ShowLabel || gs[0].Icon != "bulb" || !gs[0].Visible {
		t.Fatalf("groups = %+v, want one popover/show-label/bulb/visible group", gs)
	}
	if err := st.UpdateLinkGroup(gid, "决策", "modal", false, "rocket", false); err != nil {
		t.Fatalf("UpdateLinkGroup: %v", err)
	}
	if gs = st.LinkGroups(); gs[0].Name != "决策" || gs[0].Mode != "modal" || gs[0].ShowLabel || gs[0].Icon != "rocket" || gs[0].Visible {
		t.Fatalf("after update = %+v, want 决策/modal/no-label/rocket/hidden", gs[0])
	}

	// Two links; assign one into the group, keep one top-level.
	st.AddLink("A", "https://a", "", true, 0, 0)
	st.AddLink("B", "https://b", "", true, 0, 1)
	ls := st.Links()
	st.SetLinkGroupAndOrder(ls[1].ID, gid, 0)
	byID := map[int64]Link{}
	for _, l := range st.Links() {
		byID[l.ID] = l
	}
	if byID[ls[0].ID].GroupID != 0 || byID[ls[1].ID].GroupID != gid {
		t.Fatalf("group assignment wrong: %+v", st.Links())
	}

	// Deleting the group orphans its links back to the top level.
	if err := st.DeleteLinkGroup(gid); err != nil {
		t.Fatalf("DeleteLinkGroup: %v", err)
	}
	if len(st.LinkGroups()) != 0 {
		t.Fatal("group still present after delete")
	}
	for _, l := range st.Links() {
		if l.GroupID != 0 {
			t.Fatalf("link %d still in a deleted group (group_id=%d)", l.ID, l.GroupID)
		}
	}
}

// A link added without an icon reads back with an empty icon (not an error), so
// the frontend can fall back to the default link glyph.
func TestLinkEmptyIcon(t *testing.T) {
	st := newTestStore(t)
	if err := st.AddLink("Docs", "https://docs", "", true, 0, 0); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	ls := st.Links()
	if len(ls) != 1 || ls[0].Icon != "" {
		t.Fatalf("links = %+v, want one link with empty icon", ls)
	}
}

// Two stores on one SQLite file are two processes in production: the server and the backup CLI,
// or a server and an operator's maintenance command. A writer that finds the other holding the
// write lock must wait for it rather than fail at once with SQLITE_BUSY ("database is locked"),
// which is what TestForecastShadowDiagnosticsAcrossSQLiteConnections hit on a loaded CI runner.
func TestSQLiteWriterWaitsForAnotherConnectionsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	holder, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	waiter, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Close()

	tx, err := holder.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO meta(k, v) VALUES ('lock-holder', '1')`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		tx.Commit()
		close(released)
	}()

	if err := waiter.SetSetting("lock-waiter", "1"); err != nil {
		t.Fatalf("a writer behind another connection's lock failed instead of waiting: %v", err)
	}
	<-released
	if got := waiter.GetSetting("lock-holder", ""); got != "1" {
		t.Fatalf("holder's write not visible after commit: %q", got)
	}
}

// Every connection carries the timeout, the in-memory test store included, and an operator's own
// busy_timeout in the source is kept.
func TestSQLiteSourceCarriesTheBusyTimeout(t *testing.T) {
	for _, src := range []string{":memory:", filepath.Join(t.TempDir(), "a.db")} {
		st, err := connectStore("sqlite", src)
		if err != nil {
			t.Fatal(err)
		}
		var ms int
		if err := st.db.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil {
			t.Fatal(err)
		}
		st.Close()
		if ms != sqliteBusyTimeoutMs {
			t.Errorf("%s: busy_timeout = %d, want %d", src, ms, sqliteBusyTimeoutMs)
		}
	}
	if got := sqliteSource("data/p.db?_pragma=busy_timeout(100)"); got != "data/p.db?_pragma=busy_timeout(100)" {
		t.Errorf("operator timeout rewritten: %q", got)
	}
	if got := sqliteSource("data/p.db?mode=rw"); got != "data/p.db?mode=rw&_pragma=busy_timeout(5000)" {
		t.Errorf("existing query not extended: %q", got)
	}
}
