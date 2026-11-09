package app

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func imageRow(t *testing.T, s *Server, operation string) (id, kind, state, receipt string, found bool) {
	t.Helper()
	rows, err := s.st.db.Query(s.st.bind(`SELECT effect_id,kind,state,receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='stage' AND effect_number=1`), operation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&id, &kind, &state, &receipt); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	return
}

// Fails if the load leaves no Portal row, uses another kind or stage, or keeps asking the runner once applied.
func TestEnsureImageWritesALoadImageRowThatEndsAppliedWithTheReceiptAndIsNotSentAgain(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatal(err)
	}
	id, kind, state, receipt, found := imageRow(t, s, lease.id)
	if !found || kind != "load_image" || state != "applied" || id != platformLoadImageEffectID(lease.id, platformDigest(parts.descriptor)) {
		t.Fatalf("row %v %s %s %s", found, kind, state, id)
	}
	if r, err := journal.ParseLoadImageReceipt([]byte(receipt)); err != nil || r.EffectID != id {
		t.Fatalf("the row does not hold the runner's receipt: %v", err)
	}
	calls := len(fake.calls)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil || len(fake.calls) != calls {
		t.Fatalf("an applied load was asked for again: %v %v", err, fake.calls[calls:])
	}
}

// Fails if a load with no verdict leaves the row prepared or applied, which would hide it from the settlement
// driver or claim a load that was never seen.
func TestEnsureImageLeavesTheRowUnknownWhenTheOutcomeIsNotKnown(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.loseExecuteReplyOnce, fake.readFails = true, true
	err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint)
	if !errors.Is(err, errPlatformImageUncertain) {
		t.Fatal(err)
	}
	if _, _, state, _, found := imageRow(t, s, lease.id); !found || state != "unknown" {
		t.Fatalf("row state %q", state)
	}
}

// Fails if a rejection is not recorded, or is asked of the runner again.
func TestEnsureImageRecordsARejectionAsRevertedAndRefusesWithoutAskingAgain(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.rejectLoad = "inspect_mismatch"
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); !errors.Is(err, errPlatformImageRejected) {
		t.Fatal(err)
	}
	if _, _, state, _, _ := imageRow(t, s, lease.id); state != "reverted" {
		t.Fatalf("row state %q", state)
	}
	calls := len(fake.calls)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); !errors.Is(err, errPlatformImageRejected) || len(fake.calls) != calls {
		t.Fatalf("a recorded rejection was sent again: %v", err)
	}
}

// tryAdmit admits an operation row through the one helper that writes the operations table.
func tryAdmit(t *testing.T, s *Server, installation, action string) error {
	t.Helper()
	return insertPlatformOperation(s.st.poolExec(), platformOperationRow{
		ID: platformFixtureID(8801), InstallationID: installation, ReviewPlanID: platformFixtureID(8802), ActorScope: platformDigest([]byte("a")),
		IdempotencySHA: platformDigest([]byte("i")), RequestSHA: platformDigest([]byte("r")), Action: action, Managed: true,
		SourceGeneration: 1, TargetGeneration: 2, Stage: "queued", StateRevision: 1, ControllerEpoch: platformFixtureID(8803), NowMS: 1})
}

// giveUp makes the gate end with an unknown load and the operation finish for review.
func giveUp(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, fake *platformFakeImageRunner) {
	t.Helper()
	fake.loseExecuteReplyOnce, fake.readFails = true, true
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err == nil {
		t.Fatal("the gate did not give up")
	}
	if err := s.finishPlatformUpdateForReview(context.Background(), lease); err != nil {
		t.Fatal("the operation did not end for review:", err)
	}
	fake.readFails = false
}

// The unknown load of an operation the gate gave up on: no operation is admitted while it has no verdict, a
// later driver tick settles it under the same id, and the installation is then free. Fails if the unknown load
// is invisible, if the driver mints a second identity, or if the installation stays blocked afterwards.
func TestAnUnknownLoadBlocksEveryAdmissionUntilTheDriverSettlesItUnderTheSameId(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	giveUp(t, s, lease, endpoint, fake)
	if stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id); stage != "recovery_required" {
		t.Fatalf("operation stage %q", stage)
	}
	if err := tryAdmit(t, s, lease.installationID, "update"); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("an operation was admitted while a load had no verdict:", err)
	}
	n, err := s.settlePlatformLoadImages(context.Background(), endpoint)
	if err != nil || n != 1 {
		t.Fatalf("driver tick: %d %v", n, err)
	}
	if _, _, state, _, _ := imageRow(t, s, lease.id); state != "applied" {
		t.Fatalf("row state %q after the driver", state)
	}
	if len(fake.effectIDs) != 1 || !fake.effectIDs[platformLoadImageEffectID(lease.id, platformDigest(parts.descriptor))] {
		t.Fatalf("the driver used another identity: %v", fake.effectIDs)
	}
	if err := tryAdmit(t, s, lease.installationID, "update"); errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("the installation is still blocked after the load settled")
	}
	if n, _ := s.settlePlatformLoadImages(context.Background(), endpoint); n != 0 {
		t.Fatal("a settled load was settled again")
	}
}

