package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The PostgreSQL tests used to share the one database TEST_POSTGRES_DSN names. That forced them to run
// one at a time (each cleans the tables it uses, and two at once delete each other's rows), and it made
// them depend on what earlier tests left behind in the tables nobody cleans: a fixture that picked "the"
// session row found another test's, and an assertion that counted rows counted another test's too.
// Which earlier test that was depended on the order the tests happened to run in.
//
// testPostgresDSN instead hands each top-level test a database of its own, copied from a template that
// one OpenStore initialized at the start of the process: the PostgreSQL counterpart of newTestStore's
// SQLite image. Every test therefore starts from the same freshly initialized database, whatever ran
// before it, and tests marked t.Parallel run their PostgreSQL halves side by side. A small pool of slot
// databases (<name>_s1, <name>_s2, ...) is kept ready: a released slot is dropped and copied again in
// the background, so a test seldom waits for the copy.
//
// The named database itself is only where the pool connects to create the others. When the role may
// not create databases, the pool falls back to it alone, which is the old behaviour: every PostgreSQL
// test waits for the one before it and sees what earlier tests left. Grant CREATEDB to the test role to
// get the isolated pool.
//
// Every test that reaches PostgreSQL must get its DSN here, never from the environment directly; the
// guard in TestPostgresTestsTakeTheirDatabaseFromThePool enforces that.

// testPostgresPoolMax bounds the pool. Each store opens up to ten connections and some tests open
// several stores, so a wider pool would run into the server's default max_connections of 100.
const testPostgresPoolMax = 4

// testPostgresSlotWait bounds how long a test waits for a database. The longest PostgreSQL test runs for
// seconds and only a few wait ahead of any one, so reaching it means a lease that is never released,
// which is then reported with the tests holding the pool instead of as a silent hang.
const testPostgresSlotWait = 10 * time.Minute

// testPostgresPoolLock is the advisory lock (taken on the named database) a test process holds while it
// owns the template and slot databases, so two test processes on one server never drop each other's.
const testPostgresPoolLock = 0x72707470 // "rptp"

type testPostgresSlot struct {
	name string
	err  error // the background copy failed; the next test to take the slot reports it
}

type testPostgresPoolState struct {
	once     sync.Once
	err      error
	shared   bool     // no CREATE DATABASE: the named database alone, one test at a time
	base     *url.URL // the named database, for a URL DSN
	keyword  string   // the DSN as given, for a keyword/value DSN
	admin    *sql.DB  // connected to the named database, for CREATE and DROP DATABASE
	lock     *sql.Conn
	template string
	free     chan testPostgresSlot

	mu   sync.Mutex
	held map[string]string // test name (subtests by their full name) -> the DSN it holds
}

var testPostgresPool testPostgresPoolState

// testPostgresDSN returns the DSN of a PostgreSQL database the calling test holds alone until its
// cleanup, and skips the test when TEST_POSTGRES_DSN is not set. A test that already holds one, or
// whose parent (at any depth) does, gets that same database, so a test that opens several stores on
// "the" database, in itself or in its subtests, still sees one database. A subtest whose ancestors hold
// none takes a database of its own, released when the subtest ends: siblings never share one that the
// first to finish would drop.
func testPostgresDSN(t testing.TB) string {
	t.Helper()
	if os.Getenv("TEST_POSTGRES_DSN") == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	p := &testPostgresPool
	p.once.Do(func() { p.err = p.open(t, os.Getenv("TEST_POSTGRES_DSN")) })
	if p.err != nil {
		t.Fatalf("postgres test pool: %v", p.err)
	}
	name := t.Name()
	if dsn, ok := p.heldFor(name); ok {
		return dsn
	}
	var slot testPostgresSlot
	select {
	case slot = <-p.free:
	case <-time.After(testPostgresSlotWait):
		p.mu.Lock()
		holders := make([]string, 0, len(p.held))
		for n := range p.held {
			holders = append(holders, n)
		}
		p.mu.Unlock()
		sort.Strings(holders)
		t.Fatalf("postgres test pool: no database freed in %v; held by %v", testPostgresSlotWait, holders)
	}
	if slot.err != nil {
		// The copy made when the slot was last released failed, and the slot may have no database at
		// all. Copy again now, so a passing retry costs nothing and a failing one is reported here.
		if err := p.copyTemplate(slot.name); err != nil {
			p.free <- testPostgresSlot{name: slot.name, err: err}
			t.Fatalf("postgres test pool: preparing %s: %v (the earlier attempt: %v)", slot.name, err, slot.err)
		}
	}
	dsn := p.dsnOf(slot.name)
	p.mu.Lock()
	p.held[name] = dsn
	p.mu.Unlock()
	// Registered before the caller's own cleanups, so it runs after them: the stores the test opened are
	// closed before the database is dropped and copied again for the next test.
	t.Cleanup(func() {
		p.mu.Lock()
		delete(p.held, name)
		p.mu.Unlock()
		if p.shared {
			p.free <- slot
			return
		}
		go func() { p.free <- testPostgresSlot{name: slot.name, err: p.copyTemplate(slot.name)} }()
	})
	return dsn
}

