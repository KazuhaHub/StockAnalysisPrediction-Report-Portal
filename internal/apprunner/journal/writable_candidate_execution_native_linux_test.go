//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func writableFixtureOwnedNative(t *testing.T, socket string, c control.CandidateCommand, m writableCandidateClaimMarker) string {
	t.Helper()
	raw := candidateFixtureDocker(t, socket, "container", "ls", "-a", "--no-trunc", "--filter", "label=org.portal.app.resource="+c.Payload.ResourceID, "--format", "{{.ID}}")
	ids := strings.Fields(string(raw))
	if len(ids) == 0 {
		return ""
	}
	if len(ids) != 1 || !dataSHA(ids[0]) || m.Preflight == nil {
		t.Fatal("ambiguous/unbound writable fixture native identity")
	}
	var inspected []struct {
		ID, Name, Image string
		Config          struct {
			Labels map[string]string
			User   string
		}
		HostConfig struct {
			ReadonlyRootfs bool
			NetworkMode    string
		}
		State struct{ Running bool }
	}
	if err := json.Unmarshal(candidateFixtureDocker(t, socket, "container", "inspect", ids[0]), &inspected); err != nil || len(inspected) != 1 || inspected[0].ID != ids[0] {
		t.Fatal("writable fixture inspect", err)
	}
	actual := inspected[0]
	if actual.Name != "/portal-app-"+c.Payload.ResourceID || actual.Image != m.Preflight.Native.ImageID || actual.Config.User != "65532:65532" || !actual.HostConfig.ReadonlyRootfs || actual.HostConfig.NetworkMode != "none" {
		t.Fatal("writable fixture native owner/policy changed")
	}
	expected := map[string]string{"format": "writable-candidate/1-dev.1", "deployment": c.DeploymentID, "epoch": c.ControllerEpoch, "installation": c.InstallationID, "operation": c.OperationID, "resource": c.Payload.ResourceID, "namespace": c.Payload.DataNamespaceID, "release": c.Payload.ReleaseSHA256, "generation": c.TargetGeneration, "control-fence": c.InstallationFence, "writer-fence": c.Payload.WriterFence, "sandbox-profile": control.WritableCandidateSandboxProfile, "start-effect": m.EffectID, "start-claim": m.ClaimID, "start-payload": m.PayloadSHA256, "start-ownership": m.OwnershipSHA256, "data-receipt": m.DataReceiptSHA256, "writer-receipt": m.WriterReceiptSHA256}
	for key, value := range expected {
		if actual.Config.Labels["org.portal.app."+key] != value {
			t.Fatal("refusing cleanup of rebound writable fixture", key)
		}
	}
	if !dataSHA(actual.Config.Labels["org.portal.app.policy-sha256"]) {
		t.Fatal("missing writable fixture policy hash")
	}
	return ids[0]
}

