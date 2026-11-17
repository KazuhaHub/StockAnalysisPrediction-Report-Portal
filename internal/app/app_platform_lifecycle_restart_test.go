package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformLifecycleRestartCase struct {
	action appplatform.Action
	stage  appplatform.Stage
	kind   string
}

var platformLifecycleRestartCases = []platformLifecycleRestartCase{
	{appplatform.ActionInstall, appplatform.StageQueued, ""},
	{appplatform.ActionInstall, appplatform.StageValidate, ""},
	{appplatform.ActionInstall, appplatform.StageDownload, ""},
	{appplatform.ActionInstall, appplatform.StageStage, "stage"},
	{appplatform.ActionInstall, appplatform.StagePrepareData, "prepare-data"},
	{appplatform.ActionInstall, appplatform.StageStart, "start-candidate"},
	{appplatform.ActionInstall, appplatform.StageProbe, "probe"},
	{appplatform.ActionInstall, appplatform.StageActivate, "activate"},
	{appplatform.ActionUpdate, appplatform.StageQuiesce, ""},
	{appplatform.ActionUpdate, appplatform.StageSnapshot, "snapshot"},
	{appplatform.ActionUpdate, appplatform.StageMigrate, ""},
	{appplatform.ActionDisable, appplatform.StageRevoke, ""},
	{appplatform.ActionDisable, appplatform.StageStop, "stop"},
	{appplatform.ActionUninstall, appplatform.StageRemoveCode, "remove-code"},
	{appplatform.ActionUninstall, appplatform.StageRemoveData, "delete-data"},
	{appplatform.ActionRestore, appplatform.StageRestoreData, "restore"},
}

func platformLifecycleOperationAtStage(t *testing.T, action appplatform.Action, stage appplatform.Stage) appplatform.Operation {
	t.Helper()
	generation := uint64(2)
	if action == appplatform.ActionInstall {
		generation = 1
	}
	operation, err := appplatform.NewOperation(action, true, generation)
	if err != nil {
		t.Fatal(err)
	}
	nextStages := []appplatform.Stage{
		appplatform.StageValidate, appplatform.StageDownload,
		appplatform.StageStage, appplatform.StagePrepareData,
		appplatform.StageQuiesce, appplatform.StageSnapshot,
		appplatform.StageMigrate, appplatform.StageStart,
		appplatform.StageProbe, appplatform.StageActivate,
		appplatform.StageRevoke, appplatform.StageStop,
		appplatform.StageRemoveCode, appplatform.StageRemoveData,
		appplatform.StageRestoreData, appplatform.StageComplete,
	}
	for operation.Snapshot().Stage != stage {
		advanced := false
		for _, next := range nextStages {
			candidate, advanceErr := operation.Advance(
				operation.Snapshot().Revision, next)
			if advanceErr == nil {
				operation = candidate
				advanced = true
				break
			}
		}
		if !advanced {
			t.Fatalf("operation %s never reaches stage %s",
				action, stage)
		}
	}
	return operation
}

func platformLifecycleRestartSeed(
	t *testing.T,
	st *Store,
	tc platformLifecycleRestartCase,
) (platformOperationLease, platformEffectIntent) {
	t.Helper()
	operation := platformLifecycleOperationAtStage(t, tc.action, tc.stage)
	snapshot := operation.Snapshot()
	sourceGeneration := int64(1)
	if tc.action == appplatform.ActionInstall {
		sourceGeneration = 0
	}
	mustExec(t, st, `UPDATE app_platform_installations
		SET runtime_id=?,generation=?,desired_state='enabled',phase='pending',
		revision=1,control_fence=0,controller_epoch=?
		WHERE id=?`, platformFixtureID(5), snapshot.Generation,
		platformFixtureID(99), platformFixtureID(1))
	mustExec(t, st, `UPDATE app_platform_operations
		SET action=?,managed=1,source_generation=?,target_generation=?,
		stage=?,state_revision=?,row_revision=1,fence=0,
		lease_owner=NULL,lease_until_ms=NULL,error_code=NULL,
		completed_ms=NULL
		WHERE id=?`, string(tc.action), sourceGeneration,
		int64(snapshot.Generation), string(tc.stage),
		int64(snapshot.Revision), platformFixtureID(3))
	first, err := st.claimPlatformOperation(
		platformFixtureID(3), platformFixtureID(20))
	if err != nil {
		t.Fatalf("%s/%s initial claim: %v", tc.action, tc.stage, err)
	}
	if first.snapshot != snapshot || first.fence != 1 {
		t.Fatalf("%s/%s initial lease = %+v", tc.action, tc.stage, first)
	}
	intent := platformEffectIntent{
		number: 1, kind: tc.kind,
		payloadSHA: platformDigest([]byte(
			string(tc.action) + "/" + string(tc.stage))),
	}
	if tc.kind != "" {
		effect, effectErr := st.preparePlatformEffect(first, intent)
		if effectErr != nil || !effect.created || effect.state != "prepared" {
			t.Fatalf("%s/%s prepare effect: %+v %v",
				tc.action, tc.stage, effect, effectErr)
		}
		mustExec(t, st, `UPDATE app_platform_operation_effects
			SET state='unknown' WHERE operation_id=? AND stage=?`,
			platformFixtureID(3), string(tc.stage))
	}
	return first, intent
}