// Fails if any operation kind can be admitted while the installation holds an unsettled load.
func TestEveryOperationKindIsRefusedWhileALoadIsUnsettled(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	giveUp(t, s, lease, endpoint, fake)
	for _, action := range []string{"install", "update", "enable", "disable", "repair", "uninstall", "delete_data", "restore", "restore_import"} {
		if err := tryAdmit(t, s, lease.installationID, action); !errors.Is(err, errAppPlatformOperationConflict) {
			t.Errorf("%s was not refused: %v", action, err)
		}
	}
	// Another installation is not affected.
	if err := tryAdmit(t, s, platformFixtureID(8899), "update"); errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("an unrelated installation was refused")
	}
}

// Fails if any non-test code can write the operations table other than the one helper: every string literal and
// constant is normalised (lower case, whitespace collapsed, adjacent literals joined) and none outside the
// helper's file may hold an insert into the table, an insert or ignore into it, or a rewrite of an operation's
// installation.
func TestNoCodeWritesTheOperationsTableOutsideTheInsertHelper(t *testing.T) {
	forbidden := regexp.MustCompile(`insert (or \w+ )?into app_platform_operations\b|replace into app_platform_operations\b|update app_platform_operations set installation_id`)
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	checked := 0
	var fold func(e ast.Expr) (string, bool)
	fold = func(e ast.Expr) (string, bool) {
		switch v := e.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				u, err := strconv.Unquote(v.Value)
				return u, err == nil
			}
		case *ast.BinaryExpr:
			if v.Op == token.ADD {
				l, lok := fold(v.X)
				r, rok := fold(v.Y)
				return l + r, lok && rok
			}
		case *ast.ParenExpr:
			return fold(v.X)
		}
		return "", false
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "app_platform_operation_insert.go" {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		check := func(text string, pos token.Pos) {
			checked++
			norm := strings.Join(strings.Fields(strings.ToLower(text)), " ")
			if forbidden.MatchString(norm) {
				t.Errorf("%s writes the operations table outside the insert helper: %q", fset.Position(pos), norm)
			}
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BinaryExpr:
				if text, ok := fold(v); ok {
					check(text, v.Pos())
					return false
				}
			case *ast.BasicLit:
				if text, ok := fold(v); ok {
					check(text, v.Pos())
				}
			}
			return true
		})
	}
	if checked < 1000 {
		t.Fatalf("only %d literals were checked; the walk is broken", checked)
	}
}

func blobFiles(t *testing.T, s *Server) []string {
	t.Helper()
	store, _ := s.platformImages()
	var names []string
	for _, e := range store.list(store.blobs) {
		names = append(names, e.name)
	}
	return names
}

// Fails if the target of an operation holding an unsettled load loses its pin when the operation completes, so
// that a sweep removes the blobs settlement needs.
func TestAnUnsettledLoadKeepsItsReleaseBlobsPinnedAcrossASweepAndTheNextTickSettlesIt(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	giveUp(t, s, lease, endpoint, fake)
	store, _ := s.platformImages()
	store.grace = 0
	before := len(blobFiles(t, s))
	s.sweepPlatformImages(context.Background())
	if after := len(blobFiles(t, s)); after != before || before != 4 {
		t.Fatalf("a sweep removed the blobs of an unsettled load: %d -> %d", before, after)
	}
	if n, err := s.settlePlatformLoadImages(context.Background(), endpoint); err != nil || n != 1 {
		t.Fatalf("settlement after the sweep: %d %v", n, err)
	}
	// Settled and no longer pinned by the operation (it is recovery_required, which still pins): both hold.
	if !fake.present {
		t.Fatal("the image was not loaded")
	}
}

// The read-back needs no blobs. Fails if settlement hashes the blobs, or fails for want of them, when the runner
// already has the verdict.
func TestSettlementAppliesThroughTheReadBackWhenTheBlobsAreGoneButTheImageIsOnTheHost(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.present = true
	giveUp(t, s, lease, endpoint, fake)
	store, _ := s.platformImages()
	for _, name := range blobFiles(t, s) {
		os.Remove(filepath.Join(store.blobs, name))
	}
	loads := fake.count("load")
	n, err := s.settlePlatformLoadImages(context.Background(), endpoint)
	if err != nil || n != 1 {
		t.Fatalf("settlement without blobs: %d %v", n, err)
	}
	if _, _, state, _, _ := imageRow(t, s, lease.id); state != "applied" {
		t.Fatalf("row state %q", state)
	}
	if fake.count("load") != loads || fake.count("blob:") != 0 {
		t.Fatalf("settlement sent more than a read-back: %v", fake.calls)
	}
}

