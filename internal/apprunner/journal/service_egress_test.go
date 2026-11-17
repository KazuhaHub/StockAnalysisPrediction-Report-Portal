//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// serviceEgressBinderFixture counts native calls and hands out a listener whose
// closure the tests can observe.
type serviceEgressBinderFixture struct {
	driver.CandidateRuntime
	calls  atomic.Int32
	listen atomic.Bool
	closed atomic.Bool
	err    error
}

type observedListener struct {
	net.Listener
	closed *atomic.Bool
}

func (l observedListener) Close() error { l.closed.Store(true); return nil }

func (b *serviceEgressBinderFixture) PrepareCandidate(context.Context, driver.CandidateLaunchRequest) (driver.CandidateLaunch, error) {
	return driver.CandidateLaunch{}, driver.ErrUnsupported
}
func (b *serviceEgressBinderFixture) BindServiceBroker(context.Context, driver.CandidateLaunch, driver.BackendRegistrationSnapshot, bool) (driver.ServiceBroker, error) {
	return driver.ServiceBroker{}, driver.ErrUnsupported
}
func (b *serviceEgressBinderFixture) BindServiceBrokerUpdateStart(context.Context, driver.UpdateStartLaunchRequest, driver.UpdateStartLaunchSnapshot, driver.UpdateStartRegistrationSnapshot, bool) (driver.ServiceBroker, error) {
	return driver.ServiceBroker{}, driver.ErrUnsupported
}
func (b *serviceEgressBinderFixture) BindServiceBrokerWritable(_ context.Context, _ driver.WritableCandidateLaunchRequest, _ driver.WritableBackendRegistrationSnapshot, listen bool) (driver.ServiceBroker, error) {
	b.calls.Add(1)
	b.listen.Store(listen)
	if b.err != nil {
		return driver.ServiceBroker{}, b.err
	}
	if !listen {
		return driver.ServiceBroker{}, nil
	}
	return driver.ServiceBroker{Listener: observedListener{closed: &b.closed}}, nil
}

