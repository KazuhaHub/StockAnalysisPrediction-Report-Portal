package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Fails (the migration runs) when the image is not on the host yet and cannot be put there: no start-class
// effect may be claimed for an image the runner does not hold.
func TestMigrateDataIsNotSentWhenTheReleaseImageCannotBeLoaded(t *testing.T) {
	s, lease, endpoint, _, images := platformImageEnsureFixture(t)
	migration := &platformFakeMigrationRunner{t: t}
	s.platformMigration = migration
	images.rejectLoad = "load_rejected"
	if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); err == nil {
		t.Fatal("the migration was dispatched without an image")
	}
	if len(migration.calls) != 0 {
		t.Fatalf("a runner effect was sent: %v", migration.calls)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, lease.id); n != 0 {
		t.Fatal("a migration effect row was written for an image the runner does not hold")
	}
}

// Fails if the migration starts before the image is present, or if the load is skipped.
func TestMigrateDataRunsAfterTheImageIsLoaded(t *testing.T) {
	s, lease, endpoint, _, images := platformImageEnsureFixture(t)
	migration := &platformFakeMigrationRunner{t: t}
	s.platformMigration = migration
	s.platformSettleStart = platformFakeSettleStart(migration, nil)
	migration.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		if !images.present {
			t.Error("migrate-data was sent before the image was loaded")
		}
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	next, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart {
		t.Fatal(next.snapshot, err)
	}
	if images.count("load") == 0 {
		t.Fatal("the image was never loaded")
	}
}

// Fails (the start is reserved) when the image of the target release is not available for the update start.
func TestUpdateStartIsNotReservedWhenTheReleaseImageCannotBeLoaded(t *testing.T) {
	s, lease, endpoint, _, images := platformImageEnsureFixture(t)
	migration := &platformFakeMigrationRunner{t: t}
	s.platformMigration = migration
	images.rejectLoad = "load_rejected"
	lease.snapshot.Stage = appplatform.StageStart
	if _, err := s.executePlatformUpdateStart(context.Background(), lease, endpoint); err == nil {
		t.Fatal("the update start ran without an image")
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id); n != 0 {
		t.Fatal("a start effect was prepared for an image the runner does not hold")
	}
}

// startSenders are the calls that put a start-class command on the runner: they claim or reserve a container
// (candidate, update start, writable start), run the data writer or the migration job. Each needs the release
// image on the host first.
var startSenders = map[string]bool{
	"platformStartCandidate": true, "platformReserveUpdateStart": true, "platformExecuteUpdateStart": true,
	"platformRunWritableStart": true, "platformRunWriter": true, "ExecuteMigrate": true,
}

// readOnlyStartPaths are functions that reach a sender only to read an effect back or to reconcile one an earlier
// gated attempt sent; they send nothing new, so the gate is not theirs. Each is named with the reason.
var readOnlyStartPaths = map[string]string{
	"completePlatformInstallWriter":  "reads the writer receipt back",
	"reconcilePlatformInstallWriter": "reconciles a writer an earlier gated attempt sent",
	"runPlatformInstallWriter":       "dispatch, complete and reconcile share it; only dispatch sends, and it is gated by its caller",
	"runPlatformWritableStart":       "dispatch, complete and reconcile share it; only dispatch sends, and it is gated by its caller",
	"completePlatformWritableStart":  "reads the start back",
	"reconcilePlatformWritableStart": "reconciles a start an earlier gated attempt sent",
	"settlePlatformMigration":        "settlement reads back and discards; it never sends a migration",
	"waitPlatformWritableCandidate":  "polls readiness of a start an earlier gated attempt sent",
	"probePlatformWritableCandidate": "probes readiness of a start an earlier gated attempt sent",
}

// Fails if any function that calls a start sender can run without the image gate before it: the gate must precede
// the sender in the same function, or every non-test caller of the function must itself be gated. A new start
// path is found by its sender call, so it cannot be added without the gate.
func TestEveryStartSenderIsPrecededByTheImageGate(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	type fn struct {
		name    string
		senders []token.Pos
		gate    token.Pos
		calls   map[string][]token.Pos
		file    string
	}
	funcs := map[string]*fn{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Body == nil {
				continue
			}
			info := &fn{name: d.Name.Name, calls: map[string][]token.Pos{}, file: f}
			ast.Inspect(d.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch v := call.Fun.(type) {
				case *ast.Ident:
					name = v.Name
				case *ast.SelectorExpr:
					name = v.Sel.Name
				}
				switch {
				case name == "ensurePlatformReleaseImage":
					if info.gate == 0 || call.Pos() < info.gate {
						info.gate = call.Pos()
					}
				case startSenders[name]:
					info.senders = append(info.senders, call.Pos())
				}
				info.calls[name] = append(info.calls[name], call.Pos())
				return true
			})
			funcs[d.Name.Name] = info
		}
	}
	// entryGated: every non-test call site of the function is itself preceded by the gate, or sits in a function
	// whose own entry is gated. A function nothing calls is not gated.
	var entryGated func(name string, seen map[string]bool) bool
	entryGated = func(name string, seen map[string]bool) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		defer delete(seen, name)
		callers := 0
		for cname, caller := range funcs {
			for _, at := range caller.calls[name] {
				callers++
				if !(caller.gate != 0 && caller.gate < at) && !entryGated(cname, seen) {
					return false
				}
			}
		}
		return callers > 0
	}
	gated := func(name string) bool {
		f := funcs[name]
		for _, sender := range f.senders {
			if !(f.gate != 0 && f.gate < sender) && !entryGated(name, map[string]bool{}) {
				return false
			}
		}
		return true
	}
	checked := 0
	for name, f := range funcs {
		if len(f.senders) == 0 {
			continue
		}
		checked++
		if _, ok := readOnlyStartPaths[name]; ok {
			continue
		}
		if !gated(name) {
			t.Errorf("%s (%s) calls a start sender without the image gate before it, and not every caller is gated", name, f.file)
		}
	}
	if checked < 6 {
		t.Fatalf("only %d functions call a start sender; the sender list is stale", checked)
	}
}
