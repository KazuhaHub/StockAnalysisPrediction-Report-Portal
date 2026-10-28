//go:build linux && (amd64 || arm64)

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"golang.org/x/sys/unix"
)

func TestNativeJournalDataWriterExecution(t *testing.T) {
	image := os.Getenv("APP_WRITER_NATIVE_IMAGE")
	if image == "" {
		t.Skip("explicit disposable writer journal/XFS/Docker fixture only")
	}
	rootPath, journalPath, socket := os.Getenv("APP_DATA_JOURNAL_ROOT"), os.Getenv("APP_DATA_JOURNAL_CONTROL"), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET")
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || !filepath.IsAbs(rootPath) || !filepath.IsAbs(journalPath) || !filepath.IsAbs(socket) {
		t.Fatal("writer requires disposable root and explicit paths")
	}
	inode, err := strconv.ParseUint(os.Getenv("APP_DATA_JOURNAL_INODE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: inode}
	input, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	root, err := data.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	sockets, err := os.MkdirTemp("/run", "portal-wexec-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	native, err := driver.OpenCandidateRuntime(context.Background(), socket, rootPath, sockets, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { native.Close() }()
	raw, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("writer descriptor")
	}
	backend := manifest["backend"].(map[string]any)
	backend["image"], backend["platforms"] = image, []string{"linux/" + runtime.GOARCH}
	manifest["capabilities"] = map[string]any{"required": []string{}, "optional": []string{}}
	descriptor, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		base := 1500 + i*20
		c := runnerCommandFixture(t)
		c.InstallationID, c.OperationID, c.EffectID, c.Payload.DataNamespaceID = fixtureID(base), fixtureID(base+1), fixtureID(base+2), fixtureID(base+3)
		c.Payload.ReleaseSHA256, c.Payload.QuotaBytes, c.Payload.QuotaInodes = parsed.SHA256, "8388608", "128"
		sourceRaw, sourceSignature := signRunnerCommand(t, c)
		source, err := s.DispatchPrepareData(ctx, sourceRaw, sourceSignature, root, pin)
		if err != nil {
			t.Fatal("actual writer source preparation", err)
		}
		writer := writerCommandFixture(t, sourceRaw, sourceSignature, source)
		writer.EffectID, writer.ResourceID, writer.Platform = fixtureID(base+4), fixtureID(base+5), "linux/"+runtime.GOARCH
		if i == 1 {
			shortened := c
			shortened.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+2500, 10)
			writer.DataRaw, writer.DataSignature = signRunnerCommand(t, shortened)
		}
		writerRaw, writerSig := signWriterFixture(t, writer)
		if _, err := s.AdmitDataWriterCommand(ctx, writerRaw, writerSig); err != nil {
			t.Fatal(err)
		}
		claim, err := s.ClaimDataWriter(ctx, writerRaw, writerSig)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if _, err := s.db.Exec(`CREATE TRIGGER fixture_writer_settlement_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + writer.EffectID + `' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture writer receipt commit failure'); END`); err != nil {
				t.Fatal(err)
			}
		}
		receipt, err := s.ExecuteDataWriter(ctx, claim, descriptor, native)
		project, _ := strconv.ParseUint(source.ProjectID, 10, 32)
		physical, inspectErr := root.InspectWriteAccess(ctx, pin, c.Payload.DataNamespaceID, uint32(project))
		if inspectErr != nil || strconv.FormatUint(physical.Namespace.Inode, 10) != source.NamespaceInode {
			t.Fatal("actual writer permission effect missing", inspectErr)
		}
		if i == 0 {
			if err != nil || !validDataWriterReceipt(receipt) || receipt.Native.NamespaceInode != source.NamespaceInode || receipt.Claim.ClaimID == "" {
				t.Fatal("native writer settlement", err)
			}
			if got, err := s.ReadDataWriterReceipt(ctx, writerRaw, writerSig); err != nil || got != receipt {
				t.Fatal("native writer receipt read", err)
			}
			var ref control.Command
			json.Unmarshal(writer.DataRaw, &ref)
			now := time.Now().UnixMilli()
			ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
			writer.DataRaw, writer.DataSignature = signRunnerCommand(t, ref)
			renewed, renewedSig := signWriterFixture(t, writer)
			if got, err := s.ReadDataWriterReceipt(ctx, renewed, renewedSig); err != nil || got != receipt {
				t.Fatal("native receipt changed on renewal", err)
			}
			if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE id='`+c.Payload.DataNamespaceID+`'`) != 3 {
				t.Fatal("writer volume completion not atomic")
			}
			if _, err := s.ReadDataPreparationReceipt(ctx, writer.DataRaw, writer.DataSignature); err == nil {
				t.Fatal("write-enabled namespace adopted as sealed")
			}
		} else {
			if _, dropErr := s.db.Exec(`DROP TRIGGER fixture_writer_settlement_failure`); dropErr != nil {
				t.Fatal(dropErr)
			}
			if !errors.Is(err, driver.ErrDataWriterUncertain) || receipt != (DataWriterReceipt{}) {
				t.Fatal("failed settlement returned physical authority", err)
			}
			if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE id='`+c.Payload.DataNamespaceID+`'`) != 2 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+writer.EffectID+`' AND state='unknown' AND ack_attempted_ms IS NULL`) != 1 {
				t.Fatal("failed settlement committed a partial resource/receipt")
			}
			if _, err := s.ReadDataWriterReceipt(ctx, writerRaw, writerSig); err == nil {
				t.Fatal("unknown writer reported applied")
			}

			if _, err := s.ReconcileDataWriter(ctx, writerRaw, writerSig, descriptor, native); err == nil {
				t.Fatal("unexpired execution reference authorized recovery")
			}
			// Restart the journal and runtime after the ambiguous physical effect. The
			// old live handle must not be reconstructed or used by this recovery.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := native.Close(); err != nil {
				t.Fatal(err)
			}
			native, err = driver.OpenCandidateRuntime(ctx, socket, rootPath, sockets, root, pin)
			if err != nil {
				t.Fatal(err)
			}
			var marker dataWriterClaimMarker
			json.Unmarshal([]byte(claim.state.marker), &marker)
			oldExpiry, _ := strconv.ParseInt(marker.ExpiresMS, 10, 64)
			if remaining := time.Until(time.UnixMilli(oldExpiry + 1)); remaining > 0 {
				time.Sleep(remaining)
			}
			var ref control.Command
			json.Unmarshal(writer.DataRaw, &ref)
			now := time.Now().UnixMilli()
			ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
			writer.DataRaw, writer.DataSignature = signRunnerCommand(t, ref)
			writerRaw, writerSig = signWriterFixture(t, writer)
			path := filepath.Join(rootPath, c.Payload.DataNamespaceID)
			var before, after unix.Stat_t
			if err := unix.Stat(path, &before); err != nil {
				t.Fatal(err)
			}

			if _, err := s.db.Exec(`CREATE TRIGGER fixture_writer_recovery_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + writer.EffectID + `' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture recovery commit failure'); END`); err != nil {
				t.Fatal(err)
			}
			if result, err := s.ReconcileDataWriter(ctx, writerRaw, writerSig, descriptor, native); err == nil || result != (DataWriterReceipt{}) {
				t.Fatal("failed recovery commit returned evidence")
			}
			if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE id='`+c.Payload.DataNamespaceID+`'`) != 2 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+writer.EffectID+`' AND state='unknown'`) != 1 {
				t.Fatal("failed recovery committed half its state")
			}
			if _, err := s.db.Exec(`DROP TRIGGER fixture_writer_recovery_failure`); err != nil {
				t.Fatal(err)
			}
			recovered, err := s.ReconcileDataWriter(ctx, writerRaw, writerSig, descriptor, native)
			if err != nil || recovered.Format != dataWriterRecoveredReceiptFormat || recovered.Claim != marker || recovered.RecoveryEnvelopeSHA256 != candidateHash(writerRaw) {
				t.Fatal("actual observation-only recovery", err)
			}
			if err := unix.Stat(path, &after); err != nil {
				t.Fatal(err)
			}
			if before.Ino != after.Ino || before.Mode != after.Mode || before.Gid != after.Gid || before.Ctim != after.Ctim {
				t.Fatal("recovery repeated native permission mutation")
			}
			if got, err := s.ReadDataWriterReceipt(ctx, writerRaw, writerSig); err != nil || got != recovered {
				t.Fatal("actual recovered receipt unavailable", err)
			}
			if _, err := s.ReconcileDataWriter(ctx, writerRaw, writerSig, descriptor, native); err == nil {
				t.Fatal("applied recovery reexecuted")
			}
			if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE id='`+c.Payload.DataNamespaceID+`'`) != 3 {
				t.Fatal("actual recovered volume not settled")
			}
		}
		if again, err := s.ExecuteDataWriter(ctx, claim, descriptor, native); err == nil || again != (DataWriterReceipt{}) {
			t.Fatal("physical writer effect replayed")
		}
		if _, err := s.ClaimDataWriter(ctx, writerRaw, writerSig); err == nil {
			t.Fatal("completed/unknown physical writer reclaimed")
		}
		if _, err := root.Inspect(ctx, pin, c.Payload.DataNamespaceID, uint32(project)); err == nil {
			t.Fatal("physical writable namespace adopted as sealed")
		}
	}
	t.Log("NATIVE_JOURNAL_DATA_WRITER_EXECUTION_VERIFIED")
	t.Log("NATIVE_WRITER_SETTLEMENT_FAILURE_PRESERVED_UNKNOWN")
	t.Log("NATIVE_DATA_WRITER_RECOVERY_AFTER_EXPIRY_VERIFIED")
	t.Log("NATIVE_WRITER_RECOVERY_WITHOUT_PERMISSION_MUTATION")
	t.Log("NATIVE_WRITER_RECOVERY_METADATA_FAILURE_RETRY_VERIFIED")
}
