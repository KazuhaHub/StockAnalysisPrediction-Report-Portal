package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// The forecast access decision must come from one strict account/tree snapshot. These are the
// tenancy helpers that answer leniently (an unreadable row reads as "not restricted", a broken
// chain is shortened, a missing user is nil), so none of them may appear where the decision is made
// or consumed.
var forecastLenientHelpers = map[string]bool{
	"isRestricted": true, "isAdmin": true, "GetUser": true, "groupChain": true,
	"EffectiveGroupSettings": true, "GroupRestrictedEffective": true,
}

// forecastGateCalls are the functions that produce or consume the forecast decision. A function
// that calls one of them is a gate call site.
var forecastGateCalls = map[string]bool{
	"forecastAccess": true, "requireForecastAccess": true, "loadForecastSubject": true, "resolveForecastScope": true,
}

// forecastLenientExemptions are display-only uses inside a gate call site, each with the reason it
// cannot influence the decision. A stale entry fails the scan, so the list cannot quietly widen.
var forecastLenientExemptions = map[string]map[string]string{
	"meJSON": {
		"GetUser": "reads the display name and role for /api/me; the forecast hint is forecastAccess alone",
		"isAdmin": "the /api/me admin flag for the UI; the forecast hint is forecastAccess alone",
	},
}

// lenientCalls lists the lenient helpers called anywhere under node.
func lenientCalls(node ast.Node) []string {
	var found []string
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		if forecastLenientHelpers[name] {
			found = append(found, name)
		}
		return true
	})
	return found
}

func callsForecastGate(fn *ast.FuncDecl) bool {
	if fn.Body == nil {
		return false
	}
	calls := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			switch f := call.Fun.(type) {
			case *ast.Ident:
				calls = calls || forecastGateCalls[f.Name]
			case *ast.SelectorExpr:
				calls = calls || forecastGateCalls[f.Sel.Name]
			}
		}
		return true
	})
	return calls
}

// TestForecastScopeHasNoLenientRead is the source scan plan section 7.3.1 asks for: forecast_access.go
// in full, and every gate call site. In a forecast_* file the whole file is scanned (those files exist
// to serve the forecast decision); in a shared file only the functions that call the gate are.
func TestForecastScopeHasNoLenientRead(t *testing.T) {
	// The scanner must see what it is looking for, or a clean result means nothing.
	probe, err := parser.ParseFile(token.NewFileSet(), "probe.go", `package p
func f() { s.st.GetUser(u); isAdmin(u); s.groupChain(1) }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := lenientCalls(probe); len(got) != 3 {
		t.Fatalf("scanner missed lenient calls in a known-bad snippet: %v", got)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	callSites := map[string]bool{}
	usedExemptions := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var sites []*ast.FuncDecl
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && callsForecastGate(fn) {
				sites = append(sites, fn)
				callSites[name+":"+fn.Name.Name] = true
			}
		}
		if len(sites) == 0 && name != "forecast_access.go" {
			continue
		}
		if name == "forecast_access.go" || strings.HasPrefix(name, "forecast_") {
			for _, helper := range lenientCalls(file) {
				t.Errorf("%s reaches the forecast decision through lenient helper %s", name, helper)
			}
			continue
		}
		for _, fn := range sites {
			for _, helper := range lenientCalls(fn) {
				if _, ok := forecastLenientExemptions[fn.Name.Name][helper]; ok {
					usedExemptions[fn.Name.Name+"."+helper] = true
					continue
				}
				t.Errorf("%s:%s is a forecast gate call site and calls lenient helper %s", name, fn.Name.Name, helper)
			}
		}
	}
	for fn, helpers := range forecastLenientExemptions {
		for helper := range helpers {
			if !usedExemptions[fn+"."+helper] {
				t.Errorf("stale lenient-helper exemption %s.%s: remove it", fn, helper)
			}
		}
	}
	// The sites named by the plan must be among those found, so a rename cannot hollow the scan out.
	for _, want := range []string{
		"forecast_access.go:forecastAccess", "forecast_access.go:requireForecastAccess",
		"forecast_groups_api.go:apiReaderForecastGroups", "forecast_groups_members.go:apiReaderForecastGroupMembers",
		"forecast_read_api.go:forecastCookiePredictionView", "forecast_access_preview.go:apiForecastAccess",
		"apiui.go:meJSON", "user_admin_api.go:apiAdminGroups",
	} {
		if !callSites[want] {
			var have []string
			for site := range callSites {
				have = append(have, site)
			}
			sort.Strings(have)
			t.Errorf("gate call site %s not found by the scan; found %v", want, have)
		}
	}
}