// Compare all durable control/resource rows; never print their contents.
func writableFixtureJournalSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	snapshot := map[string][][]any{}
	for _, table := range []string{"runner_deployment", "runner_commands", "runner_resources"} {
		rows, err := s.db.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot[table] = append(snapshot[table], values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return candidateHash(encoded)
}
func assertWritableFixtureReadiness(t *testing.T, s *Store, native *driver.CandidateRuntime, raw, sig, descriptor []byte, r WritableCandidateRunningReceipt) {
	t.Helper()
	before := writableFixtureJournalSnapshot(t, s)
	deadline := time.Now().Add(5 * time.Second)
	var ready WritableCandidateReadiness
	var err error
	for {
		ready, err = s.ObserveWritableCandidateReadiness(context.Background(), raw, sig, native, descriptor)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual writable readiness", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	encoded, _ := json.Marshal(r)
	if ready.Format != writableCandidateReadinessFormat || ready.NativeID != r.NativeID || ready.RunningReceiptSHA256 != candidateHash(encoded) || ready.PayloadSHA256 != r.Claim.PayloadSHA256 || ready.Protocol != "portal.backend/1" || ready.DataFormat != "1" {
		t.Fatal("readiness lost exact process/source binding")
	}
	p, err := s.prepareWritableCandidateReadiness(context.Background(), raw, sig, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := native.CheckWritableCandidateReadiness(context.Background(), p.request, p.expected, r.NativeID, "2"); err == nil {
		t.Fatal("mismatched backend data format accepted")
	}
	if _, err := native.CheckWritableCandidateReadiness(context.Background(), p.request, p.expected, strings.Repeat("f", 64), "1"); err == nil {
		t.Fatal("different native process probed")
	}
	if before != writableFixtureJournalSnapshot(t, s) {
		t.Fatal("readiness changed durable state")
	}
}

// Actual-source intent test only: secrets stay in disposable memory and no
// registration file is delivered by this helper.
func assertWritableFixtureRegistrationIntent(t *testing.T, s *Store, raw, sig, descriptor []byte, r WritableCandidateRunningReceipt, effect string, continuation func([]byte, []byte, []byte)) {
	t.Helper()
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	arow, err := s.ControllerDeployment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := control.Authority{DeploymentID: arow.DeploymentID, ControllerEpoch: arow.ControllerEpoch, ControlRevision: arow.ControlRevision, PublicKey: arow.PublicKey, Fingerprint: arow.Fingerprint}
	ref, err := control.VerifyWritableCandidate(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	index := ref.Index()
	reg, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: parsed.Manifest.ID, DeploymentID: index.DeploymentID, ControllerEpoch: index.ControllerEpoch, InstallationID: index.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: parsed.SHA256}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	document, err := reg.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(document)
	encoded, _ := json.Marshal(r)
	c := control.WritableBackendRegistrationCommand{Format: control.WritableBackendRegistrationFormat, EffectID: effect, CandidateRaw: raw, CandidateSignature: sig, RunningReceiptSHA256: candidateHash(encoded), DocumentSHA256: candidateHash(document)}
	signed, signature := signWritableRegistrationFixture(t, c)
	v, err := control.VerifyWritableBackendRegistration(signed, signature, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	checked, err := control.ValidateWritableBackendRegistrationDocument(v, descriptor, document)
	if err != nil {
		t.Fatal(err)
	}
	checked.Close()
	if _, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), signed, signature); err != nil {
		t.Fatal("actual writable registration intent", err)
	}
	view, err := s.ReadWritableBackendRegistrationIntent(context.Background(), signed, signature)
	if err != nil || view.NativeID != r.NativeID || view.RunningReceiptSHA256 != c.RunningReceiptSHA256 || view.DocumentSHA256 != c.DocumentSHA256 {
		t.Fatal("actual registration source view", err)
	}
	if replay, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), signed, signature); err != nil || !replay.Replayed {
		t.Fatal("actual registration intent replay", err)
	}
	bad := c
	bad.RunningReceiptSHA256 = strings.Repeat("f", 64)
	badRaw, badSig := signWritableRegistrationFixture(t, bad)
	if _, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), badRaw, badSig); err == nil {
		t.Fatal("different actual process receipt admitted")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+effect+`' AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL`) != 1 {
		t.Fatal("intent delivered or stored private registration")
	}
	if continuation != nil {
		continuation(signed, signature, document)
	}

}