// writableEgressSession signs an egress session for the applied writable
// registration of writableHandshakeFixture.
func writableEgressSession(t *testing.T, before writableBackendExchangePreflight, registrationRaw, registrationSig []byte, mutate func(*control.ServiceEgressSessionCommand)) ([]byte, []byte, control.ServiceEgressSessionCommand) {
	t.Helper()
	index := before.facts.verified.Candidate().Index()
	authority := before.facts.source.authority
	now := time.Now().UnixMilli()
	c := control.ServiceEgressSessionCommand{
		Format: control.ServiceEgressSessionFormat, DeploymentID: authority.DeploymentID,
		ControllerEpoch: index.ControllerEpoch, ControlRevision: strconv.FormatInt(authority.ControlRevision, 10),
		SessionID: fixtureID(2001), InstallationID: index.InstallationID, Generation: "1", PolicyRevision: "3",
		ReleaseSHA256: index.Payload.ReleaseSHA256, ServiceIdentitySHA256: strings.Repeat("a", 64),
		Resources: []string{"network.http"}, Concurrency: "2", RatePerMinute: "120", Burst: "20",
		RegistrationRaw: registrationRaw, RegistrationSignature: registrationSig,
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+120000, 10),
	}
	if mutate != nil {
		mutate(&c)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	defer clear(key)
	raw, sig, err := control.SignServiceEgressSession(c, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, c
}

func TestServiceEgressBindingCallsBinderOnce(t *testing.T) {
	for _, listen := range []bool{true, false} {
		s, regRaw, regSig, before := writableHandshakeFixture(t)
		raw, sig, _ := writableEgressSession(t, before, regRaw, regSig, nil)
		binder := &serviceEgressBinderFixture{}
		original := writableFixtureJournalSnapshot(t, s)
		broker, err := s.bindPreparedServiceEgressWritable(context.Background(), before, raw, sig, binder, listen)
		if err != nil {
			t.Fatal(err)
		}
		if binder.calls.Load() != 1 || binder.listen.Load() != listen || (broker.Listener != nil) != listen {
			t.Fatalf("calls=%d listen=%v broker=%+v", binder.calls.Load(), binder.listen.Load(), broker)
		}
		if binder.closed.Load() {
			t.Fatal("a good binding was closed")
		}
		if original != writableFixtureJournalSnapshot(t, s) {
			t.Fatal("opening a session wrote journal state")
		}
		s.Close()
	}
	// A native failure is returned as it is and leaves nothing bound.
	s, regRaw, regSig, before := writableHandshakeFixture(t)
	defer s.Close()
	raw, sig, _ := writableEgressSession(t, before, regRaw, regSig, nil)
	binder := &serviceEgressBinderFixture{err: driver.ErrContainerUncertain}
	if broker, err := s.bindPreparedServiceEgressWritable(context.Background(), before, raw, sig, binder, true); err == nil || broker.Listener != nil || binder.calls.Load() != 1 {
		t.Fatal("native failure produced a binding")
	}
}

func TestServiceEgressBindingRefusesTupleDrift(t *testing.T) {
	// Refused before the native call: the binder is never invoked.
	for _, mode := range []string{"signature", "other enrollment", "closing store"} {
		t.Run(mode, func(t *testing.T) {
			s, regRaw, regSig, before := writableHandshakeFixture(t)
			defer s.Close()
			raw, sig, _ := writableEgressSession(t, before, regRaw, regSig, nil)
			ctx := context.Background()
			switch mode {
			case "signature":
				sig[0] ^= 1
			case "other enrollment":
				before.facts.source.authority.ControlRevision++
			case "closing store":
				s.closing.Store(true)
			}
			binder := &serviceEgressBinderFixture{}
			if broker, err := s.bindPreparedServiceEgressWritable(ctx, before, raw, sig, binder, true); err == nil || broker.Listener != nil {
				t.Fatal("drifted session produced a binding")
			}
			if binder.calls.Load() != 0 {
				t.Fatal("native bind ran for a refused session")
			}
		})
	}
	// Drift the journal shows after the bind: the socket is closed again.
	queries := map[string]string{
		"authority":    `UPDATE runner_deployment SET control_revision=control_revision+1`,
		"not applied":  `UPDATE runner_commands SET state='unknown' WHERE stage='activate' AND effect_number=3`,
		"stale source": `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
		"newer fence":  `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='activate' AND effect_number=3`,
	}
	for mode, query := range queries {
		t.Run(mode, func(t *testing.T) {
			s, regRaw, regSig, before := writableHandshakeFixture(t)
			defer s.Close()
			raw, sig, _ := writableEgressSession(t, before, regRaw, regSig, nil)
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			binder := &serviceEgressBinderFixture{}
			broker, err := s.bindPreparedServiceEgressWritable(context.Background(), before, raw, sig, binder, true)
			if err == nil || broker.Listener != nil {
				t.Fatal("drifted journal produced a binding")
			}
			if binder.calls.Load() == 1 && !binder.closed.Load() {
				t.Fatal("a refused binding left its socket open")
			}
		})
	}
	// A session for another tuple than its registration cannot even be signed;
	// the same check guards the open.
	s, regRaw, regSig, before := writableHandshakeFixture(t)
	defer s.Close()
	index := before.facts.verified.Candidate().Index()
	authority := before.facts.source.authority
	for name, mutate := range map[string]func(*control.ServiceEgressSessionCommand){
		"installation": func(c *control.ServiceEgressSessionCommand) { c.InstallationID = fixtureID(9) },
		"generation":   func(c *control.ServiceEgressSessionCommand) { c.Generation = "2" },
		"release":      func(c *control.ServiceEgressSessionCommand) { c.ReleaseSHA256 = strings.Repeat("c", 64) },
		"epoch":        func(c *control.ServiceEgressSessionCommand) { c.ControllerEpoch = fixtureID(8) },
	} {
		now := time.Now().UnixMilli()
		c := control.ServiceEgressSessionCommand{Format: control.ServiceEgressSessionFormat, DeploymentID: authority.DeploymentID, ControllerEpoch: index.ControllerEpoch, ControlRevision: strconv.FormatInt(authority.ControlRevision, 10), SessionID: fixtureID(2002), InstallationID: index.InstallationID, Generation: "1", PolicyRevision: "3", ReleaseSHA256: index.Payload.ReleaseSHA256, ServiceIdentitySHA256: strings.Repeat("a", 64), Resources: []string{"network.http"}, Concurrency: "1", RatePerMinute: "120", Burst: "20", RegistrationRaw: regRaw, RegistrationSignature: regSig, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+120000, 10)}
		mutate(&c)
		key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
		if _, _, err := control.SignServiceEgressSession(c, key, authority, now); err == nil {
			t.Fatalf("session with another %s was signed", name)
		}
		clear(key)
	}
}