// heldFor is the database the named test or its nearest ancestor holds, if any.
func (p *testPostgresPoolState) heldFor(name string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if dsn, ok := p.held[name]; ok {
			return dsn, true
		}
		i := strings.LastIndexByte(name, '/')
		if i < 0 {
			return "", false
		}
		name = name[:i]
	}
}

// dsnOf is the DSN of database name on the server TEST_POSTGRES_DSN names, in the form it was given.
func (p *testPostgresPoolState) dsnOf(name string) string {
	if p.base != nil {
		u := *p.base
		u.Path = "/" + name
		return u.String()
	}
	return testPostgresKeywordDSN(p.keyword, name)
}

var testPostgresDBName = regexp.MustCompile(`(^|\s)dbname=(?:'(?:[^'\\]|\\.)*'|\S*)`)

// testPostgresKeywordDSN replaces (or adds) dbname in a keyword/value DSN such as
// "host=/run/postgresql dbname=reports_test".
func testPostgresKeywordDSN(dsn, name string) string {
	quoted := "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name) + "'"
	if testPostgresDBName.MatchString(dsn) {
		return testPostgresDBName.ReplaceAllStringFunc(dsn, func(m string) string {
			if strings.HasPrefix(m, " ") || strings.HasPrefix(m, "\t") {
				return m[:1] + quoted
			}
			return quoted
		})
	}
	return strings.TrimSpace(dsn) + " " + quoted
}

// open takes the pool lock, builds the template and the slots. It runs once per test process.
func (p *testPostgresPoolState) open(t testing.TB, base string) error {
	cfg, err := pgconn.ParseConfig(base)
	if err != nil {
		return err
	}
	name := cfg.Database
	if name == "" {
		return errors.New("TEST_POSTGRES_DSN names no database")
	}
	if strings.Contains(base, "://") {
		if p.base, err = url.Parse(base); err != nil {
			return err
		}
	} else {
		p.keyword = base
	}
	p.held = map[string]string{}
	if p.admin, err = sql.Open("pgx", base); err != nil {
		return err
	}
	ctx := context.Background()
	if p.lock, err = p.admin.Conn(ctx); err != nil {
		return err
	}
	// Held on this connection until the process exits. A second test process on the same server waits
	// here for the first to finish instead of dropping the databases under it, and says so.
	for waited := time.Duration(0); ; waited += time.Second {
		var got bool
		if err := p.lock.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, testPostgresPoolLock).Scan(&got); err != nil {
			return err
		}
		if got {
			break
		}
		if waited%(10*time.Second) == 0 {
			t.Logf("postgres test pool: another test process holds the pool on this server; waiting (%v so far)", waited)
		}
		time.Sleep(time.Second)
	}
	p.template = name + "_tpl"
	if err := p.exec(`DROP DATABASE IF EXISTS ` + pgIdent(p.template) + ` WITH (FORCE)`); err != nil {
		return err
	}
	if err := p.exec(`CREATE DATABASE ` + pgIdent(p.template)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42501" { // insufficient_privilege
			t.Logf("postgres test pool: the role may not create databases, so every PostgreSQL test shares %s and runs alone (%v)", name, err)
			p.shared = true
			p.free = make(chan testPostgresSlot, 1)
			p.free <- testPostgresSlot{name: name}
			return nil
		}
		return err
	}
	st, err := OpenStore("postgres", p.dsnOf(p.template))
	if err != nil {
		return fmt.Errorf("initialize the template: %w", err)
	}
	st.Close()
	size := min(runtime.GOMAXPROCS(0), testPostgresPoolMax)
	p.free = make(chan testPostgresSlot, size)
	for i := 1; i <= size; i++ {
		slot := name + "_s" + strconv.Itoa(i)
		if err := p.copyTemplate(slot); err != nil {
			return err
		}
		p.free <- testPostgresSlot{name: slot}
	}
	return nil
}