func platformLifecycleRestartSeedControl(t *testing.T, st *Store) {
	t.Helper()
	platformSeedPendingControl(t, st)
	for key, value := range map[string]string{
		"app_platform_controller_epoch": platformFixtureID(99),
		"app_platform_restore_paused":   "0",
	} {
		if err := st.poolExec().setSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
}

func platformLifecycleRestartTakeover(
	t *testing.T,
	tc platformLifecycleRestartCase,
	first platformOperationLease,
	intent platformEffectIntent,
	reopened *Store,
) {
	t.Helper()
	if _, err := reopened.claimPlatformOperation(
		platformFixtureID(3), platformFixtureID(21),
	); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("%s/%s live lease was stolen: %v",
			tc.action, tc.stage, err)
	}
	mustExec(t, reopened, `UPDATE app_platform_operations
		SET lease_until_ms=0 WHERE id=?`, platformFixtureID(3))
	second, err := reopened.claimPlatformOperation(
		platformFixtureID(3), platformFixtureID(21))
	if err != nil {
		t.Fatalf("%s/%s takeover: %v", tc.action, tc.stage, err)
	}
	if second.snapshot != first.snapshot ||
		second.fence != first.fence+1 ||
		second.owner != platformFixtureID(21) {
		t.Fatalf("%s/%s takeover lease = %+v",
			tc.action, tc.stage, second)
	}
	if _, err := reopened.heartbeatPlatformOperation(first); !errors.Is(
		err, errAppPlatformLeaseConflict,
	) {
		t.Fatalf("%s/%s stale owner renewed: %v",
			tc.action, tc.stage, err)
	}
	if tc.kind != "" {
		if _, err := reopened.preparePlatformEffect(
			first, intent,
		); !errors.Is(err, errAppPlatformLeaseConflict) {
			t.Fatalf("%s/%s stale owner committed an effect: %v",
				tc.action, tc.stage, err)
		}
		reconciled, err := reopened.preparePlatformEffect(
			second, intent)
		if err != nil || reconciled.created ||
			reconciled.state != "unknown" ||
			reconciled.admittedFence != first.fence {
			t.Fatalf("%s/%s changed ambiguous effect: %+v %v",
				tc.action, tc.stage, reconciled, err)
		}
		if scalar[int](t, reopened, `SELECT COUNT(*)
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage=?`,
			platformFixtureID(3), string(tc.stage)) != 1 {
			t.Fatalf("%s/%s duplicated an ambiguous effect",
				tc.action, tc.stage)
		}
	}
	if renewed, err := reopened.heartbeatPlatformOperation(second); err != nil ||
		renewed.owner != second.owner || renewed.fence != second.fence {
		t.Fatalf("%s/%s replacement cannot renew: %+v %v",
			tc.action, tc.stage, renewed, err)
	}
	if scalar[string](t, reopened, `SELECT stage
		FROM app_platform_operations WHERE id=?`,
		platformFixtureID(3)) != string(tc.stage) {
		t.Fatalf("%s/%s takeover changed durable stage",
			tc.action, tc.stage)
	}
}

func TestPlatformLifecycleRestartMatrixSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle-restart.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			_ = st.Close()
		}
	})
	platformLifecycleRestartSeedControl(t, st)
	for _, tc := range platformLifecycleRestartCases {
		t.Run(string(tc.action)+"/"+string(tc.stage), func(t *testing.T) {
			first, intent := platformLifecycleRestartSeed(t, st, tc)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st = nil
			reopened, openErr := OpenStore("sqlite", path)
			if openErr != nil {
				t.Fatal(openErr)
			}
			st = reopened
			platformLifecycleRestartTakeover(
				t, tc, first, intent, reopened)
		})
	}
}

func TestPlatformLifecycleRestartMatrixPostgres(t *testing.T) {
	st := platformTestStore(t, true)
	platformLifecycleRestartSeedControl(t, st)
	for _, tc := range platformLifecycleRestartCases {
		t.Run(string(tc.action)+"/"+string(tc.stage), func(t *testing.T) {
			first, intent := platformLifecycleRestartSeed(t, st, tc)
			reopened, err := OpenStore("postgres", pgDSN(t))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			platformLifecycleRestartTakeover(
				t, tc, first, intent, reopened)
		})
	}
}

func TestPlatformLifecycleRestartMatrixCoverage(t *testing.T) {
	cases := make(map[string]bool)
	for _, tc := range platformLifecycleRestartCases {
		cases[string(tc.stage)] = true
	}
	for _, stage := range []appplatform.Stage{
		appplatform.StageQueued, appplatform.StageValidate,
		appplatform.StageDownload, appplatform.StageStage,
		appplatform.StagePrepareData, appplatform.StageQuiesce,
		appplatform.StageSnapshot, appplatform.StageMigrate,
		appplatform.StageStart, appplatform.StageProbe,
		appplatform.StageActivate, appplatform.StageRevoke,
		appplatform.StageStop, appplatform.StageRemoveCode,
		appplatform.StageRemoveData, appplatform.StageRestoreData,
	} {
		if !cases[string(stage)] {
			t.Fatalf("restart matrix omits durable stage %s", stage)
		}
	}
	for _, terminal := range []appplatform.Stage{
		appplatform.StageComplete, appplatform.StageFailed,
		appplatform.StageRecoveryRequired,
	} {
		if cases[string(terminal)] {
			t.Fatalf("restart matrix treats terminal stage %s as work",
				terminal)
		}
	}
}
