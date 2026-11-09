package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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

// Fails if a start path can be reached without the image gate, which is how a pruned or never-loaded image
// would reach a start. Each listed function must open with the ensure call.
func TestEveryStartPathEnsuresTheImageBeforeAnythingElse(t *testing.T) {
	want := map[string]string{
		"app_platform_update_start_stage.go":      "executePlatformUpdateStart",
		"app_platform_restore_start_stage.go":     "executePlatformRestoreStart",
		"app_platform_migration_stage.go":         "dispatchPlatformMigrateData",
		"app_platform_writable_install_worker.go": "startPlatformWritableInstall",
		"app_platform_candidate_stage.go":         "dispatchPlatformInstallCandidate",
	}
	for file, name := range want {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name {
				continue
			}
			found = true
			first, ok := fn.Body.List[0].(*ast.IfStmt)
			if !ok {
				t.Fatalf("%s does not open with the image gate", name)
			}
			init, ok := first.Init.(*ast.AssignStmt)
			if !ok || len(init.Rhs) != 1 {
				t.Fatalf("%s does not open with the image gate", name)
			}
			call, ok := init.Rhs[0].(*ast.CallExpr)
			if !ok || !strings.HasSuffix(exprString(call.Fun), "ensurePlatformReleaseImage") {
				t.Fatalf("%s does not open with the image gate", name)
			}
		}
		if !found {
			t.Fatalf("%s: %s is gone; update the start paths", file, name)
		}
	}
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.Ident:
		return v.Name
	}
	return ""
}
