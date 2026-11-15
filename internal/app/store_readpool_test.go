package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// readDBOf is the read pool's database, nil where there is none.
func readDBOf(st *Store) *sql.DB {
	if rp := st.readPoolOrNil(); rp != nil {
		return rp.db
	}
	return nil
}

// openFileStore opens a file-backed SQLite store through OpenStore, the path the server takes, so it
// carries the read pool.
func openFileStore(tb testing.TB) *Store {
	tb.Helper()
	st, err := OpenStore("sqlite", filepath.Join(tb.TempDir(), "portal.db"))
	if err != nil {
		tb.Fatalf("OpenStore: %v", err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	return st
}

// holdWriter begins a write transaction on the writer connection, writes to it so the write lock is
// taken, and returns a func that ends it. While it is held the single writer connection is in use.
func holdWriter(tb testing.TB, st *Store) (release func()) {
	tb.Helper()
	tx, err := st.db.Begin()
	if err != nil {
		tb.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO meta(k,v) VALUES('readpool:held','1') ON CONFLICT(k) DO UPDATE SET v=excluded.v`); err != nil {
		tb.Fatalf("hold write: %v", err)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = tx.Rollback() }) }
}

func TestReadPoolIsOpenForAFileBackedSQLiteStore(t *testing.T) {
	st := openFileStore(t)
	if readDBOf(st) == nil {
		t.Fatal("a file-backed SQLite store must carry a read pool")
	}
	if readDBOf(st) == st.db {
		t.Fatal("the read pool must be a different pool from the writer")
	}
	if got := readDBOf(st).Stats().MaxOpenConnections; got != sqliteReadPoolSize {
		t.Fatalf("read pool size = %d, want %d", got, sqliteReadPoolSize)
	}
	if got := st.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("the writer pool must stay at one connection, got %d", got)
	}
	var mode string
	if err := st.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q (%v), want wal", mode, err)
	}
}

func TestReadPoolIsAbsentWhereItCannotWork(t *testing.T) {
	mem := newTestStore(t) // in-memory: every connection would be a different database
	if readDBOf(mem) != nil {
		t.Fatal("an in-memory store must not carry a read pool")
	}
	if mem.readDB() != mem.db {
		t.Fatal("without a read pool, reads fall back to the writer pool")
	}
	var one int
	if err := mem.readQueryRow("SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("fallback read: %d, %v", one, err)
	}
}

func TestReadsProceedWhileAWriteTransactionIsHeld(t *testing.T) {
	st := openFileStore(t)
	if err := st.SetSetting("readpool:probe", "before"); err != nil {
		t.Fatal(err)
	}
	release := holdWriter(t, st)
	defer release()

	// Control: the writer pool is occupied, so a query on it cannot proceed.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	var n int
	err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM meta").Scan(&n)
	cancel()
	if err == nil {
		t.Fatal("control failed: the writer pool answered while its only connection was held")
	}

	// The read pool answers, with the committed state, through the store's own accessors.
	done := make(chan string, 1)
	go func() { done <- st.GetSetting("readpool:probe", "missing") }()
	select {
	case got := <-done:
		if got != "before" {
			t.Fatalf("read during a held write = %q, want the committed %q", got, "before")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a read blocked behind the held write transaction")
	}
	// The held transaction's uncommitted row is not visible to a reader.
	if got := st.GetSetting("readpool:held", "absent"); got != "absent" {
		t.Fatalf("a reader saw an uncommitted write: %q", got)
	}
	// Many concurrent readers all proceed.
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var c int
			errs <- st.readQueryRow("SELECT COUNT(*) FROM meta").Scan(&c)
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent reads blocked behind the held write transaction")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
}

func TestAWriteIsVisibleToTheNextReadPoolQuery(t *testing.T) {
	st := openFileStore(t)
	for i := 0; i < 300; i++ {
		want := strings.Repeat("x", i%7) + string(rune('a'+i%26))
		if err := st.SetSetting("readpool:rw", want); err != nil {
			t.Fatal(err)
		}
		if got := st.GetSetting("readpool:rw", ""); got != want {
			t.Fatalf("iteration %d: read %q right after writing %q", i, got, want)
		}
	}
	// A transaction commit is visible as soon as Commit returns, to a reader on another connection.
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO meta(k,v) VALUES('readpool:tx','committed')`); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSetting("readpool:tx", "none"); got != "none" {
		t.Fatalf("an open transaction's write leaked to a reader: %q", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSetting("readpool:tx", "none"); got != "committed" {
		t.Fatalf("a committed transaction's write was not visible: %q", got)
	}
}

func TestReadPoolRejectsWrites(t *testing.T) {
	st := openFileStore(t)
	for name, run := range map[string]func() error{
		"raw pool": func() error {
			_, err := readDBOf(st).Exec(`INSERT INTO meta(k,v) VALUES('readpool:w','1')`)
			return err
		},
		"cached exec": func() error {
			_, err := st.readExec().exec(`INSERT INTO meta(k,v) VALUES('readpool:w','1')`)
			return err
		},
		"read snapshot": func() error {
			snap, end := st.beginReadSnapshot(context.Background())
			defer end()
			_, err := snap.exec(`INSERT INTO meta(k,v) VALUES('readpool:w','1')`)
			return err
		},
		"pragma off": func() error {
			if _, err := readDBOf(st).Exec(`PRAGMA query_only=0`); err != nil {
				return err
			}
			_, err := readDBOf(st).Exec(`INSERT INTO meta(k,v) VALUES('readpool:w','1')`)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Errorf("%s: a write on the read pool succeeded", name)
		}
	}
	if got := st.GetSetting("readpool:w", "absent"); got != "absent" {
		t.Fatalf("a rejected write left a row behind: %q", got)
	}
}

func TestReadSnapshotRunsOnTheReadPoolWhileTheWriterIsHeld(t *testing.T) {
	st := openFileStore(t)
	if _, _, err := st.UpsertReport(Rep{Symbol: "600000", Date: "2026-11-01", RType: "t", Title: "T", MD: "b"}); err != nil {
		t.Fatal(err)
	}
	release := holdWriter(t, st)
	defer release()
	done := make(chan int, 1)
	go func() {
		snap, end := st.beginReadSnapshot(context.Background())
		defer end()
		// Twice, so the second call goes through the statement cache of the read pool's transaction.
		a, _ := st.newBySymbolFrom(snap, "600000", nil)
		b, _ := st.newBySymbolFrom(snap, "600000", nil)
		done <- len(a)*10 + len(b)
	}()
	select {
	case got := <-done:
		if got != 11 {
			t.Fatalf("snapshot reads returned %d, want one report each time", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the read snapshot blocked behind the held writer")
	}
}

func TestReadPoolSourceDerivation(t *testing.T) {
	for _, tc := range []struct {
		in   string
		ok   bool
		want string // substring of the derived source
	}{
		{":memory:", false, ""},
		{"", false, ""},
		{"file::memory:?cache=shared", false, ""},
		{"file:x.db?mode=memory&cache=shared", false, ""},
		{"data/portal.db", true, "file:data/portal.db?"},
		{"data/we ird#%.db", true, "file:data/we%20ird%23%25.db?"},
		// The driver cuts a plain name at its first '?' and reads the rest as parameters: the read pool opens the
		// same file, and the operator's parameters travel with it.
		{"data/we ird#%.db?_pragma=foreign_keys(1)", true, "file:data/we%20ird%23%25.db?_pragma=foreign_keys(1)&"},
		{"data/p.db?_timeout=100", true, "_timeout=100"},
		{"file:/var/lib/p.db?_pragma=busy_timeout(9000)", true, "_pragma=busy_timeout(9000)"},
	} {
		got, ok := sqliteReadSource(tc.in)
		if ok != tc.ok {
			t.Errorf("%q: ok=%v, want %v (%q)", tc.in, ok, tc.ok, got)
			continue
		}
		if ok && !strings.Contains(got, tc.want) {
			t.Errorf("%q: %q does not contain %q", tc.in, got, tc.want)
		}
		if ok && (!strings.Contains(got, "mode=ro") || !strings.Contains(got, "query_only(1)")) {
			t.Errorf("%q: %q is not read-only", tc.in, got)
		}
		if ok && !sourceSetsBusyTimeout(got) {
			t.Errorf("%q: %q carries no busy timeout", tc.in, got)
		}
		if ok && strings.Count(got, "busy_timeout") > 1 {
			t.Errorf("%q: %q must carry exactly one busy_timeout", tc.in, got)
		}
	}
	// A path with characters a URI treats specially still opens, and is read back.
	dir := t.TempDir()
	odd := filepath.Join(dir, "a b#c%e.db")
	st, err := OpenStore("sqlite", odd+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if readDBOf(st) == nil {
		t.Fatal("no read pool for a path with special characters")
	}
	if err := st.SetSetting("k", "v"); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSetting("k", ""); got != "v" {
		t.Fatalf("read through the pool on an odd path: %q", got)
	}
	// The writer and the readers opened the one file the driver resolved, the name before the '?'.
	if _, err := os.Stat(odd); err != nil {
		t.Fatalf("the database file is not where the driver puts it: %v", err)
	}
	if _, err := os.Stat(odd + "-wal"); err != nil && st.WALBytes() != 0 {
		t.Fatalf("WALBytes disagrees with the file: %v", err)
	}
}

func TestStoreWithoutWALFallsBackToTheWriter(t *testing.T) {
	// A source that pins another journal mode (operator choice, or a filesystem that cannot do WAL)
	// gets no read pool rather than readers that would block the writer.
	st, err := OpenStore("sqlite", "file:"+filepath.Join(t.TempDir(), "p.db")+"?_pragma=journal_mode(DELETE)")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if readDBOf(st) != nil {
		var mode string
		_ = st.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
		t.Fatalf("read pool opened with journal_mode=%s", mode)
	}
	if got := st.GetSetting("nothing", "d"); got != "d" {
		t.Fatalf("fallback read: %q", got)
	}
}

func TestClosingTheStoreClosesTheReadPoolAndLeavesNoWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portal.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting("a", "b")
	_ = st.GetSetting("a", "")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		if info, _ := os.Stat(path + "-wal"); info != nil && info.Size() > 0 {
			t.Fatalf("a cleanly closed store left a non-empty WAL (%d bytes)", info.Size())
		}
	}
	// Reopening sees the data.
	again, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := again.GetSetting("a", ""); got != "b" {
		t.Fatalf("reopened store read %q", got)
	}
}

func TestPostgresStoreHasNoReadPool(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	// connectStore, not OpenStore: the question is the pool, which OpenStore leaves to openReadPool,
	// and the test needs no migrated schema (a shared test database may not allow one) beyond the
	// tables it reads.
	st, err := connectStore("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	key := "readpool:pg:probe"
	defer func() { _ = st.DeleteSetting(key); _ = st.Close() }() // the database is shared: leave no row behind
	st.openReadPool(dsn)                                         // what OpenStore does after initialization: a no-op on PostgreSQL
	if readDBOf(st) != nil || st.readPoolOrNil() != nil {
		t.Fatal("PostgreSQL must not carry a read pool")
	}
	if st.readDB() != st.db {
		t.Fatal("on PostgreSQL the read accessor is the one pool")
	}
	if err := st.SetSetting(key, "pg"); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSetting(key, ""); got != "pg" {
		t.Fatalf("read after write on PostgreSQL: %q", got)
	}
	snap, end := st.beginReadSnapshot(context.Background())
	defer end()
	if _, err := st.newBySymbolFrom(snap, "no-such-symbol", nil); err != nil {
		t.Fatalf("snapshot read on PostgreSQL: %v", err)
	}
}

// readLatencies runs readers against a store whose writer connection is kept busy, and returns the
// sorted read latencies. The writer is held for hold out of every period, the share of the single
// connection the relay's admitted requests occupy in the soak (about a third).
func readLatencies(tb testing.TB, st *Store, hold, period time.Duration, reads int) []time.Duration {
	tb.Helper()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			tx, err := st.db.Begin()
			if err != nil {
				return
			}
			_, _ = tx.Exec(`INSERT INTO meta(k,v) VALUES('readpool:bench','1') ON CONFLICT(k) DO UPDATE SET v=excluded.v`)
			time.Sleep(hold)
			_ = tx.Commit()
			time.Sleep(period - hold)
		}
	}()
	time.Sleep(period)
	out := make([]time.Duration, 0, reads)
	for i := 0; i < reads; i++ {
		start := time.Now()
		_ = st.GetSetting("timezone", "")
		_, _, _ = st.SearchNewLatest(Filters{}, nil)
		out = append(out, time.Since(start))
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestReadsAreNotSerializedBehindTheWriter shows the effect the pool exists for: with the writer
// connection held a third of the time, reads on the single connection wait for it, and reads on the
// read pool do not. The bound is loose (a factor, not a figure) so a slow CI host does not flake it.
func TestReadsAreNotSerializedBehindTheWriter(t *testing.T) {
	const hold, period = 10 * time.Millisecond, 30 * time.Millisecond
	pooled := openFileStore(t)
	single := openFileStore(t)
	single.closeReadPool() // the same store, reads forced back onto the one connection
	if readDBOf(single) != nil {
		t.Fatal("closeReadPool left the pool in place")
	}
	p := readLatencies(t, pooled, hold, period, 120)
	s := readLatencies(t, single, hold, period, 120)
	p95 := func(d []time.Duration) time.Duration { return d[len(d)*95/100] }
	t.Logf("read p95 behind a writer held %v of every %v: single connection %v, read pool %v", hold, period, p95(s), p95(p))
	if p95(s) < 3*time.Millisecond {
		t.Skipf("the single-connection baseline did not queue behind the writer (p95 %v); nothing to compare", p95(s))
	}
	if p95(p)*2 > p95(s) {
		t.Fatalf("read pool p95 %v is not clearly below the single connection's %v", p95(p), p95(s))
	}
}

// BenchmarkSQLiteReadBehindWriter reports read p50 and p95 with the writer connection held a third of
// the time, on the single connection and on the read pool.
func BenchmarkSQLiteReadBehindWriter(b *testing.B) {
	for _, variant := range []string{"single_connection", "read_pool"} {
		b.Run(variant, func(b *testing.B) {
			st := openFileStore(b)
			if variant == "single_connection" {
				st.closeReadPool()
			}
			for i := 0; i < b.N; i++ {
				got := readLatencies(b, st, 10*time.Millisecond, 30*time.Millisecond, 200)
				b.ReportMetric(float64(got[len(got)*95/100].Microseconds()), "p95_us")
				b.ReportMetric(float64(got[len(got)/2].Microseconds()), "p50_us")
			}
		})
	}
}

// TestNamedReadPathsDoNotWaitForTheWriter drives the paths the capacity plan names (the session
// check, the home page, the stock page, the report body and the dispatch decision) while the writer
// connection is held, and fails if any of them waits for it.
func TestNamedReadPathsDoNotWaitForTheWriter(t *testing.T) {
	st := openFileStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	s.names = LoadNames(t.TempDir(), st)
	if err := st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	id, _, err := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-11-01", RType: "t", Title: "600519 t", MD: "# body"})
	if err != nil {
		t.Fatal(err)
	}
	r0 := httptest.NewRequest("GET", "/", nil)
	r0.Header.Set("User-Agent", "Chrome/130.0")
	w0 := httptest.NewRecorder()
	if !s.setSessionCookieFor(w0, r0, *st.GetUser("alice"), time.Hour) {
		t.Fatal("session")
	}
	cookie := w0.Result().Cookies()[0]

	// The activity stamp is a throttled write; take it before the writer is held.
	warm := httptest.NewRequest("GET", "/api/home", nil)
	warm.AddCookie(cookie)
	if s.currentActiveUser(warm) != "alice" {
		t.Fatal("warm-up session check")
	}

	release := holdWriter(t, st)
	defer release()

	within := func(name string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); fn() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("%s waited for the held writer connection", name)
		}
	}
	within("session check", func() {
		r := httptest.NewRequest("GET", "/api/home", nil)
		r.AddCookie(cookie)
		if got := s.currentActiveUser(r); got != "alice" {
			t.Errorf("currentActiveUser = %q", got)
		}
	})
	within("home", func() {
		rec := httptest.NewRecorder()
		s.apiHome(rec, httptest.NewRequest("GET", "/api/home", nil), "alice")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "600519") {
			t.Errorf("home: %d %.120s", rec.Code, rec.Body.String())
		}
	})
	within("stock page reads", func() {
		// The stock page's last step is the report-read audit row, a write, which has to take the
		// writer; every read before it runs on the read pool, as the snapshot does here.
		snap, end := st.beginReadSnapshot(context.Background())
		defer end()
		scope := s.viewerScope("alice")
		reps, err := st.newBySymbolFrom(snap, "600519", scope)
		if err != nil || len(reps) != 1 {
			t.Errorf("stock reads: %d, %v", len(reps), err)
		}
		_ = st.typeConfigsFrom(snap)
		_ = st.versionsFrom(snap)
		_ = s.forecastTimelineDates("alice", "600519", reps)
	})
	within("report body", func() {
		rep, err := st.GetNew(id, nil)
		if err != nil || rep == nil || rep.MD != "# body" {
			t.Errorf("GetNew: %+v, %v", rep, err)
		}
	})
	within("dispatch decision", func() { _ = s.queuedItems() })
}

// TestStockAndHomePagesAnswerOnAFileBackedStore runs the two pages end to end on a store with a read
// pool, so the routed reads are exercised through the real handlers and not only through the helpers.
func TestStockAndHomePagesAnswerOnAFileBackedStore(t *testing.T) {
	st := openFileStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	s.names = LoadNames(t.TempDir(), st)
	if err := st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-11-01", "2026-11-02"} {
		if _, _, err := st.UpsertReport(Rep{Symbol: "600519", Date: d, RType: "t", Title: "600519 t " + d, MD: "# body " + d}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	s.apiHome(rec, httptest.NewRequest("GET", "/api/home", nil), "alice")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "600519") {
		t.Fatalf("home: %d %.200s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/stock/600519?date=2026-11-01", nil)
	req.SetPathValue("symbol", "600519")
	s.apiStock(rec, req, "alice")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "# body 2026-11-01") {
		t.Fatalf("stock: %d %.300s", rec.Code, rec.Body.String())
	}
	// The read audit row the page wrote is on the writer and visible to the next read.
	var n int
	if err := st.readQueryRow(`SELECT COUNT(*) FROM audit_log WHERE action=?`, AuditReportRead).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read audit rows: %d, %v", n, err)
	}
}

func TestBusyTimeoutIsNotAddedWhenTheOperatorSetsOne(t *testing.T) {
	for _, src := range []string{
		"p.db?_pragma=busy_timeout(7)",
		"p.db?_busy_timeout=7",
		"p.db?_timeout=7",
		"file:p.db?_timeout=7&cache=shared",
	} {
		if got := sqliteSource(src); got != src {
			t.Errorf("sqliteSource(%q) = %q, want it left as written", src, got)
		}
		if got := sqliteWriterSource(src); strings.Contains(got, "busy_timeout(5000)") {
			t.Errorf("sqliteWriterSource(%q) = %q added a second busy timeout", src, got)
		}
	}
	if got := sqliteSource("p.db"); got != "p.db?_pragma=busy_timeout(5000)" {
		t.Errorf("sqliteSource(plain) = %q", got)
	}
	if got := sqliteWriterSource("p.db?_pragma=journal_size_limit(1)"); strings.Count(got, "journal_size_limit") != 1 {
		t.Errorf("a source that names journal_size_limit must keep its own: %q", got)
	}
}

// ---------- filesystem ----------

func TestNetworkAndFuseFilesystemsAreUnsafeForWAL(t *testing.T) {
	for magic, want := range map[uint32]struct {
		name   string
		unsafe bool
	}{
		0x6969: {"nfs", true}, 0x517B: {"smb", true}, 0xFF534D42: {"cifs", true}, 0xFE534D42: {"smb2", true},
		0x65735546: {"fuse", true}, 0x01021997: {"9p", true},
		0xEF53: {"ext4", false}, 0x58465342: {"xfs", false}, 0x01021994: {"tmpfs", false}, 0x794C7630: {"overlayfs", false},
		0x12345678: {"unlisted", false},
	} {
		name, unsafe := classifyLinuxFilesystem(magic)
		if name != want.name || unsafe != want.unsafe {
			t.Errorf("magic %#x = %q unsafe=%v, want %q unsafe=%v", magic, name, unsafe, want.name, want.unsafe)
		}
	}
}

func withFilesystemCheck(t *testing.T, check func(string) (string, bool, error)) {
	t.Helper()
	old := sqliteFilesystemCheck
	sqliteFilesystemCheck = check
	t.Cleanup(func() { sqliteFilesystemCheck = old })
}

func TestNoWALAndNoReadPoolOnANetworkFilesystem(t *testing.T) {
	withFilesystemCheck(t, func(string) (string, bool, error) { return "nfs", true, nil })
	st := openFileStore(t)
	if st.readPoolOrNil() != nil {
		t.Fatal("a read pool was opened on a network filesystem")
	}
	var mode string
	if err := st.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode == "wal" {
		t.Fatalf("journal_mode = %q (%v): the file must not be switched to WAL there", mode, err)
	}
	state := st.PoolState()
	if state.ReadPool != readPoolDisabled || !strings.Contains(state.Reason, "nfs") || state.Reader != nil {
		t.Fatalf("state = %+v", state)
	}
	if err := st.SetSetting("k", "v"); err != nil || st.GetSetting("k", "") != "v" {
		t.Fatalf("the single connection must keep working: %v", err)
	}
}

func TestNoWALAndNoReadPoolWhenTheFilesystemCannotBeIdentified(t *testing.T) {
	withFilesystemCheck(t, func(string) (string, bool, error) { return "", false, errors.New("statfs: boom") })
	st := openFileStore(t)
	var mode string
	_ = st.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if st.readPoolOrNil() != nil || mode == "wal" {
		t.Fatalf("fail-safe: no pool and no WAL when detection errors (pool=%v mode=%s)", st.readPoolOrNil() != nil, mode)
	}
	if state := st.PoolState(); state.ReadPool != readPoolDisabled || !strings.Contains(state.Reason, "boom") {
		t.Fatalf("state = %+v", state)
	}
}

func TestTheRealFilesystemDetectionIdentifiesALocalDirectory(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("filesystem identification is implemented on Linux and macOS")
	}
	f := filepath.Join(t.TempDir(), "x.db")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, unsafe, err := detectSQLiteFilesystem(f)
	if err != nil {
		t.Fatalf("detection failed on a local file: %v", err)
	}
	if unsafe || name == "" {
		t.Fatalf("a temp directory was classified %q unsafe=%v", name, unsafe)
	}
	if _, _, err := detectSQLiteFilesystem(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing file must be an error (the fail-safe), not a verdict")
	}
}

// ---------- WAL size ----------

func walFileSize(t *testing.T, st *Store) int64 {
	t.Helper()
	info, err := os.Stat(st.walPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

// growWAL commits about mib MiB while a reader pins the log, so no checkpoint can finish and the log
// grows. It returns the func that releases the reader.
func growWAL(t *testing.T, st *Store, mib int) (release func()) {
	t.Helper()
	if _, err := st.exec(`CREATE TABLE IF NOT EXISTS walgrow(id INTEGER PRIMARY KEY, v BLOB)`); err != nil {
		t.Fatal(err)
	}
	var seed int
	_ = st.readQueryRow(`SELECT COUNT(*) FROM walgrow`).Scan(&seed)
	rtx, err := readDBOf(st).Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := rtx.QueryRow(`SELECT COUNT(*) FROM walgrow`).Scan(&n); err != nil { // takes the snapshot
		t.Fatal(err)
	}
	blob := make([]byte, 256<<10)
	for i := 0; i < mib*4; i++ {
		if _, err := st.exec(`INSERT INTO walgrow(v) VALUES(?)`, blob); err != nil {
			t.Fatal(err)
		}
	}
	return func() { _ = rtx.Rollback() }
}

func TestTheWALShrinksAfterLoad(t *testing.T) {
	old := sqliteJournalSizeLimit
	sqliteJournalSizeLimit = 1 << 20
	t.Cleanup(func() { sqliteJournalSizeLimit = old })
	st := openFileStore(t)
	release := growWAL(t, st, 12)
	grown := walFileSize(t, st)
	if grown < 8<<20 {
		release()
		t.Fatalf("the load did not grow the WAL (%d bytes); the test proves nothing", grown)
	}
	release()
	frames, bytes, err := st.checkpointWAL()
	if err != nil {
		t.Fatal(err)
	}
	if bytes > sqliteJournalSizeLimit {
		t.Fatalf("after the maintenance pass the WAL is %d bytes (%d frames before), want at most %d", bytes, frames, sqliteJournalSizeLimit)
	}
	t.Logf("WAL %d bytes after the load, %d after the checkpoint", grown, bytes)
}

func TestJournalSizeLimitTruncatesTheLogWhenItRestarts(t *testing.T) {
	old := sqliteJournalSizeLimit
	sqliteJournalSizeLimit = 1 << 20
	t.Cleanup(func() { sqliteJournalSizeLimit = old })
	st := openFileStore(t)
	release := growWAL(t, st, 12)
	if walFileSize(t, st) < 8<<20 {
		release()
		t.Fatal("the load did not grow the WAL")
	}
	release()
	// Everything copied back (a passive checkpoint), then one small write restarts the log, and the
	// limit cuts the file back instead of leaving it at its high-water mark.
	var busy, logFrames, done int64
	if err := st.db.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &logFrames, &done); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`INSERT INTO walgrow(v) VALUES(x'00')`); err != nil {
		t.Fatal(err)
	}
	if got := walFileSize(t, st); got > 2<<20 {
		t.Fatalf("WAL is %d bytes after the log restarted, want about the %d byte limit", got, sqliteJournalSizeLimit)
	}
}

func TestTheWALMaintenanceRunsOnATickerAndStopsWithTheStore(t *testing.T) {
	oldI, oldL := walCheckpointInterval, sqliteJournalSizeLimit
	walCheckpointInterval, sqliteJournalSizeLimit = 20*time.Millisecond, 1<<20
	t.Cleanup(func() { walCheckpointInterval, sqliteJournalSizeLimit = oldI, oldL })
	st, err := OpenStore("sqlite", filepath.Join(t.TempDir(), "portal.db"))
	if err != nil {
		t.Fatal(err)
	}
	release := growWAL(t, st, 12)
	release()
	deadline := time.Now().Add(5 * time.Second)
	for walFileSize(t, st) > sqliteJournalSizeLimit && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := walFileSize(t, st); got > sqliteJournalSizeLimit {
		t.Fatalf("the ticker never brought the WAL down: %d bytes", got)
	}
	done := make(chan struct{})
	go func() { _ = st.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the maintenance goroutine")
	}
}

// ---------- concurrency and failure ----------

func TestClosingTheReadPoolUnderLiveReadersIsRaceFreeAndNeverPanics(t *testing.T) {
	st := openFileStore(t)
	if err := st.SetSetting("k", "v"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = st.GetSetting("k", "d")
				var n int
				_ = st.readQueryRow("SELECT COUNT(*) FROM meta").Scan(&n)
				snap, end := st.beginReadSnapshot(context.Background())
				_ = snap.queryRow("SELECT COUNT(*) FROM meta").Scan(&n)
				end()
				_ = st.PoolState()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	st.closeReadPool()
	time.Sleep(50 * time.Millisecond)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
	// After Close every read answers with a closed-database error, not a panic.
	var n int
	err := st.readQueryRow("SELECT COUNT(*) FROM meta").Scan(&n)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("read after Close = %v, want a closed-database error", err)
	}
	if got := st.GetSetting("k", "default"); got != "default" {
		t.Fatalf("GetSetting after Close = %q", got)
	}
}

func TestAReadPoolFailureIsRetriedOnTheWriterNotReturnedAsADefault(t *testing.T) {
	st := openFileStore(t)
	if err := st.SetSetting("notification_dispatch_paused", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	readDBOf(st).Close() // the read pool breaks under the store; the store still believes in it
	if got := st.GetSetting("notification_dispatch_paused", "0"); got != "1" {
		t.Fatalf("a broken read pool turned a set flag into its default %q", got)
	}
	if u := st.GetUser("alice"); u == nil || u.Username != "alice" {
		t.Fatalf("a broken read pool made an existing account look missing: %+v", u)
	}
	if got := st.GetSetting("never_set", "dflt"); got != "dflt" {
		t.Fatalf("an unset key must still return the default: %q", got)
	}
}

// ---------- state ----------

func TestPoolStateReportsTheReadPool(t *testing.T) {
	st := openFileStore(t)
	_ = st.GetSetting("x", "")
	state := st.PoolState()
	if state.ReadPool != readPoolOpen || state.Reader == nil || state.Reader.Max != sqliteReadPoolSize || state.Writer.Max != 1 {
		t.Fatalf("state = %+v", state)
	}
	if state.Filesystem == "" {
		t.Errorf("the filesystem is not reported: %+v", state)
	}
	if mem := newTestStore(t).PoolState(); mem.ReadPool != readPoolNotApplicable || mem.Reader != nil {
		t.Fatalf("in-memory state = %+v", mem)
	}
	st.closeReadPool()
	if got := st.PoolState().ReadPool; got != readPoolClosed {
		t.Fatalf("after close: %q", got)
	}
}

func TestStorageUsageEndpointReportsThePoolState(t *testing.T) {
	st := openFileStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	rec := httptest.NewRecorder()
	s.apiCleanupUsage(rec, httptest.NewRequest("GET", "/api/admin/cleanup/usage", nil), "admin")
	var body struct {
		Database StorePoolState `json:"database"`
		DBBytes  int64          `json:"db_bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %.200s", err, rec.Body.String())
	}
	if body.Database.ReadPool != readPoolOpen || body.Database.Reader == nil {
		t.Fatalf("database = %+v", body.Database)
	}
}
