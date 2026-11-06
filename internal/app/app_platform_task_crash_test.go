package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

const (
	taskCrashChildEnv  = "RP_PLATFORM_TASK_CRASH_CHILD"
	taskCrashDBEnv     = "RP_PLATFORM_TASK_CRASH_DB"
	taskCrashModeEnv   = "RP_PLATFORM_TASK_CRASH_MODE"
	taskCrashEffectEnv = "RP_PLATFORM_TASK_CRASH_EFFECT"
	taskCrashStartEnv  = "RP_PLATFORM_TASK_CRASH_STARTED"
)

func TestPlatformTaskWorkerCrashChild(t *testing.T) {
	if os.Getenv(taskCrashChildEnv) != "1" {
		t.Skip("crash child runs only under the parent test")
	}
	dbPath := os.Getenv(taskCrashDBEnv)
	mode := os.Getenv(taskCrashModeEnv)
	effectPath := os.Getenv(taskCrashEffectEnv)
	startedPath := os.Getenv(taskCrashStartEnv)
	if dbPath == "" || effectPath == "" || startedPath == "" ||
		(mode != "before_effect" && mode != "after_effect") {
		t.Fatal("invalid crash-child environment")
	}
	store, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{
		st: store, cfg: &config.Config{SecretKey: rotOldKey},
	}
	server.platformTaskBackend = func(context.Context, platformTaskLease, string) appplatform.TaskExecutionResult {
		if err := os.WriteFile(startedPath, []byte("handler-entered"), 0600); err != nil {
			return appplatform.TaskExecutionResult{
				Outcome: appplatform.TaskResultFailed, ErrorCode: "fixture_start_write_failed",
			}
		}
		if mode == "after_effect" {
			file, err := os.OpenFile(effectPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
			if err != nil {
				return appplatform.TaskExecutionResult{
					Outcome: appplatform.TaskResultFailed, ErrorCode: "fixture_effect_write_failed",
				}
			}
			if _, err = file.WriteString("committed\n"); err != nil {
				file.Close()
				return appplatform.TaskExecutionResult{
					Outcome: appplatform.TaskResultFailed, ErrorCode: "fixture_effect_write_failed",
				}
			}
			if err = file.Sync(); err != nil {
				file.Close()
				return appplatform.TaskExecutionResult{
					Outcome: appplatform.TaskResultFailed, ErrorCode: "fixture_effect_sync_failed",
				}
			}
			file.Close()
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	scheduler, err := newPlatformTaskScheduler(
		server, platformFixtureID(7101), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scheduler.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash-child handler returned without dying")
}

func TestPlatformTaskWorkerCrashLeaseRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics differ on Windows")
	}
	for _, mode := range []string{"before_effect", "after_effect"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			dbPath := filepath.Join(directory, "portal.db")
			effectPath := filepath.Join(directory, "effect.log")
			startedPath := filepath.Join(directory, "started.marker")
			store, err := OpenStore("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			server, installation := platformServiceIdentityFixtureWithStore(t, store)
			in := admitTaskFixture(t, server, installation, "crash:"+mode,
				appplatform.TaskHandlerBackend, []byte(`{"fixture":"crash"}`))

			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(
				executable, "-test.run=^TestPlatformTaskWorkerCrashChild$")
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			command.Env = append(os.Environ(),
				taskCrashChildEnv+"=1",
				taskCrashDBEnv+"="+dbPath,
				taskCrashModeEnv+"="+mode,
				taskCrashEffectEnv+"="+effectPath,
				taskCrashStartEnv+"="+startedPath,
			)
			err = command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child did not die by SIGKILL: %v\n%s", err, output.String())
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("child exit was not SIGKILL: %v\n%s", err, output.String())
			}
			if started, err := os.ReadFile(startedPath); err != nil || string(started) != "handler-entered" {
				t.Fatalf("handler entry marker: %q %v", started, err)
			}
			effects := readCrashEffects(t, effectPath)
			wantEffects := 0
			if mode == "after_effect" {
				wantEffects = 1
			}
			if len(effects) != wantEffects {
				t.Fatalf("effects before recovery = %v", effects)
			}
			if got := scalar[string](t, store, `SELECT state FROM app_platform_tasks`); got != "running" {
				t.Fatalf("killed worker task state = %q", got)
			}
			if got := scalar[string](t, store, `SELECT state FROM app_platform_task_attempts`); got != "running" {
				t.Fatalf("killed worker attempt state = %q", got)
			}

			// The test moves only disposable timestamps into the past; it does
			// not alter the task identity, effect log or handler payload.
			mustExec(t, store, `UPDATE app_platform_tasks
				SET created_ms=1,updated_ms=1,lease_until_ms=60000`)
			mustExec(t, store, `UPDATE app_platform_task_attempts
				SET created_ms=1,started_ms=1,lease_until_ms=60000,updated_ms=1`)
			next, err := server.claimNextPlatformTask(
				context.Background(), platformFixtureID(7102))
			if err != nil || next.ID != "" {
				t.Fatalf("expired crashed lease was reclaimed: %+v %v", next, err)
			}
			if got := scalar[string](t, store, `SELECT state||':'||error_code FROM app_platform_tasks`); got != "uncertain:task_lease_expired" {
				t.Fatalf("recovered task state = %q", got)
			}
			if got := scalar[string](t, store, `SELECT state||':'||error_code FROM app_platform_task_attempts`); got != "uncertain:task_lease_expired" {
				t.Fatalf("recovered attempt state = %q", got)
			}
			if scalar[int](t, store, `SELECT COUNT(*) FROM app_platform_tasks WHERE result_json IS NOT NULL OR completed_ms IS NOT NULL OR worker_id IS NOT NULL OR lease_until_ms IS NOT NULL`) != 0 {
				t.Fatal("recovered crash released a result or retained a worker lease")
			}
			replayed, err := server.claimNextPlatformTask(
				context.Background(), platformFixtureID(7103))
			if err != nil || replayed.ID != "" || len(readCrashEffects(t, effectPath)) != wantEffects {
				t.Fatal("recovered crash was automatically replayed")
			}
			again, created, err := server.admitPlatformTask(
				context.Background(), installation, in)
			if err != nil || created || !platformValidUUID(again) ||
				scalar[int](t, store, `SELECT COUNT(*) FROM app_platform_tasks`) != 1 {
				t.Fatalf("stable occurrence identity changed after crash: id=%q created=%v err=%v", again, created, err)
			}
		})
	}
}

func readCrashEffects(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}
