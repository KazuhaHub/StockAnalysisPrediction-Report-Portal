//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func syntheticAppliedWriterFixture(t *testing.T) (*Store, []byte, []byte, DataWriterReceipt) {
	t.Helper()
	s, raw, sig, _ := admittedWriterFixture(t)
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	tx, f, err := s.beginDataWriter(context.Background(), raw, sig, "unknown", &claim.state.marker)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var marker dataWriterClaimMarker
	json.Unmarshal([]byte(claim.state.marker), &marker)
	source := f.source
	root := markerRoot(source.Claim)
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	r := DataWriterReceipt{Format: "portal.runner.data-writer-receipt/1-dev.1", Claim: marker, ControlRevision: f.verified.Data().ControlRevision, SettledMS: now, Native: dataWriterNativeEvidence{Format: "portal.runner.data-writer-native/1-dev.1", ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64", Profile: control.DataWriterSandboxProfile, SeccompSHA256: driver.DataWriterPolicySHA256(), RootFilesystemUUID: root.FilesystemUUID, RootInode: strconv.FormatUint(root.Inode, 10), ProjectID: source.ProjectID, Device: source.Device, NamespaceInode: source.NamespaceInode, QuotaBytes: source.QuotaBytes, QuotaInodes: source.QuotaInodes, UsedBytes: source.UsedBytes, UsedInodes: source.UsedInodes, ObservedMS: now}}
	r.BindingSHA256 = dataWriterReceiptBinding(r)
	if !validDataWriterReceipt(r) {
		t.Fatal("synthetic reader fixture shape")
	}
	encoded, _ := json.Marshal(r)
	// Direct rows validate historical reads only. They do not construct opaque
	// native evidence and are never submitted as a physical settlement proof.
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE kind='activate'`, string(encoded), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET revision=3,observed_ms=?,updated_ms=? WHERE kind='volume'`, now, now); err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, r
}

func TestDataWriterHistoricalReceiptReadAndRenewal(t *testing.T) {
	s, raw, sig, want := syntheticAppliedWriterFixture(t)
	defer s.Close()
	got, err := s.ReadDataWriterReceipt(context.Background(), raw, sig)
	if err != nil || got != want {
		t.Fatal("historical writer receipt", err)
	}
	var c control.DataWriterCommand
	json.Unmarshal(raw, &c)
	var ref control.Command
	json.Unmarshal(c.DataRaw, &ref)
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli(), 10), strconv.FormatInt(time.Now().UnixMilli()+25000, 10)
	c.DataRaw, c.DataSignature = signRunnerCommand(t, ref)
	raw, sig = signWriterFixture(t, c)
	if got, err := s.ReadDataWriterReceipt(context.Background(), raw, sig); err != nil || got != want {
		t.Fatal("renewal rewrote historical proof", err)
	}
	if _, err := s.ClaimDataWriter(context.Background(), raw, sig); err == nil {
		t.Fatal("applied writer reclaimed")
	}
	if _, err := s.ReadDataPreparationReceipt(context.Background(), c.DataRaw, c.DataSignature); err == nil {
		t.Fatal("write-enabled resource adopted as sealed")
	}
}

