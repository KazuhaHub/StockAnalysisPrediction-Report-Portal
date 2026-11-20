package transport

import (
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
)

func TestEgressPostgresAdmitCodec(t *testing.T) {
	good := EgressPostgresAdmit{Target: "orders-primary", Credential: "orders-probe"}
	payload, err := EncodeEgressPostgresAdmit(good)
	if err != nil || string(payload) != `{"target":"orders-primary","credential":"orders-probe"}` {
		t.Fatalf("%s %v", payload, err)
	}
	if got, err := DecodeEgressPostgresAdmit(payload); err != nil || got != good {
		t.Fatalf("%+v %v", got, err)
	}
	for name, raw := range map[string]string{
		"unknown member": `{"target":"orders","credential":"orders-probe","sql":"SELECT 2"}`,
		"reordered":      `{"credential":"orders-probe","target":"orders"}`,
		"bad target":     `{"target":"Orders","credential":"orders-probe"}`,
		"bad credential": `{"target":"orders","credential":"Orders Probe"}`,
		"empty":          `{"target":"","credential":""}`,
		"trailing":       `{"target":"orders","credential":"orders-probe"} `,
		"missing member": `{"target":"orders"}`,
	} {
		if _, err := DecodeEgressPostgresAdmit([]byte(raw)); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	if _, err := EncodeEgressPostgresAdmit(EgressPostgresAdmit{Target: strings.Repeat("a", 49), Credential: "x"}); err == nil {
		t.Fatal("an oversize target encoded")
	}
}

func TestEgressPostgresFrameAndOutcomeCodes(t *testing.T) {
	// The frame is a call start the runner sends and Portal receives, and only that way.
	if !isEgressCallStart(EgressFramePostgresAdmit) || !EgressPortal.receives(EgressFramePostgresAdmit) || EgressRunner.receives(EgressFramePostgresAdmit) {
		t.Fatal("the postgres admit has the wrong direction")
	}
	if !EgressRunner.sends(EgressFramePostgresAdmit) || EgressPortal.sends(EgressFramePostgresAdmit) {
		t.Fatal("the postgres admit is sent by the wrong side")
	}
	// Status codes 1..6 in the order of the doc, and nothing else.
	for i, name := range []string{"reachable", "connect_failed", "tls_failed", "auth_failed", "query_failed", "timeout"} {
		if EgressPostgresStatus(name) != uint16(i+1) || EgressPostgresOutcome(uint16(i+1)) != name {
			t.Fatalf("status of %s", name)
		}
	}
	if EgressPostgresStatus("uncertain") != 0 || EgressPostgresOutcome(0) != "" || EgressPostgresOutcome(7) != "" {
		t.Fatal("a code outside the six was accepted")
	}
	// A refused or cancelled check is never uncertain.
	for _, err := range []error{errors.New("anything"), broker.ErrUncertain, broker.ErrUnavailable} {
		if o := egressPostgresOutcomeOf(broker.PostgresResult{}, err); o.Class == EgressOutcomeUncertain {
			t.Fatalf("%v became uncertain", err)
		}
		if broker.AppPostgresErrorFor(err) == broker.AppErrUncertain {
			t.Fatalf("%v mapped to uncertain", err)
		}
	}
	if broker.AppPostgresErrorFor(broker.ErrDenied) != broker.AppErrDenied || broker.AppPostgresErrorFor(broker.ErrBusy) != broker.AppErrBusy {
		t.Fatal("denied and busy lost their codes")
	}
}

func TestPostgresProbeProfileCodec(t *testing.T) {
	raw, err := EncodePostgresProbeProfile(NewPostgresProbeProfile())
	if err != nil || string(raw) != `{"format":"portal.runner.postgres-probe/1-dev.1","command_format":"portal.runner.postgres-probe-request/1-dev.1","policy_format":"portal.postgres-probe-policy/1-dev.1"}` {
		t.Fatalf("%s %v", raw, err)
	}
	if _, err := ParsePostgresProbeProfile(raw); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"other format":  strings.Replace(string(raw), "probe/1-dev.1", "probe/2-dev.1", 1),
		"extra member":  strings.Replace(string(raw), `}`, `,"server_version":"16"}`, 1),
		"other command": strings.Replace(string(raw), "request/1-dev.1", "request/2-dev.1", 1),
		"not json":      "PG",
	} {
		if _, err := ParsePostgresProbeProfile([]byte(bad)); !errors.Is(err, ErrPostgresProbeUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
