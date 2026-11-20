//go:build linux

package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const egrPostgresRequest = `{"requestId":"req-1","target":"orders","credential":"orders-probe"}`

func (f *egrFixture) postgres(t *testing.T, body string) egrReply {
	t.Helper()
	r, err := f.request("1", "POST", "/v1/postgres/liveness", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var egrPostgresResources = []string{"network.http", "network.postgres", "secrets.use"}

// postgresCommand is the command a correct Portal signs for a postgres admit.
func (p *portalSide) postgresCommand(admit EgressPostgresAdmit) control.PostgresProbeCommand {
	policy, err := appplatform.CanonicalPostgresPolicy(appplatform.PostgresPolicy{Targets: []appplatform.PostgresTarget{{ID: "orders", Host: "db.example.com", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPublic}, {ID: "orders-b", Host: "replica.example.com", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPublic}}, Deadline: 3 * time.Second, Concurrency: 2})
	if err != nil {
		p.t.Fatal(err)
	}
	parsed, _ := appplatform.ParsePostgresPolicy(policy)
	scope := sha256.Sum256([]byte("portal-network-service/v1:" + p.command.SessionID))
	now := time.Now().UnixMilli()
	sessionExpires, _ := strconv.ParseInt(p.command.ExpiresMS, 10, 64)
	return control.PostgresProbeCommand{Format: control.PostgresProbeCommandFormat, DeploymentID: p.f.a.DeploymentID, ControllerEpoch: p.command.ControllerEpoch, ControlRevision: p.command.ControlRevision, RequestID: "30000000-0000-4000-8000-" + hex.EncodeToString(scope[:6]), InstallationID: p.command.InstallationID, Generation: p.command.Generation, PolicyRevision: p.command.PolicyRevision, ReleaseSHA256: p.command.ReleaseSHA256, ActorScope: hex.EncodeToString(scope[:]), PolicyRaw: policy, PolicySHA256: parsed.SHA256, TargetID: admit.Target, CredentialName: admit.Credential, Username: []byte("status_probe"), Password: []byte("a long password"), IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(min(now+20000, sessionExpires), 10)}
}

// servePostgres answers postgres admits like Portal: it signs a command (after mutate), then answers
// challenges with proofs unless withhold is set. The outcome frame lands in p.outcomes.
func (p *portalSide) servePostgres(mutate func(*control.PostgresProbeCommand), withhold bool) {
	p.mu.Lock()
	p.answer = func(call *EgressCall, first EgressFrame) bool {
		if first.Type != EgressFramePostgresAdmit {
			return false
		}
		admit, err := DecodeEgressPostgresAdmit(first.Payload)
		if err != nil {
			p.t.Errorf("postgres admit frame: %v", err)
			return true
		}
		command := p.postgresCommand(admit)
		if mutate != nil {
			mutate(&command)
		}
		raw, sig, err := control.SignPostgresProbeCommand(command, p.f.key, p.f.a, time.Now().UnixMilli())
		if err != nil {
			_ = call.Send(EgressFrameRefusal, []byte{EgressRefusalDenied})
			return true
		}
		payload, _ := EncodeEgressSigned(raw, sig)
		if call.Send(EgressFrameCommand, payload) != nil {
			return true
		}
		digest := sha256.Sum256(raw)
		for {
			frame, err := call.Recv(context.Background())
			if err != nil {
				return true
			}
			switch frame.Type {
			case EgressFrameChallenge:
				if withhold {
					continue
				}
				_, nonce, err := DecodeEgressChallenge(frame.Payload)
				if err != nil {
					return true
				}
				proof, psig, err := control.SignPostgresProbeCheckDigest(hex.EncodeToString(digest[:]), hex.EncodeToString(nonce[:]), mustInt(p.t, command.ExpiresMS), p.f.key, time.Now().UnixMilli())
				if err != nil {
					return true
				}
				out, _ := EncodeEgressSigned(proof, psig)
				if call.Send(EgressFrameProof, out) != nil {
					return true
				}
			case EgressFrameOutcome:
				o, err := DecodeEgressOutcome(frame.Payload)
				if err != nil {
					p.t.Errorf("outcome: %v", err)
					return true
				}
				p.outcomes <- o
				return true
			}
		}
	}
	p.mu.Unlock()
}

func TestServiceEgressPostgresNeedsTheResourcesAndAValidRequest(t *testing.T) {
	f := newEgrFixture(t)
	f.open(egrOptions{})
	// A session without network.postgres: denied, and nothing was asked of Portal.
	egrError(t, f.postgres(t, egrPostgresRequest), 403, "denied")

	f = newEgrFixture(t)
	f.open(egrOptions{resources: []string{"network.http", "network.postgres"}})
	// No secrets.use: a check always names a binding, so the session cannot make one.
	egrError(t, f.postgres(t, egrPostgresRequest), 403, "denied")

	f = newEgrFixture(t)
	f.open(egrOptions{resources: egrPostgresResources})
	for name, body := range map[string]string{
		"unknown member": `{"requestId":"req-1","target":"orders","credential":"orders-probe","sql":"SELECT 2"}`,
		"missing member": `{"requestId":"req-1","target":"orders"}`,
		"bad target":     `{"requestId":"req-1","target":"Orders","credential":"orders-probe"}`,
		"bad credential": `{"requestId":"req-1","target":"orders","credential":"orders probe"}`,
		"duplicate":      `{"requestId":"req-1","target":"orders","target":"other","credential":"orders-probe"}`,
		"not an object":  `[]`,
	} {
		t.Run(name, func(t *testing.T) { egrError(t, f.postgres(t, body), 400, "invalid") })
	}
	if r, err := f.request("1", "GET", "/v1/postgres/liveness", "", ""); err != nil || r.status != 400 {
		t.Fatalf("GET: %+v %v", r, err)
	}
}

func TestServiceEgressPostgresPortalRefusalsKeepTheirCodes(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{resources: egrPostgresResources})
	for code, want := range map[byte][2]string{EgressRefusalDenied: {"403", "denied"}, EgressRefusalUnavailable: {"503", "unavailable"}, EgressRefusalBusy: {"429", "busy"}} {
		p.mu.Lock()
		p.answer = func(call *EgressCall, first EgressFrame) bool {
			if first.Type != EgressFramePostgresAdmit {
				return false
			}
			_ = call.Send(EgressFrameRefusal, []byte{code})
			return true
		}
		p.mu.Unlock()
		status, _ := strconv.Atoi(want[0])
		egrError(t, f.postgres(t, egrPostgresRequest), status, want[1])
	}
}

func TestServiceEgressPostgresCommandForAnotherTupleIsDenied(t *testing.T) {
	for name, mutate := range map[string]func(*control.PostgresProbeCommand){
		"installation": func(c *control.PostgresProbeCommand) { c.InstallationID = "40000000-0000-4000-8000-0000000000ff" },
		"generation":   func(c *control.PostgresProbeCommand) { c.Generation = "2" },
		"release":      func(c *control.PostgresProbeCommand) { c.ReleaseSHA256 = strings.Repeat("c", 64) },
		"actor scope":  func(c *control.PostgresProbeCommand) { c.ActorScope = strings.Repeat("d", 64) },
		"other target": func(c *control.PostgresProbeCommand) { c.TargetID = "orders" + "-b" },
		"credential":   func(c *control.PostgresProbeCommand) { c.CredentialName = "another-probe" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newEgrFixture(t)
			p := f.open(egrOptions{resources: egrPostgresResources})
			p.servePostgres(mutate, false)
			egrError(t, f.postgres(t, egrPostgresRequest), 403, "denied")
			select {
			case o := <-p.outcomes:
				if o.Class != EgressOutcomeDenied {
					t.Fatalf("outcome %+v", o)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the refusal was not reported")
			}
		})
	}
}

func TestServiceEgressPostgresWithheldProofIsDeniedBeforeAnyDial(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{resources: egrPostgresResources})
	p.servePostgres(nil, true)
	started := time.Now()
	egrError(t, f.postgres(t, egrPostgresRequest), 403, "denied")
	if time.Since(started) > 5*time.Second {
		t.Fatalf("a withheld proof held the call for %s", time.Since(started))
	}
	select {
	case o := <-p.outcomes:
		if o.Class != EgressOutcomeDenied || o.Status != 0 {
			t.Fatalf("outcome %+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the denial was not reported")
	}
}

func TestPostgresProbeQueryRoundTrip(t *testing.T) {
	path := targetsEndpoint(t, true, nil)
	got, err := queryPostgresProbe(context.Background(), path, privateProfile(uint32(os.Geteuid())))
	if err != nil || got != NewPostgresProbeProfile() {
		t.Fatalf("%+v %v", got, err)
	}
	// A runner without a network provider does not answer, like one that predates the query.
	path = targetsEndpoint(t, false, nil)
	if _, err := queryPostgresProbe(context.Background(), path, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrPostgresProbeUnsupported) {
		t.Fatalf("narrow runner: %v", err)
	}
}