// Fails if a load that truly needs gone blobs loops silently: the settings status must name the release and say
// the package has to be uploaded again, for a missing blob and for an unreadable manifest alike.
func TestSettlementThatNeedsGoneBlobsIsSurfacedAsAPackageReuploadRequirement(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	giveUp(t, s, lease, endpoint, fake)
	store, _ := s.platformImages()
	layer := parts.plan.Layers[0]
	os.Remove(store.blobPath(layer.Digest))
	if n, _ := s.settlePlatformLoadImages(context.Background(), endpoint); n != 0 {
		t.Fatal("a load settled without its blobs")
	}
	if _, _, state, _, _ := imageRow(t, s, lease.id); state != "unknown" {
		t.Fatalf("row state %q", state)
	}
	target := platformDigest(parts.descriptor)
	got, err := s.platformImageDeliveryProjection(s.st.poolExec(), lease.installationID, "")
	if err != nil || got == nil || got.State != "reupload_required" || got.ReleaseSHA256 != target || got.Reason != "blobs_missing" {
		t.Fatalf("status for a missing blob: %+v %v", got, err)
	}
	os.Remove(store.blobPath(parts.entry.ManifestDigest))
	got, err = s.platformImageDeliveryProjection(s.st.poolExec(), lease.installationID, "")
	if err != nil || got == nil || got.Reason != "manifest_unreadable" || got.ReleaseSHA256 != target {
		t.Fatalf("status for an unreadable manifest: %+v %v", got, err)
	}
	// The same fact stops the sweep, and the stop is logged with the release and the installation.
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	store.grace = 0
	s.sweepPlatformImages(context.Background())
	if !strings.Contains(logged.String(), target) || !strings.Contains(logged.String(), lease.installationID) {
		t.Fatalf("the sweep stop did not name the release and the installation: %q", logged.String())
	}
}

// Nine finished operations hold an unknown load; one cannot be settled because its installation's fence moved.
// Fails if that one starves the other eight, or if it is not reported.
func TestAnUnsettleableLoadDoesNotStarveTheRestOfTheWindow(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	giveUp(t, s, lease, endpoint, fake)
	ids := []string{lease.id}
	for i := range 8 {
		op, plan, effect := platformFixtureID(8100+i), platformFixtureID(8200+i), platformFixtureID(8300+i)
		mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			SELECT ?,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
			FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, plan, lease.id)
		mustExec(t, s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
			SELECT ?,installation_id,?,actor_scope,?,?,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms
			FROM app_platform_operations WHERE id=?`, op, plan, platformDigest([]byte(op)), platformDigest([]byte(plan)), lease.id)
		mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			SELECT ?,stage,effect_number,?,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms
			FROM app_platform_operation_effects WHERE operation_id=? AND kind='load_image'`, op, effect, lease.id)
		// The row of a clone must carry the identity and digest its own command will: derive them the way the
		// gate does.
		cloneLease, err := platformLoadSettlementLease(s.st.poolExec(), op)
		if err != nil {
			t.Fatal(err)
		}
		target, err := s.loadPlatformImageDescriptorTarget(context.Background(), cloneLease)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := s.newPlatformImageSignerGuarded(context.Background(), cloneLease, func(migExec) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		call, err := signer.loadCall(target)
		signer.close()
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET effect_id=?,payload_sha256=? WHERE operation_id=? AND kind='load_image'`, call.command.EffectID, call.command.PayloadSHA256, op)
		ids = append(ids, op)
	}
	// The first (oldest) operation's fence has moved past the installation's: it can never be settled here.
	mustExec(t, s.st, `UPDATE app_platform_operations SET fence=fence+5,completed_ms=1 WHERE id=?`, lease.id)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	n, err := s.settlePlatformLoadImages(context.Background(), endpoint)
	if err != nil || n != 8 {
		t.Fatalf("settled %d of the eight that could settle: %v\n%s", n, err, logged.String())
	}
	if !strings.Contains(logged.String(), lease.id) {
		t.Fatal("the load that cannot settle was not reported")
	}
	if _, _, state, _, _ := imageRow(t, s, lease.id); state != "unknown" {
		t.Fatalf("the unsettleable load changed state: %q", state)
	}
}