func TestDataWriterHistoricalReceiptRejectsDrift(t *testing.T) {
	for _, mode := range []string{"volume_revision", "volume_time", "writer_time", "writer_ack", "source_ack", "authority", "native_inode", "policy", "binding", "grant_revision"} {
		t.Run(mode, func(t *testing.T) {
			s, raw, sig, r := syntheticAppliedWriterFixture(t)
			defer s.Close()
			queries := map[string]string{
				"volume_revision": `UPDATE runner_resources SET revision=2`,
				"volume_time":     `UPDATE runner_resources SET observed_ms=observed_ms+1`,
				"writer_time":     `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE kind='activate'`,
				"writer_ack":      `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='activate'`,
				"source_ack":      `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='prepare-data'`,
				"authority":       `UPDATE runner_deployment SET control_revision=control_revision+1`,
			}
			if q := queries[mode]; q != "" {
				if _, err := s.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			} else {
				switch mode {
				case "native_inode":
					n, _ := strconv.ParseUint(r.Native.NamespaceInode, 10, 64)
					r.Native.NamespaceInode = strconv.FormatUint(n+1, 10)
				case "policy":
					r.Native.SeccompSHA256 = strings.Repeat("f", 64)
				case "binding":
					r.BindingSHA256 = strings.Repeat("f", 64)
				case "grant_revision":
					r.ControlRevision = "2"
				}
				if mode != "binding" {
					r.BindingSHA256 = dataWriterReceiptBinding(r)
				}
				encoded, _ := json.Marshal(r)
				if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE kind='activate'`, string(encoded)); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.ReadDataWriterReceipt(context.Background(), raw, sig); err == nil || got != (DataWriterReceipt{}) {
				t.Fatal("altered writer receipt accepted", mode, err)
			}
		})
	}
}

func TestDataWriterHistoricalReceiptCurrentKeyKeepsGrantRevision(t *testing.T) {
	s, raw, _, want := syntheticAppliedWriterFixture(t)
	defer s.Close()
	e := enrollmentFixture(31)
	e.ControlRevision = 4
	if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=4,verification_key=?,key_fingerprint=?,revision=revision+1`, e.PublicKey, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	var c control.DataWriterCommand
	json.Unmarshal(raw, &c)
	var ref control.Command
	json.Unmarshal(c.DataRaw, &ref)
	ref.ControlRevision = "4"
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: 4, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	var err error
	c.DataRaw, c.DataSignature, err = control.Sign(ref, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	raw, sig := signWriterWithAuthority(t, c, key, a)
	if got, err := s.ReadDataWriterReceipt(context.Background(), raw, sig); err != nil || got != want || got.ControlRevision != "3" {
		t.Fatal("grant revision changed on current-key read", err)
	}
}

func syntheticWriterExecutionFixture(t *testing.T) (*Store, []byte, []byte, []byte) {
	t.Helper()
	s, _ := enrolledCommandStore(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	c := runnerCommandFixture(t)
	c.Payload.ReleaseSHA256 = parsed.SHA256
	source, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), source, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), source, sig); err != nil {
		t.Fatal(err)
	}
	dataClaim, err := s.ClaimPrepareData(context.Background(), source, sig)
	if err != nil {
		t.Fatal(err)
	}
	// Use the existing synthetic data fixture only to obtain journal history.
	var reservation DataReservation
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	if _, err := s.RunPrepareDataOnce(context.Background(), dataClaim, func(ctx context.Context, r DataReservation, _ control.PrepareDataPayload) error {
		reservation = r
		return s.bindDataRoot(ctx, dataClaim.state, pin)
	}); err != nil {
		t.Fatal(err)
	}
	proof, err := s.settlePreparedData(context.Background(), dataClaim.state, pin, syntheticDataObservation(reservation))
	if err != nil {
		t.Fatal(err)
	}
	writerRaw, writerSig := signWriterFixture(t, writerCommandFixture(t, source, sig, proof))
	return s, writerRaw, writerSig, descriptor
}

func TestDataWriterCallbackCannotSettleOpaqueEvidence(t *testing.T) {
	s, writerRaw, writerSig, descriptor := syntheticWriterExecutionFixture(t)
	defer s.Close()
	if _, err := s.AdmitDataWriterCommand(context.Background(), writerRaw, writerSig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDataWriter(context.Background(), writerRaw, writerSig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunDataWriterOnce(context.Background(), claim, func(context.Context, DataWriterPlan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, err := s.settleDataWriter(context.Background(), claim, descriptor, driver.DataWriterObservation{}); err == nil || got != (DataWriterReceipt{}) {
		t.Fatal("callback/zero native observation settled writer")
	}
	if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='volume'`) != 2 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='activate' AND state='unknown'`) != 1 {
		t.Fatal("failed evidence wrote partial completion")
	}
}

func TestDataWriterReceiptRejectsZeroIdentity(t *testing.T) {
	s, _, _, original := syntheticAppliedWriterFixture(t)
	defer s.Close()
	for _, field := range []string{"claim", "effect", "resource", "filesystem"} {
		t.Run(field, func(t *testing.T) {
			r := original
			zero := "00000000-0000-0000-0000-000000000000"
			switch field {
			case "claim":
				r.Claim.ClaimID = zero
			case "effect":
				r.Claim.EffectID = zero
			case "resource":
				r.Claim.ResourceID = zero
			case "filesystem":
				r.Native.RootFilesystemUUID = zero
			}
			r.BindingSHA256 = dataWriterReceiptBinding(r)
			raw, _ := json.Marshal(r)
			if _, err := ParseDataWriterReceipt(raw); err == nil {
				t.Fatal("zero receipt identity accepted")
			}
		})
	}
}
