//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Synthetic registration metadata authorizes no native snapshot or response.
func writableRequestJournalFixture(t *testing.T) (*Store, writableBackendExchangePreflight, []byte, []byte, []byte, string) {
	t.Helper()
	s, raw, sig, before := writableHandshakeFixture(t)
	index := before.facts.verified.Candidate().Index()
	material, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: "notes-demo", DeploymentID: index.DeploymentID, ControllerEpoch: index.ControllerEpoch, InstallationID: index.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: index.Payload.ReleaseSHA256}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	signer, err := material.Signer("boot-test")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	body := []byte(`{"value":1}`)
	assertion, err := signer.Sign("POST", "/v1/echo", body, protocol.Viewer{Subject: strings.Repeat("a", 64), Role: "user"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	command := control.WritableBackendRequestCommand{Format: control.WritableBackendRequestFormat, RequestID: fixtureID(1910), RegistrationRaw: raw, RegistrationSignature: sig, BootID: "boot-test", Method: "POST", Path: "/v1/echo", BodySHA256: candidateHash(body), AssertionSHA256: candidateHash([]byte(assertion))}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	defer clear(key)
	request, signature, err := control.SignWritableBackendRequest(command, key, before.facts.source.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return s, before, request, signature, body, assertion
}
func TestWritableBackendRequestMetadataAndOpaqueBoundary(t *testing.T) {
	r := WritableBackendRequestResult{Format: writableBackendRequestResultFormat, RequestID: fixtureID(1910), RequestSHA256: strings.Repeat("a", 64), DeliveryReceiptSHA256: strings.Repeat("b", 64), NativeID: strings.Repeat("c", 64), BootID: "boot-1", Response: driver.BackendResponse{Status: 200, Body: []byte(`{"ok":true}`)}, ObservedMS: "1"}
	raw, _ := json.Marshal(r)
	if got, err := ParseWritableBackendRequestResult(raw); err != nil || !bytes.Equal(got.Response.Body, r.Response.Body) {
		t.Fatal("canonical reply", err)
	}
	for _, bad := range [][]byte{nil, append(raw, ' '), []byte(strings.Replace(string(raw), writableBackendRequestResultFormat, "portal.runner.backend-request-result/1-dev.1", 1)), []byte(strings.Replace(string(raw), `"observed_ms":"1"`, `"observed_ms":"-1"`, 1)), []byte(strings.Replace(string(raw), `"status":200`, `"status":302`, 1)), []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`), []byte(strings.TrimSuffix(string(raw), "}") + `,"boot_id":"boot-1"}`)} {
		if got, err := ParseWritableBackendRequestResult(bad); err == nil || got.Format != "" {
			t.Fatal("ambiguous reply accepted")
		}
	}
	if _, err := ParseBackendRequestResult(raw); err == nil {
		t.Fatal("writable reply crossed readonly protocol")
	}
	if _, err := new(Store).ForwardWritableBackendRequest(context.Background(), nil, nil, nil, nil, "", nil); err == nil {
		t.Fatal("missing native provider supplied reply")
	}
	s, before, request, signature, body, assertion := writableRequestJournalFixture(t)
	defer s.Close()
	original := writableFixtureJournalSnapshot(t, s)
	if got, err := s.finishWritableBackendRequest(context.Background(), before, request, signature, body, assertion, driver.WritableBackendRequestObservation{}); err == nil || got.Format != "" {
		t.Fatal("zero native provenance produced a reply")
	}
	if original != writableFixtureJournalSnapshot(t, s) {
		t.Fatal("zero proof changed journal")
	}
}
func TestWritableBackendRequestRefusesCurrentDrift(t *testing.T) {
	for _, mode := range []string{"authority", "ack", "source", "native", "file_receipt", "time", "cancelled", "request", "body", "assertion"} {
		t.Run(mode, func(t *testing.T) {
			s, before, raw, sig, body, assertion := writableRequestJournalFixture(t)
			defer s.Close()
			queries := map[string]string{"authority": `UPDATE runner_deployment SET control_revision=control_revision+1`, "ack": `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='activate' AND effect_number=3`, "source": `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`, "native": `UPDATE runner_resources SET native_id='` + strings.Repeat("f", 64) + `' WHERE kind='container'`, "file_receipt": `UPDATE runner_commands SET receipt_json='{}' WHERE stage='activate' AND effect_number=3`, "time": `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='activate' AND effect_number=3`}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "cancelled":
				cancel()
			case "request":
				sig[0] ^= 1
			case "body":
				body = []byte(`{"value":2}`)
			case "assertion":
				assertion += "x"
			default:
				if _, err := s.db.Exec(queries[mode]); err != nil {
					t.Fatal(err)
				}
			}
			original := writableFixtureJournalSnapshot(t, s)
			if got, err := s.finishWritableBackendRequest(ctx, before, raw, sig, body, assertion, driver.WritableBackendRequestObservation{}); err == nil || got.Format != "" {
				t.Fatal("invalid request/source accepted", mode)
			}
			if original != writableFixtureJournalSnapshot(t, s) {
				t.Fatal("request refusal changed journal")
			}
		})
	}
}
