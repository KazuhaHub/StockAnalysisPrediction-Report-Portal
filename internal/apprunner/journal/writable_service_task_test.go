//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// writableServiceTaskJournalFixture signs a generation-1 service task against
// the applied writable registration row and returns a physical snapshot that
// matches it exactly. Tests mutate one side to prove each binding is checked.
func writableServiceTaskJournalFixture(t *testing.T) (*Store, writableBackendExchangePreflight, []byte, []byte, []byte, driver.WritableServiceTaskSnapshot) {
	t.Helper()
	s, raw, sig, before := writableHandshakeFixture(t)
	index := before.facts.verified.Candidate().Index()
	now := time.Now().UnixMilli()
	taskID := fixtureID(1921)
	body, err := appplatform.EncodeTaskBackendRequest(taskID, "writable:1", []byte(`{"fixture":true}`))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := appplatform.SignServiceIdentity(bytes.Repeat([]byte{31}, 32), appplatform.ServiceIdentityGrant{
		Format: appplatform.ServiceIdentityFormat, LeaseID: fixtureID(1922),
		AppID: "notes-demo", InstallationID: index.InstallationID,
		ControllerEpoch: index.ControllerEpoch, Generation: "1", PolicyRevision: "3",
		ReleaseSHA256: index.Payload.ReleaseSHA256, Resources: []string{"tasks.schedule"},
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+60000, 10),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	command := control.ServiceBackendTaskCommand{
		Format: control.ServiceBackendTaskFormat, RequestID: fixtureID(1923),
		TaskID: taskID, LeaseID: fixtureID(1922), Handler: appplatform.TaskHandlerBackend,
		InstallationID: index.InstallationID, Generation: "1", PolicyRevision: "3",
		ControllerEpoch: index.ControllerEpoch, ReleaseSHA256: index.Payload.ReleaseSHA256,
		BootID: "boot-test", RegistrationRaw: raw, RegistrationSignature: sig,
		ServiceIdentity: identity, BodySHA256: candidateHash(body),
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	defer clear(key)
	task, signature, err := control.SignServiceBackendTask(command, key, before.facts.source.authority, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := driver.WritableServiceTaskSnapshot{
		Registration: before.binding, Handler: command.Handler,
		BodySHA256: candidateHash(body), ServiceIdentitySHA256: candidateHash(identity),
		Response:   driver.BackendResponse{Status: 200, Body: []byte(`{"format":"portal.app-task.response/1-dev.1","outcome":"succeeded","result":{"count":1}}`)},
		ObservedMS: before.facts.source.now,
	}
	return s, before, task, signature, body, snapshot
}

func TestWritableServiceTaskIsForwardedAgainstAppliedRegistration(t *testing.T) {
	s, before, raw, sig, body, snapshot := writableServiceTaskJournalFixture(t)
	defer s.Close()
	original := writableFixtureJournalSnapshot(t, s)
	result, err := s.finishWritableServiceBackendTask(context.Background(), before, raw, sig, body, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeliveryReceiptSHA256 != candidateHash([]byte(before.facts.row.receipt)) ||
		result.RequestSHA256 != candidateHash(raw) || result.TaskID != fixtureID(1921) ||
		result.NativeID != before.binding.NativeID || result.Response.Status != 200 {
		t.Fatal("result is not bound to the writable registration receipt", result)
	}
	if original != writableFixtureJournalSnapshot(t, s) {
		t.Fatal("a task wrote journal state")
	}
}

func TestWritableServiceTaskRefusesDriftAndSnapshotMismatch(t *testing.T) {
	for _, mode := range []string{
		"authority", "not applied", "stale source", "newer fence", "time before", "time after", "cancelled",
		"task signature", "body", "identity", "binding", "handler", "body hash", "identity hash",
	} {
		t.Run(mode, func(t *testing.T) {
			s, before, raw, sig, body, snapshot := writableServiceTaskJournalFixture(t)
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			queries := map[string]string{
				"authority":    `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"not applied":  `UPDATE runner_commands SET state='unknown' WHERE stage='activate' AND effect_number=3`,
				"stale source": `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
				"newer fence":  `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='activate' AND effect_number=3`,
			}
			switch mode {
			case "time before":
				snapshot.ObservedMS = before.facts.source.now - 1
			case "time after":
				snapshot.ObservedMS = time.Now().UnixMilli() + 3600000
			case "cancelled":
				cancel()
			case "task signature":
				sig[0] ^= 1
			case "body":
				body = []byte(`{"changed":true}`)
			case "identity":
				snapshot.ServiceIdentitySHA256 = strings.Repeat("0", 64)
			case "identity hash":
				snapshot.ServiceIdentitySHA256 = candidateHash([]byte("other"))
			case "body hash":
				snapshot.BodySHA256 = candidateHash([]byte("other"))
			case "binding":
				snapshot.Registration.NativeID = strings.Repeat("f", 64)
			case "handler":
				snapshot.Handler = appplatform.TaskHandlerDigest
			default:
				if _, err := s.db.Exec(queries[mode]); err != nil {
					t.Fatal(err)
				}
			}
			original := writableFixtureJournalSnapshot(t, s)
			if got, err := s.finishWritableServiceBackendTask(ctx, before, raw, sig, body, snapshot); err == nil || got.Format != "" {
				t.Fatal("drifted writable task returned a result", mode)
			}
			if mode != "authority" && mode != "not applied" && mode != "stale source" && mode != "newer fence" && original != writableFixtureJournalSnapshot(t, s) {
				t.Fatal("refusal changed journal")
			}
		})
	}
}

func TestWritableServiceTaskRefusesBeforeNativeIO(t *testing.T) {
	s, _, raw, sig, body, _ := writableServiceTaskJournalFixture(t)
	defer s.Close()
	verified, err := control.VerifyServiceBackendTask(raw, sig, controlAuthorityForTest(t, s), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := verified.WritableRegistration(); !ok {
		t.Fatal("fixture is not a writable task")
	}
	native := &driver.CandidateRuntime{}
	// A closing store and a missing native provider are refused before any
	// descriptor, gate or native work.
	if got, err := s.forwardWritableServiceBackendTask(context.Background(), verified, raw, sig, []byte("x"), body, nil); err == nil || got.Format != "" {
		t.Fatal("missing native provider produced a result")
	}
	s.closing.Store(true)
	if got, err := s.forwardWritableServiceBackendTask(context.Background(), verified, raw, sig, []byte("x"), body, native); err == nil || got.Format != "" {
		t.Fatal("closing store produced a result")
	}
	s.closing.Store(false)
	// A non-writable verified task is refused by the writable path.
	if got, err := s.forwardWritableServiceBackendTask(context.Background(), control.VerifiedServiceBackendTask{}, raw, sig, []byte("x"), body, native); err == nil || got.Format != "" {
		t.Fatal("unbound task produced a result")
	}
}

func controlAuthorityForTest(t *testing.T, s *Store) control.Authority {
	t.Helper()
	d, err := s.ControllerDeployment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
}
