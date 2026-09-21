package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The route table (routes.go): what every registration accepts, and the proof that nothing registers
// a route another way.
//
// This matters because of what builds on it. A policy that has to hold every signed-in reader
// somewhere (the forced-enrolment gate) is only as complete as its list of routes, and the failure it
// would ship with is the route somebody added later without thinking about it. A test that asks the
// table is a test that keeps answering after the next registration; a test that reads the file for a
// spelling stops answering the moment the spelling changes.

func routeTableServer(t *testing.T) *Server {
	t.Helper()
	return &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle(), v1Rate: newRateLimiter()}
}

// TestNothingRegistersARouteOutsideTheTable is the structural half: `mux.HandleFunc` appears in
// routes.go and nowhere else in the package, so a new route cannot skip the table — and therefore
// cannot skip the class it would have to declare.
func TestNothingRegistersARouteOutsideTheTable(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "routes.go" {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		if strings.Contains(string(raw), "mux.HandleFunc(") {
			t.Errorf("%s registers a route directly: registration goes through the helpers in routes.go, "+
				"which are what records the credential each route accepts", name)
		}
	}
	if checked == 0 {
		t.Fatal("no files were checked; the scan is broken, not the package")
	}
}

// TestEveryPatternIsRegisteredOnce is the table's own shape: the mux would panic on a duplicate, so
// this is about the table agreeing with the mux rather than about finding one.
func TestEveryPatternIsRegisteredOnce(t *testing.T) {
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	if len(s.routeTable) < 150 {
		t.Fatalf("the route table holds %d patterns — the registrations did not all go through it", len(s.routeTable))
	}
	seen := map[string]bool{}
	for _, row := range s.routeTable {
		if seen[row.pattern] {
			t.Errorf("%s is registered twice", row.pattern)
		}
		seen[row.pattern] = true
	}
	// A perm route with no permission named would be an admin route with the check missing.
	for _, row := range s.routeTable {
		if row.class == authPerm && strings.TrimSpace(row.perm) == "" {
			t.Errorf("%s is registered as a permission route with no permission", row.pattern)
		}
	}
}

// TestSessionRoutesRefuseAnAnonymousCaller is the behavioural half, and the one that cannot be
// satisfied by a well-worded comment: every route in the session set is called with no credential at
// all and has to refuse. It walks the TABLE rather than a hand-written list, so a route added later
// is covered the moment it is registered.
func TestSessionRoutesRefuseAnAnonymousCaller(t *testing.T) {
	s := routeTableServer(t)
	mux := http.NewServeMux()
	s.wireRoutes(mux)

	routes := s.sessionRoutes()
	if len(routes) < 100 {
		t.Fatalf("only %d session routes — the class table is not what it should be", len(routes))
	}
	for _, row := range routes {
		method, path, ok := strings.Cut(row.pattern, " ")
		if !ok {
			t.Fatalf("pattern %q has no method", row.pattern)
		}
		// A concrete path: {symbol} and {id} take any value, and the refusal happens long before a
		// handler would look at it.
		concrete := strings.ReplaceAll(strings.ReplaceAll(path, "{id}", "1"), "{symbol}", "600519")
		for _, param := range []string{"{key}", "{slug}", "{name}", "{rev}", "{market}"} {
			concrete = strings.ReplaceAll(concrete, param, "x")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, concrete, nil))
		switch row.class {
		case authSessionOrBearer:
			// A session is one way in and a machine token the other; neither is present here.
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s → %d without a session or a token, want 401", method, path, rec.Code)
			}
		default:
			// The JSON surface answers 401; the report pages send a browser to the login form.
			if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusSeeOther {
				t.Errorf("%s %s → %d without a session, want 401 (JSON) or 303 (page)", method, path, rec.Code)
			}
		}
	}
}

// The machine surface is the one place a session is NOT accepted, and the table should say so: a
// token opens it (or nothing does), which is what keeps the browser's cookie out of it.
func TestTheMachineSurfaceIsNotASessionClass(t *testing.T) {
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	var v1 int
	for _, row := range s.routeTable {
		if strings.Contains(row.pattern, " /api/v1/") {
			v1++
			if row.class != authBearer {
				t.Errorf("%s is registered as class %v, want authBearer", row.pattern, row.class)
			}
		}
	}
	if v1 == 0 {
		t.Fatal("no /api/v1 routes were found")
	}
}

// The omnibox is the one route that takes EITHER credential, and it has to stay in the session set:
// a browser calls it with a cookie, so a policy that skips it leaves a readable hole.
func TestTheOmniboxIsASessionRoute(t *testing.T) {
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	for _, row := range s.routeTable {
		if row.pattern == "GET /api/symbols" {
			if row.class != authSessionOrBearer {
				t.Errorf("/api/symbols is class %v, want authSessionOrBearer", row.class)
			}
			return
		}
	}
	t.Error("/api/symbols is not registered")
}