// copyTemplate replaces the slot database with a fresh copy of the template. FORCE ends any connection a
// test leaked to the slot, which would otherwise block the drop.
func (p *testPostgresPoolState) copyTemplate(slot string) error {
	if err := p.exec(`DROP DATABASE IF EXISTS ` + pgIdent(slot) + ` WITH (FORCE)`); err != nil {
		return err
	}
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		// Two copies of one template at the same moment can refuse with "source database is being
		// accessed by other users" (55006); it clears as soon as the other copy finishes.
		if err = p.exec(`CREATE DATABASE ` + pgIdent(slot) + ` TEMPLATE ` + pgIdent(p.template)); err == nil {
			return nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55006" {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}

func (p *testPostgresPoolState) exec(q string) error {
	_, err := p.admin.ExecContext(context.Background(), q)
	return err
}

func pgIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// TestPostgresTestsTakeTheirDatabaseFromThePool keeps every PostgreSQL test on the pool. A test that read
// TEST_POSTGRES_DSN itself would connect to the named database, which the pool only creates databases
// from, and in the shared fallback would use it while another test holds it. Reading the variable only to
// skip (== "" or != "") is fine.
func TestPostgresTestsTakeTheirDatabaseFromThePool(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`"TEST_POSTGRES_DSN"\)(\s*[!=]=\s*"")?`)
	for _, f := range files {
		if f == "postgres_test_pool_test.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range direct.FindAllSubmatch(src, -1) {
			if m[1] == nil {
				t.Errorf("%s reads TEST_POSTGRES_DSN for a connection; use testPostgresDSN(t)", f)
			}
		}
	}
}

// The pool names its databases in whichever DSN form TEST_POSTGRES_DSN used.
func TestPostgresPoolNamesDatabasesInTheGivenDSNForm(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"host=/run/pg dbname=reports_test sslmode=disable", "host=/run/pg dbname='reports_test_s1' sslmode=disable"},
		{"dbname='reports test' host=/run/pg", "dbname='reports_test_s1' host=/run/pg"},
		{"host=/run/pg", "host=/run/pg dbname='reports_test_s1'"},
	} {
		if got := testPostgresKeywordDSN(c.in, "reports_test_s1"); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	u, err := url.Parse("postgres://portal:portal@localhost:5432/reports_test?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := (&testPostgresPoolState{base: u}).dsnOf("reports_test_s2"), "postgres://portal:portal@localhost:5432/reports_test_s2?sslmode=disable"; got != want {
		t.Errorf("url form: got %q, want %q", got, want)
	}
}

// A test and its subtests at any depth share the lease the nearest of them holds, so a fixture that asks
// twice gets one database (asking again must never wait for a second slot); siblings share nothing.
func TestPostgresPoolLeasesFollowTheTestTree(t *testing.T) {
	t.Parallel()
	p := &testPostgresPoolState{held: map[string]string{"A/sub": "db1", "B": "db2"}}
	for name, want := range map[string]string{"A/sub": "db1", "A/sub/deeper": "db1", "B": "db2", "B/x/y": "db2", "A": "", "A/other": "", "C": ""} {
		got, ok := p.heldFor(name)
		if got != want || ok != (want != "") {
			t.Errorf("%s: got %q %v, want %q", name, got, ok, want)
		}
	}
}