func TestNativeJournalWritableCandidateExecution(t *testing.T) {
	image := os.Getenv("APP_WRITER_NATIVE_IMAGE")
	if image == "" {
		t.Skip("explicit disposable writer journal/XFS/Docker fixture only")
	}
	rootPath, journalPath, socket := os.Getenv("APP_DATA_JOURNAL_ROOT"), os.Getenv("APP_DATA_JOURNAL_CONTROL"), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET")
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || !filepath.IsAbs(rootPath) || !filepath.IsAbs(journalPath) || !filepath.IsAbs(socket) {
		t.Fatal("requires disposable root and explicit paths")
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
	sockets, err := os.MkdirTemp("/run", "portal-jwexec-")
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
	json.Unmarshal(raw, &manifest)
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
	for slot := 0; slot < 5; slot++ {
		t.Run(strconv.Itoa(slot), func(t *testing.T) {
			ctx := context.Background()
			base := 1800 + slot*20
			prepare := runnerCommandFixture(t)
			prepare.InstallationID, prepare.OperationID, prepare.EffectID, prepare.Payload.DataNamespaceID = fixtureID(base), fixtureID(base+1), fixtureID(base+2), fixtureID(base+3)
			prepare.Payload.ReleaseSHA256, prepare.Payload.QuotaBytes, prepare.Payload.QuotaInodes = parsed.SHA256, "8388608", "128"
			dataRaw, dataSig := signRunnerCommand(t, prepare)
			source, err := s.DispatchPrepareData(ctx, dataRaw, dataSig, root, pin)
			if err != nil {
				t.Fatal(err)
			}
			writer := writerCommandFixture(t, dataRaw, dataSig, source)
			writer.EffectID, writer.ResourceID, writer.Platform = fixtureID(base+4), fixtureID(base+5), "linux/"+runtime.GOARCH
			writerRaw, writerSig := signWriterFixture(t, writer)
			if _, err := s.AdmitDataWriterCommand(ctx, writerRaw, writerSig); err != nil {
				t.Fatal(err)
			}
			writerClaim, err := s.ClaimDataWriter(ctx, writerRaw, writerSig)
			if err != nil {
				t.Fatal(err)
			}
			written, err := s.ExecuteDataWriter(ctx, writerClaim, descriptor, native)
			if err != nil {
				t.Fatal(err)
			}
			c := writableIntentFixture(t, writerRaw, writerSig, written)
			c.EffectID = fixtureID(base + 6)
			if slot == 1 {
				now := time.Now().UnixMilli()
				prepare.IssuedMS, prepare.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+5000, 10)
				writer.DataRaw, writer.DataSignature = signRunnerCommand(t, prepare)
				c.WriterRaw, c.WriterSignature = signWriterFixture(t, writer)
			}
			raw, sig := signWritableIntent(t, c)
			if _, err := s.AdmitWritableCandidateCommand(ctx, raw, sig); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReserveWritableCandidate(ctx, raw, sig); err != nil {
				t.Fatal(err)
			}
			claim, err := s.ClaimWritableCandidate(ctx, raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			v, err := control.VerifyWritableCandidate(raw, sig, control.Authority{DeploymentID: enrollmentFixture(30).DeploymentID, ControllerEpoch: enrollmentFixture(30).ControllerEpoch, ControlRevision: 3, PublicKey: enrollmentFixture(30).PublicKey, Fingerprint: enrollmentFixture(30).Fingerprint}, time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			index := v.Index()
			t.Cleanup(func() {
				var marker writableCandidateClaimMarker
				var stored string
				if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&stored); err != nil {
					t.Error(err)
					return
				}
				if r, err := ParseWritableCandidateRunningReceipt([]byte(stored)); err == nil {
					marker = r.Claim
				} else if !canonicalDataJSON(stored, &marker) {
					t.Error("cleanup lost writable marker")
					return
				}
				id := writableFixtureOwnedNative(t, socket, index, marker)
				if id != "" {
					candidateFixtureDocker(t, socket, "container", "stop", "--time", "10", id)
					candidateFixtureDocker(t, socket, "container", "rm", id)
				}
				identity := driver.CandidateIdentity{DeploymentID: index.DeploymentID, ControllerEpoch: index.ControllerEpoch, InstallationID: index.InstallationID, OperationID: index.OperationID, ResourceID: index.Payload.ResourceID, NamespaceID: index.Payload.DataNamespaceID, ReleaseSHA256: index.Payload.ReleaseSHA256, Generation: index.TargetGeneration, ControlFence: index.InstallationFence, WriterFence: index.Payload.WriterFence}
				if err := native.ReleaseCandidateHTTPStorage(ctx, identity); err != nil {
					t.Error("owned writable IPC cleanup", err)
				}
			})
			if slot == 1 {
				if _, err := s.db.Exec(`CREATE TRIGGER fixture_writable_settlement_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + c.EffectID + `' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture writable receipt failure');END`); err != nil {
					t.Fatal(err)
				}
			}
			if slot == 2 {
				if _, err := s.db.Exec(`CREATE TRIGGER fixture_writable_preflight_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + c.EffectID + `' AND NEW.state='unknown' AND json_extract(NEW.receipt_json,'$.preflight') IS NOT NULL BEGIN SELECT RAISE(ABORT,'fixture writable preflight failure');END`); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := s.ExecuteWritableCandidate(ctx, claim, native, descriptor)
			if slot == 0 || slot == 3 || slot == 4 {
				if err != nil || !validWritableRunningReceipt(receipt) || receipt.Claim.Preflight == nil || receipt.Claim.Preflight.Native.NamespaceInode != source.NamespaceInode {
					t.Fatal("actual writable running settlement", err)
				}
				id := writableFixtureOwnedNative(t, socket, index, receipt.Claim)
				if id == "" || id != receipt.NativeID {
					t.Fatal("writable receipt does not identify actual process")
				}
				assertWritableFixtureReadiness(t, s, native, raw, sig, descriptor, receipt)
				if got, err := s.ReadWritableCandidateRunningReceipt(ctx, raw, sig); err != nil || !sameWritableReceipt(got, receipt) {
					t.Fatal("actual process historical read", err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UnixMilli()
				prepare.IssuedMS, prepare.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
				writer.DataRaw, writer.DataSignature = signRunnerCommand(t, prepare)
				c.WriterRaw, c.WriterSignature = signWriterFixture(t, writer)
				renewed, renewedSig := signWritableIntent(t, c)
				if got, err := s.ReadWritableCandidateRunningReceipt(ctx, renewed, renewedSig); err != nil || !sameWritableReceipt(got, receipt) {
					t.Fatal("actual process receipt lost after reopen/renewal", err)
				}
				if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+writer.ResourceID+`' AND native_id='`+id+`' AND revision=2 AND observed_state='running' AND observed_ms IS NOT NULL`) != 1 {
					t.Fatal("actual process/resource not atomic")
				}
				if slot == 0 {
					before := writableFixtureJournalSnapshot(t, s)
					candidateFixtureDocker(t, socket, "container", "stop", "--time", "10", id)
					if got, err := s.ObserveWritableCandidateReadiness(ctx, renewed, renewedSig, native, descriptor); err == nil || got.Format != "" {
						t.Fatal("stopped process reported ready")
					}
					if strings.TrimSpace(string(candidateFixtureDocker(t, socket, "container", "inspect", "--format", "{{.State.Running}}", id))) != "false" {
						t.Fatal("readiness restarted stopped process")
					}
					if before != writableFixtureJournalSnapshot(t, s) {
						t.Fatal("stopped readiness changed durable records")
					}
					assertWritableFixtureRegistrationIntent(t, s, renewed, renewedSig, descriptor, receipt, fixtureID(base+7), func(rr, rs, doc []byte) {
						if _, err := s.DispatchWritableBackendRegistration(ctx, rr, rs, descriptor, doc, native); err == nil {
							t.Fatal("stopped process accepted registration delivery")
						}
						if _, err := s.ReadWritableBackendRegistrationIntent(ctx, rr, rs); err != nil {
							t.Fatal("stopped preflight consumed prepared intent", err)
						}
						if _, err := os.Lstat(writableRegistrationFixturePath(t, rr, sockets)); !os.IsNotExist(err) {
							t.Fatal("stopped delivery created file")
						}
					})
				} else if slot == 3 {
					assertWritableFixtureRegistrationIntent(t, s, renewed, renewedSig, descriptor, receipt, fixtureID(base+7), func(rr, rs, doc []byte) {
						assertWritableRegistrationMissingFile(t, s, native, rr, descriptor, doc, sockets)
					})
				} else {
					assertWritableFixtureRegistrationIntent(t, s, renewed, renewedSig, descriptor, receipt, fixtureID(base+7), func(rr, rs, doc []byte) {
						assertWritableRegistrationNormalDelivery(t, s, native, rr, rs, descriptor, doc, sockets)
					})
				}

			} else if slot == 1 {
				if _, dropErr := s.db.Exec(`DROP TRIGGER fixture_writable_settlement_failure`); dropErr != nil {
					t.Fatal(dropErr)
				}
				if !errors.Is(err, ErrWritableCandidateUncertain) || !strings.Contains(err.Error(), "fixture writable receipt failure") || receipt.Format != "" {
					t.Fatal("failed commit reported actual start success", err)
				}
				var stored string
				s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&stored)
				var m writableCandidateClaimMarker
				if !canonicalDataJSON(stored, &m) || m.Preflight == nil || writableFixtureOwnedNative(t, socket, index, m) == "" {
					t.Fatal("native effect/binding not preserved after commit failure")
				}
				if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+writer.ResourceID+`' AND native_id='`+writableCandidatePendingPrefix+writer.ResourceID+`' AND revision=1 AND observed_state='unknown' AND observed_ms IS NULL`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+c.EffectID+`' AND state='unknown'`) != 1 {
					t.Fatal("failed process commit left half a result")
				}
				if _, err := s.ReadWritableCandidateRunningReceipt(ctx, raw, sig); err == nil {
					t.Fatal("unknown start read as applied")
				}
				if ready, err := s.ObserveWritableCandidateReadiness(ctx, raw, sig, native, descriptor); err == nil || ready.Format != "" {
					t.Fatal("unknown native start reported ready")
				}

				// Lose process-local handles while retaining the actual process.
				// Reopen both owners, wait out the original deadline, and use a
				// fresh signed observation envelope. No old claim is resumed.
				id := writableFixtureOwnedNative(t, socket, index, m)
				if _, err := s.ReconcileWritableCandidate(ctx, raw, sig, descriptor, native); err == nil {
					t.Fatal("live claim recovered")
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if err := native.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				native, err = driver.OpenCandidateRuntime(ctx, socket, rootPath, sockets, root, pin)
				if err != nil {
					t.Fatal(err)
				}
				expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
				if delay := time.Until(time.UnixMilli(expires + 1)); delay > 0 {
					time.Sleep(delay)
				}
				now := time.Now().UnixMilli()
				prepare.IssuedMS, prepare.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
				writer.DataRaw, writer.DataSignature = signRunnerCommand(t, prepare)
				c.WriterRaw, c.WriterSignature = signWriterFixture(t, writer)
				fresh, freshSig := signWritableIntent(t, c)
				beforeNative := candidateFixtureDocker(t, socket, "container", "inspect", "--format", "{{.Id}} {{.Created}} {{.State.StartedAt}} {{.RestartCount}}", id)
				if _, err := s.db.Exec(`CREATE TRIGGER fixture_writable_recovery_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + c.EffectID + `' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture writable recovery failure');END`); err != nil {
					t.Fatal(err)
				}
				if r, err := s.ReconcileWritableCandidate(ctx, fresh, freshSig, descriptor, native); err == nil || !strings.Contains(err.Error(), "fixture writable recovery failure") || r.Format != "" {
					t.Fatal("recovery failure bypassed", err)
				}
				if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+writer.ResourceID+`' AND revision=1 AND observed_state='unknown' AND observed_ms IS NULL`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+c.EffectID+`' AND state='unknown'`) != 1 {
					t.Fatal("recovery failure left half a result")
				}
				if _, err := s.db.Exec(`DROP TRIGGER fixture_writable_recovery_failure`); err != nil {
					t.Fatal(err)
				}
				recovered, err := s.ReconcileWritableCandidate(ctx, fresh, freshSig, descriptor, native)
				if err != nil || recovered.Format != writableRecoveredRunningFormat || recovered.NativeID != id || recovered.RecoveryEnvelopeSHA256 != candidateHash(fresh) {
					t.Fatal("actual observation recovery", err)
				}
				originalClaim, _ := json.Marshal(m)
				recoveredClaim, _ := json.Marshal(recovered.Claim)
				if string(originalClaim) != string(recoveredClaim) {
					t.Fatal("recovery rewrote original claim/preflight")
				}
				afterNative := candidateFixtureDocker(t, socket, "container", "inspect", "--format", "{{.Id}} {{.Created}} {{.State.StartedAt}} {{.RestartCount}}", id)
				if string(beforeNative) != string(afterNative) {
					t.Fatal("observation recovery restarted/replaced process")
				}
				if got, err := s.ReadWritableCandidateRunningReceipt(ctx, fresh, freshSig); err != nil || !sameWritableReceipt(got, recovered) {
					t.Fatal("recovered historical read", err)
				}
				if _, err := s.ReconcileWritableCandidate(ctx, fresh, freshSig, descriptor, native); err == nil {
					t.Fatal("applied recovery repeated")
				}
				assertWritableFixtureReadiness(t, s, native, fresh, freshSig, descriptor, recovered)
				assertWritableFixtureRegistrationIntent(t, s, fresh, freshSig, descriptor, recovered, fixtureID(base+7), func(rr, rs, doc []byte) {
					s, native = assertWritableRegistrationNativeDelivery(t, s, native, rr, descriptor, doc, sockets, journalPath, socket, rootPath, root, pin)
				})
			}
			if slot == 2 {
				if _, dropErr := s.db.Exec(`DROP TRIGGER fixture_writable_preflight_failure`); dropErr != nil {
					t.Fatal(dropErr)
				}
				if !errors.Is(err, ErrWritableCandidateUncertain) || !strings.Contains(err.Error(), "fixture writable preflight failure") || receipt.Format != "" {
					t.Fatal("preflight failure was bypassed", err)
				}
				var stored string
				if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				var m writableCandidateClaimMarker
				if !canonicalDataJSON(stored, &m) || m.Preflight != nil || writableFixtureOwnedNative(t, socket, index, m) != "" {
					t.Fatal("process started before preflight committed")
				}
				if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+writer.ResourceID+`' AND revision=1 AND observed_state='unknown' AND observed_ms IS NULL`) != 1 {
					t.Fatal("unbound preflight changed process state")
				}
			}
			if _, err := s.ClaimWritableCandidate(ctx, raw, sig); err == nil {
				t.Fatal("completed/unknown start reclaimed")
			}
			if again, err := s.ExecuteWritableCandidate(ctx, claim, native, descriptor); err == nil || again.Format != "" {
				t.Fatal("native start repeated")
			}
		})
	}
	t.Log("NATIVE_WRITABLE_JOURNAL_PROCESS_SETTLEMENT_VERIFIED")
	t.Log("NATIVE_WRITABLE_PROCESS_COMMIT_FAILURE_PRESERVES_UNKNOWN")
	t.Log("NATIVE_WRITABLE_PREFLIGHT_COMMIT_PRECEDES_PROCESS")
	t.Log("NATIVE_WRITABLE_START_RECOVERY_AFTER_EXPIRY_VERIFIED")
	t.Log("NATIVE_WRITABLE_RECOVERY_WITHOUT_PROCESS_RESTART")
	t.Log("NATIVE_WRITABLE_RECOVERY_METADATA_FAILURE_RETRY_VERIFIED")
	t.Log("NATIVE_WRITABLE_CURRENT_READINESS_VERIFIED")
	t.Log("NATIVE_WRITABLE_RECOVERED_READINESS_VERIFIED")
	t.Log("NATIVE_WRITABLE_READINESS_PRESERVES_JOURNAL")
	t.Log("NATIVE_WRITABLE_STOPPED_READINESS_REFUSED")
	t.Log("NATIVE_WRITABLE_REGISTRATION_INTENT_ACTUAL_SOURCES_VERIFIED")
	t.Log("NATIVE_WRITABLE_REGISTRATION_DELIVERY_VERIFIED")
	t.Log("NATIVE_WRITABLE_REGISTRATION_RECOVERY_PRESERVES_FILE")
	t.Log("NATIVE_WRITABLE_REGISTRATION_MISSING_FILE_NOT_CREATED")
	t.Log("NATIVE_WRITABLE_REGISTRATION_STOPPED_DELIVERY_REFUSED")
}
