//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// --- fixtures --------------------------------------------------------------

type egrEnrollment struct {
	mu    sync.Mutex
	a     control.Authority
	reads atomic.Int32
}

func (e *egrEnrollment) ControllerDeployment(context.Context) (journal.Deployment, error) {
	e.reads.Add(1)
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.a
	return journal.Deployment{ControllerEnrollment: journal.ControllerEnrollment{DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: a.ControlRevision, PublicKey: a.PublicKey, Fingerprint: a.Fingerprint}}, nil
}

func (e *egrEnrollment) set(f func(*control.Authority)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(&e.a)
}

// egrListener is an in-memory net.Listener: dial hands the accepting side one end
// of a pipe, so no filesystem socket is needed.
type egrListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newEgrListener() *egrListener {
	return &egrListener{conns: make(chan net.Conn), done: make(chan struct{})}
}
func (l *egrListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *egrListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *egrListener) Addr() net.Addr { return &net.UnixAddr{Name: "egress-test", Net: "unix"} }
func (l *egrListener) dial() (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		client.Close()
		server.Close()
		return nil, net.ErrClosed
	case <-time.After(2 * time.Second):
		client.Close()
		server.Close()
		return nil, errors.New("dial timeout")
	}
}

type egrFixture struct {
	t                                *testing.T
	a                                control.Authority
	key                              ed25519.PrivateKey
	registrationRaw, registrationSig []byte
	descriptor                       []byte
	installation                     string
	epoch, release                   string
	enrollment                       *egrEnrollment
	provider                         *networkProvider
	real                             networkBackgroundExecutor
	registry                         *egressRegistry
	binds, listens                   atomic.Int32
	listenerMu                       sync.Mutex
	listeners                        map[string]*egrListener
	peerOK                           atomic.Bool
	policy                           appplatform.HTTPPolicy
	executed                         atomic.Int32
	// lastRaw is the signed command bytes the last background call was handed.
	lastRaw atomic.Value
	// bindHook, when set, runs inside every bind (a place a racing close can land).
	bindHook func(listen bool)
}

func newEgrFixture(t *testing.T) *egrFixture {
	t.Helper()
	a, _, raw, sig, descriptor, _ := registrationRequestFixture(t)
	registration, err := control.VerifyBackendRegistration(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	candidate := registration.Candidate()
	f := &egrFixture{t: t, a: a, key: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), registrationRaw: raw, registrationSig: sig, descriptor: descriptor, installation: candidate.InstallationID, epoch: candidate.ControllerEpoch, release: candidate.Payload.ReleaseSHA256, enrollment: &egrEnrollment{a: a}, listeners: map[string]*egrListener{}}
	f.peerOK.Store(true)
	f.policy = appplatform.HTTPPolicy{Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"GET", "POST"}, RequestBytes: 64, ResponseBytes: 64, Deadline: time.Second, Concurrency: 4}
	provider, err := newNetworkProvider(f.enrollment, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	f.provider = provider
	f.real = provider.background
	provider.background = egrBackground{f: f}
	ctx, cancel := context.WithCancel(context.Background())
	f.registry = newEgressRegistry(ctx, provider, f.bind)
	t.Cleanup(func() { cancel(); f.registry.close() })
	return f
}

// bind is the fixture's EgressBinder: it verifies nothing against a journal (the
// journal is covered by its own tests) and hands out an in-memory listener.
func (f *egrFixture) bind(ctx context.Context, v control.VerifiedServiceEgressSession, open EgressOpen, listen bool) (EgressListener, error) {
	f.binds.Add(1)
	if f.bindHook != nil {
		f.bindHook(listen)
	}
	if !listen {
		return EgressListener{}, nil
	}
	f.listens.Add(1)
	l := newEgrListener()
	f.listenerMu.Lock()
	f.listeners[v.Command().Generation] = l
	f.listenerMu.Unlock()
	return EgressListener{Listener: l, PeerOK: func(net.Conn) bool { return f.peerOK.Load() }}, nil
}

func (f *egrFixture) listener(generation string) *egrListener {
	f.listenerMu.Lock()
	defer f.listenerMu.Unlock()
	return f.listeners[generation]
}

// egrBackground stands in for the executor pool with the same four checks and
// the same effect as the foreground fixture. hook, when set, runs after the
// third check (the request is on the wire) and may block.
type egrBackground struct{ f *egrFixture }

type egrBackgroundHook func(ctx context.Context, c control.NetworkHTTPRequestCommand)

var egrHooks sync.Map // *egrFixture -> egrBackgroundHook

func (b egrBackground) DoBackground(ctx context.Context, raw, sig, body []byte, a control.Authority) (broker.HTTPResponse, error) {
	b.f.lastRaw.Store(raw)
	v, err := control.VerifyNetworkHTTPRequest(raw, sig, a, time.Now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(v, body) != nil {
		return broker.HTTPResponse{}, broker.ErrDenied
	}
	for i := 0; i < 4; i++ {
		if _, err := b.f.provider.current(ctx, v.Command()); err != nil {
			if i == 3 {
				return broker.HTTPResponse{}, broker.ErrUncertain
			}
			return broker.HTTPResponse{}, broker.ErrDenied
		}
		if i == 2 {
			b.f.executed.Add(1)
			if hook, ok := egrHooks.Load(b.f); ok {
				hook.(egrBackgroundHook)(ctx, v.Command())
			}
		}
	}
	return broker.HTTPResponse{Status: 429, ContentType: "application/json", Body: []byte(`{"ok":true}`), RetryAfterSeconds: 7}, nil
}

type egrOptions struct {
	resources   []string
	concurrency string
	generation  string
	lifetime    time.Duration
	mutate      func(*control.ServiceEgressSessionCommand)
}

func (f *egrFixture) sessionCommand(o egrOptions) control.ServiceEgressSessionCommand {
	if o.resources == nil {
		o.resources = []string{"network.http", "notifications.publish"}
	}
	if o.concurrency == "" {
		o.concurrency = "2"
	}
	if o.lifetime == 0 {
		o.lifetime = 2 * time.Minute
	}
	identity := sha256.Sum256([]byte("identity"))
	id := make([]byte, 16)
	_, _ = io.ReadFull(randReader{}, id)
	now := time.Now().UnixMilli()
	c := control.ServiceEgressSessionCommand{Format: control.ServiceEgressSessionFormat, DeploymentID: f.a.DeploymentID, ControllerEpoch: f.epoch, ControlRevision: strconv.FormatInt(f.a.ControlRevision, 10), SessionID: egrUUID(id), InstallationID: f.installation, Generation: "1", PolicyRevision: "1", ReleaseSHA256: f.release, ServiceIdentitySHA256: hex.EncodeToString(identity[:]), Resources: o.resources, Concurrency: o.concurrency, RatePerMinute: "120", Burst: "20", RegistrationRaw: f.registrationRaw, RegistrationSignature: f.registrationSig, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+o.lifetime.Milliseconds(), 10)}
	if o.generation != "" {
		c.Generation = o.generation
	}
	if o.mutate != nil {
		o.mutate(&c)
	}
	return c
}

type randReader struct{}

func (randReader) Read(p []byte) (int, error) { return len(p), nilFill(p) }
func nilFill(p []byte) error {
	var counter = egrCounter.Add(1)
	for i := range p {
		p[i] = byte(counter >> (uint(i%8) * 8))
	}
	return nil
}

var egrCounter atomic.Uint64

func egrUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

// portalSide is the Portal end of one open session.
type portalSide struct {
	f        *egrFixture
	t        *testing.T
	mux      *EgressMux
	command  control.ServiceEgressSessionCommand
	conn     net.Conn
	ack      byte
	openErr  chan error
	outcomes chan EgressOutcome
	notices  chan broker.AppNotice
	// answer decides the reply to a call; nil answers the default way.
	mu      sync.Mutex
	answer  func(call *EgressCall, first EgressFrame) bool
	mutate  func(*control.NetworkHTTPRequestCommand)
	noticeR func(broker.AppNotice) (byte, EgressNoticeResult)
	gate    chan struct{}
	admits  chan EgressAdmit
	// reports collects the proxy reports (doc 184 section 7) the runner sends. While holdReports is set
	// the Portal side holds each report's call open, as a slow audit write would.
	reports     chan broker.ProxyReport
	holdReports chan struct{}
}

// open sends a signed session over a fresh connection and returns the Portal
// side after the runner's acknowledgement. It returns the status of the ack.
func (f *egrFixture) open(o egrOptions) *portalSide {
	f.t.Helper()
	command := f.sessionCommand(o)
	raw, sig, err := control.SignServiceEgressSession(command, f.key, f.a, time.Now().UnixMilli())
	if err != nil {
		f.t.Fatal(err)
	}
	return f.openRaw(command, raw, sig)
}

func (f *egrFixture) openRaw(command control.ServiceEgressSessionCommand, raw, sig []byte) *portalSide {
	f.t.Helper()
	client, server := net.Pipe()
	frame, err := EncodeEgressOpen(raw, sig, f.descriptor)
	if err != nil {
		f.t.Fatal(err)
	}
	p := &portalSide{f: f, t: f.t, command: command, conn: client, openErr: make(chan error, 1), outcomes: make(chan EgressOutcome, 64), notices: make(chan broker.AppNotice, 16), admits: make(chan EgressAdmit, 16), reports: make(chan broker.ProxyReport, 64)}
	go func() {
		open, err := ReadEgressOpen(server, false)
		if err != nil {
			p.openErr <- err
			return
		}
		p.openErr <- f.registry.open(context.Background(), server, open)
	}()
	go client.Write(frame)
	ack := make([]byte, 41)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(client, ack); err != nil {
		f.t.Fatalf("no acknowledgement: %v", err)
	}
	_ = client.SetReadDeadline(time.Time{})
	digest := sha256.Sum256(frame)
	status, err := ParseEgressAck(ack, digest)
	if err != nil {
		f.t.Fatal(err)
	}
	p.ack = status
	// open returns once the session is installed (or refused for good).
	select {
	case <-p.openErr:
	case <-time.After(5 * time.Second):
		f.t.Fatal("open did not return")
	}
	if status == EgressAckOpen {
		p.mux = NewEgressMux(client, EgressPortal)
		go func() { _ = p.mux.Run(context.Background()) }()
		go p.serve()
		f.t.Cleanup(func() { p.mux.Close(EgressCloseShutdown); client.Close() })
	} else {
		client.Close()
	}
	return p
}

func (p *portalSide) serve() {
	for {
		call, err := p.mux.Accept(context.Background())
		if err != nil {
			return
		}
		go p.handle(call)
	}
}

func (p *portalSide) handle(call *EgressCall) {
	defer call.Finish()
	first, err := call.Recv(context.Background())
	if err != nil {
		return
	}
	p.mu.Lock()
	answer, mutate, noticeR, gate := p.answer, p.mutate, p.noticeR, p.gate
	p.mu.Unlock()
	if answer != nil && answer(call, first) {
		return
	}
	switch first.Type {
	case EgressFrameProxyReport:
		report, err := DecodeEgressProxyReport(first.Payload)
		if err != nil {
			p.t.Errorf("proxy report frame: %v", err)
			return
		}
		p.mu.Lock()
		hold := p.holdReports
		p.mu.Unlock()
		if hold != nil {
			<-hold
		}
		p.reports <- report
	case EgressFrameNotice:
		n, err := DecodeEgressNotice(first.Payload)
		if err != nil {
			p.t.Errorf("notice frame: %v", err)
			return
		}
		p.notices <- n
		code, result := byte(0), EgressNoticeResult{EventID: "evt-1", Created: true}
		if noticeR != nil {
			code, result = noticeR(n)
		}
		if code != 0 {
			_ = call.Send(EgressFrameRefusal, []byte{code})
			return
		}
		payload, _ := EncodeEgressNoticeResult(result)
		_ = call.Send(EgressFrameNoticeResult, payload)
	case EgressFrameAdmit:
		admit, err := DecodeEgressAdmit(first.Payload)
		if err != nil {
			p.t.Errorf("admit frame: %v", err)
			return
		}
		p.admits <- admit
		if gate != nil {
			<-gate
		}
		command := p.networkCommand(admit)
		if mutate != nil {
			mutate(&command)
		}
		raw, sig, err := control.SignNetworkHTTPRequest(command, p.f.key, p.f.a, time.Now().UnixMilli())
		if err != nil {
			// The Portal fixture could not sign what the test asked for: tell the
			// runner to refuse, which is what a real Portal does.
			_ = call.Send(EgressFrameRefusal, []byte{EgressRefusalDenied})
			return
		}
		payload, _ := EncodeEgressSigned(raw, sig)
		if err := call.Send(EgressFrameCommand, payload); err != nil {
			return
		}
		for {
			frame, err := call.Recv(context.Background())
			if err != nil {
				return
			}
			switch frame.Type {
			case EgressFrameChallenge:
				_, nonce, err := DecodeEgressChallenge(frame.Payload)
				if err != nil {
					return
				}
				proof, psig, err := control.SignNetworkHTTPCheck(command, hex.EncodeToString(nonce[:]), p.f.key, p.f.a, time.Now().UnixMilli())
				if err != nil {
					return
				}
				out, _ := EncodeEgressSigned(proof, psig)
				if call.Send(EgressFrameProof, out) != nil {
					return
				}
			case EgressFrameOutcome:
				o, err := DecodeEgressOutcome(frame.Payload)
				if err != nil {
					p.t.Errorf("outcome: %v", err)
					return
				}
				p.outcomes <- o
				return
			}
		}
	}
}

// networkCommand is the command a correct Portal signs for an admit.
func (p *portalSide) networkCommand(admit EgressAdmit) control.NetworkHTTPRequestCommand {
	policy, err := appplatform.CanonicalHTTPPolicy(p.f.policy)
	if err != nil {
		p.t.Fatal(err)
	}
	parsed, _ := appplatform.ParseHTTPPolicy(policy)
	scope := sha256.Sum256([]byte("portal-network-service/v1:" + p.command.SessionID))
	now := time.Now().UnixMilli()
	requestID := make([]byte, 16)
	_, _ = io.ReadFull(randReader{}, requestID)
	sessionExpires, _ := strconv.ParseInt(p.command.ExpiresMS, 10, 64)
	expires := min(now+20000, sessionExpires)
	return control.NetworkHTTPRequestCommand{Format: control.NetworkHTTPRequestFormat, DeploymentID: p.f.a.DeploymentID, ControllerEpoch: p.command.ControllerEpoch, ControlRevision: p.command.ControlRevision, RequestID: egrUUID(requestID), InstallationID: p.command.InstallationID, ReviewPlanID: "50000000-0000-4000-8000-000000000001", Generation: p.command.Generation, PolicyRevision: p.command.PolicyRevision, ReleaseSHA256: p.command.ReleaseSHA256, ActorScope: hex.EncodeToString(scope[:]), PolicyRaw: policy, PolicySHA256: parsed.SHA256, Method: admit.Method, URL: admit.URL, Accept: admit.Accept, ContentType: admit.ContentType, BodyBytes: strconv.Itoa(admit.BodyBytes), BodySHA256: admit.BodySHA256, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(expires, 10)}
}

// --- app side -----------------------------------------------------------------

type egrReply struct {
	status int
	header http.Header
	body   string
}

func (f *egrFixture) request(generation, method, path, contentType, body string) (egrReply, error) {
	l := f.listener(generation)
	if l == nil {
		return egrReply{}, errors.New("no listener")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return l.dial() }, DisableKeepAlives: true}, Timeout: 30 * time.Second}
	req, _ := http.NewRequest(method, "http://broker"+path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return egrReply{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return egrReply{status: resp.StatusCode, header: resp.Header, body: string(raw)}, nil
}

const egrGetRequest = `{"requestId":"req-1","method":"GET","url":"https://api.example.com/v1/x","accept":"application/json","contentType":"","bodyBase64":""}`

func (f *egrFixture) network(t *testing.T, body string) egrReply {
	t.Helper()
	r, err := f.request("1", "POST", "/v1/network/http", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func egrError(t *testing.T, r egrReply, status int, code string) {
	t.Helper()
	if r.status != status || r.body != `{"error":"`+code+`","retryable":false}` {
		t.Fatalf("want %d %s, got %d %s", status, code, r.status, r.body)
	}
}

// --- tests -----------------------------------------------------------------------

func TestServiceEgressNoSessionReturns503(t *testing.T) {
	f := newEgrFixture(t)
	// Before the first session there is no socket at all: connect fails.
	if _, err := f.request("1", "GET", "/v1/status", "", ""); err == nil {
		t.Fatal("a listener exists before any session")
	}
	p := f.open(egrOptions{})
	if p.ack != EgressAckOpen {
		t.Fatal("session refused")
	}
	if r, err := f.request("1", "GET", "/v1/status", "", ""); err != nil || r.status != 200 || r.body != `{"egress":"available","resources":["network.http","notifications.publish"]}` || r.header.Get("Content-Type") != "application/json" || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%+v %v", r, err)
	}
	// A session that ends leaves the listener, which now answers 503.
	p.mux.Close(EgressCloseExpired)
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := f.request("1", "GET", "/v1/status", "", "")
		if err == nil && r.body == `{"egress":"unavailable","resources":[]}` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never became unavailable: %+v %v", r, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	egrError(t, f.network(t, egrGetRequest), 503, "unavailable")
	r, _ := f.request("1", "POST", "/v1/notifications", "application/json", `{"occurrenceId":"o","title":"t","message":"m","severity":"info"}`)
	egrError(t, r, 503, "unavailable")
	// Anything else on the socket is invalid, and never reaches Portal.
	for _, tc := range [][3]string{{"GET", "/v1/network/http", ""}, {"POST", "/v1/status", "application/json"}, {"POST", "/v2/network/http", "application/json"}, {"POST", "/v1/network/http?x=1", "application/json"}, {"DELETE", "/", ""}, {"POST", "/v1/network/http", "text/plain"}} {
		r, err := f.request("1", tc[0], tc[1], tc[2], egrGetRequest)
		if err != nil {
			t.Fatal(err)
		}
		egrError(t, r, 400, "invalid")
	}
	if f.executed.Load() != 0 {
		t.Fatal("a refused request executed")
	}
}

func TestServiceEgressRateAndConcurrency(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{concurrency: "2"})
	gate := make(chan struct{})
	p.mu.Lock()
	p.gate = gate
	p.mu.Unlock()
	var wg sync.WaitGroup
	results := make(chan egrReply, 3)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.network(t, egrGetRequest) }()
	}
	// Both are admitted and wait for Portal.
	for i := 0; i < 2; i++ {
		select {
		case <-p.admits:
		case <-time.After(5 * time.Second):
			t.Fatal("call not admitted")
		}
	}
	r := f.network(t, egrGetRequest)
	egrError(t, r, 429, "busy")
	if r.header.Get("Retry-After") != "1" {
		t.Fatal("busy without Retry-After")
	}
	close(gate)
	wg.Wait()
	close(results)
	for r := range results {
		if r.status != 200 || r.body != `{"status":429,"contentType":"application/json","bodyBase64":"eyJvayI6dHJ1ZX0=","retryAfterSeconds":7}` {
			t.Fatalf("%+v", r)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case o := <-p.outcomes:
			if o.Class != EgressOutcomeOK || o.Status != 429 || o.ResponseBytes != 11 || o.RetryAfter != 7 {
				t.Fatalf("%+v", o)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("outcome not reported")
		}
	}
	// The rate bucket is the installation's: 20 calls in a burst, the 21st is busy,
	// and notices draw on the same bucket. A frozen clock makes this exact.
	frozen := func() time.Time { return time.Unix(1_800_000_000, 0) }
	f.registry.mu.Lock()
	inst := f.registry.installs[f.installation]
	f.registry.mu.Unlock()
	inst.mu.Lock()
	inst.bucket = broker.NewTokenBucket(120, 20, frozen)
	inst.mu.Unlock()
	p.mu.Lock()
	p.gate = nil
	p.answer = func(call *EgressCall, first EgressFrame) bool {
		// Refuse at once: every call still consumes a token.
		_ = call.Send(EgressFrameRefusal, []byte{EgressRefusalDenied})
		return true
	}
	p.mu.Unlock()
	for i := 0; i < 19; i++ {
		egrError(t, f.network(t, egrGetRequest), 403, "denied")
	}
	r, _ = f.request("1", "POST", "/v1/notifications", "application/json", `{"occurrenceId":"o","title":"t","message":"m","severity":"info"}`)
	egrError(t, r, 403, "denied") // the 20th token went to a notice
	egrError(t, f.network(t, egrGetRequest), 429, "busy")
	r, _ = f.request("1", "POST", "/v1/notifications", "application/json", `{"occurrenceId":"o","title":"t","message":"m","severity":"info"}`)
	egrError(t, r, 429, "busy")
}

func TestServiceEgressCommandForOtherInstallationRefused(t *testing.T) {
	other := "40000000-0000-4000-8000-0000000000ff"
	for name, mutate := range map[string]func(*control.NetworkHTTPRequestCommand){
		"installation": func(c *control.NetworkHTTPRequestCommand) { c.InstallationID = other },
		"generation":   func(c *control.NetworkHTTPRequestCommand) { c.Generation = "2" },
		"policy rev":   func(c *control.NetworkHTTPRequestCommand) { c.PolicyRevision = "2" },
		"release":      func(c *control.NetworkHTTPRequestCommand) { c.ReleaseSHA256 = strings.Repeat("c", 64) },
		"actor scope (a viewer)": func(c *control.NetworkHTTPRequestCommand) {
			c.ActorScope = strings.Repeat("d", 64)
		},
		"credential": func(c *control.NetworkHTTPRequestCommand) {
			c.CredentialName, c.CredentialHeader, c.CredentialValue = "upstream", "Authorization", []byte("Bearer secret")
		},
		"other url":    func(c *control.NetworkHTTPRequestCommand) { c.URL = "https://api.example.com/v1/other" },
		"other method": func(c *control.NetworkHTTPRequestCommand) { c.Method = "POST" },
		"other accept": func(c *control.NetworkHTTPRequestCommand) { c.Accept = "text/html" },
		"other body": func(c *control.NetworkHTTPRequestCommand) {
			c.BodyBytes = "3"
			c.BodySHA256 = strings.Repeat("e", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEgrFixture(t)
			p := f.open(egrOptions{})
			p.mu.Lock()
			p.mutate = mutate
			p.mu.Unlock()
			egrError(t, f.network(t, egrGetRequest), 403, "denied")
			if f.executed.Load() != 0 {
				t.Fatal("a command for another tuple executed")
			}
			select {
			case o := <-p.outcomes:
				if o.Class != EgressOutcomeDenied {
					t.Fatalf("outcome %+v", o)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("refusal not reported")
			}
			// The session survives: the next, correct, call works.
			p.mu.Lock()
			p.mutate = nil
			p.mu.Unlock()
			if r := f.network(t, egrGetRequest); r.status != 200 {
				t.Fatalf("session did not survive: %+v", r)
			}
		})
	}
	t.Run("replayed command", func(t *testing.T) {
		f := newEgrFixture(t)
		p := f.open(egrOptions{})
		var saved atomic.Value
		p.mu.Lock()
		p.mutate = func(c *control.NetworkHTTPRequestCommand) {
			if previous, ok := saved.Load().(control.NetworkHTTPRequestCommand); ok {
				*c = previous
				return
			}
			saved.Store(*c)
		}
		p.mu.Unlock()
		if r := f.network(t, egrGetRequest); r.status != 200 {
			t.Fatalf("%+v", r)
		}
		egrError(t, f.network(t, egrGetRequest), 403, "denied")
	})
}

func TestServiceEgressSessionCloseMakesInFlightUncertain(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	egrHooks.Store(f, egrBackgroundHook(func(ctx context.Context, c control.NetworkHTTPRequestCommand) {
		once.Do(func() { close(reached) })
		<-resume
	}))
	defer egrHooks.Delete(f)
	result := make(chan egrReply, 1)
	go func() { result <- f.network(t, egrGetRequest) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("call never reached the wire")
	}
	// Portal ends the session while the request is in flight: the release check
	// cannot be answered, so the outcome is unknown and no body is released.
	p.mux.Close(EgressCloseDrift)
	time.Sleep(50 * time.Millisecond)
	close(resume)
	select {
	case r := <-result:
		egrError(t, r, 502, "uncertain")
		if strings.Contains(r.body, "bodyBase64") {
			t.Fatal("a body was released")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call did not finish")
	}
	// A call waiting for its command when the session closes is not uncertain:
	// nothing was ever sent.
	g := newEgrFixture(t)
	q := g.open(egrOptions{})
	block := make(chan struct{})
	q.mu.Lock()
	q.gate = block
	q.mu.Unlock()
	pending := make(chan egrReply, 1)
	go func() { pending <- g.network(t, egrGetRequest) }()
	<-q.admits
	q.mux.Close(EgressCloseShutdown)
	select {
	case r := <-pending:
		egrError(t, r, 503, "unavailable")
	case <-time.After(10 * time.Second):
		t.Fatal("pending call did not finish")
	}
	close(block)
}

func TestServiceEgressEpochChangeClosesSessions(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	f.registry.sweep()
	select {
	case <-p.mux.Done():
		t.Fatal("a healthy session was swept")
	case <-time.After(100 * time.Millisecond):
	}
	f.enrollment.set(func(a *control.Authority) { a.ControlRevision++ })
	f.registry.sweep()
	select {
	case <-p.mux.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session survived an enrollment change")
	}
	if p.mux.PeerReason() != EgressCloseEpoch {
		t.Fatalf("close reason %d", p.mux.PeerReason())
	}
	egrError(t, f.network(t, egrGetRequest), 503, "unavailable")
	// The old session cannot be opened again: it is no longer verified.
	again := f.open(egrOptions{})
	if again.ack != EgressAckRefused {
		t.Fatal("a session for the old epoch opened")
	}
}

func TestServiceEgressNoticeRoundTrip(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	const body = `{"occurrenceId":"probe-1","title":"Monitor down","message":"api failed","severity":"error"}`
	r, err := f.request("1", "POST", "/v1/notifications", "application/json", body)
	if err != nil || r.status != 200 || r.body != `{"eventId":"evt-1","created":true}` {
		t.Fatalf("%+v %v", r, err)
	}
	select {
	case n := <-p.notices:
		if string(n.JSON()) != body {
			t.Fatalf("notice changed on the wire: %s", n.JSON())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notice frame")
	}
	for name, tc := range map[string]struct {
		code   byte
		status int
		app    string
	}{"conflict": {EgressRefusalConflict, 409, "conflict"}, "capacity": {EgressRefusalCapacity, 429, "capacity"}, "invalid": {EgressRefusalInvalid, 400, "invalid"}, "denied": {EgressRefusalDenied, 403, "denied"}, "unavailable": {EgressRefusalUnavailable, 503, "unavailable"}, "busy": {EgressRefusalBusy, 429, "busy"}} {
		p.mu.Lock()
		p.noticeR = func(broker.AppNotice) (byte, EgressNoticeResult) { return tc.code, EgressNoticeResult{} }
		p.mu.Unlock()
		r, err := f.request("1", "POST", "/v1/notifications", "application/json", body)
		if err != nil {
			t.Fatal(name, err)
		}
		egrError(t, r, tc.status, tc.app)
		<-p.notices
	}
	// Idempotent replay is Portal's answer (created:false); the runner relays it.
	p.mu.Lock()
	p.noticeR = func(broker.AppNotice) (byte, EgressNoticeResult) {
		return 0, EgressNoticeResult{EventID: "evt-1", Created: false}
	}
	p.mu.Unlock()
	if r, _ := f.request("1", "POST", "/v1/notifications", "application/json", body); r.body != `{"eventId":"evt-1","created":false}` {
		t.Fatal(r.body)
	}
	<-p.notices
	// Open input never reaches Portal.
	for _, bad := range []string{`{}`, strings.Replace(body, `"severity":"error"`, `"severity":"error","namespace":"app.other"`, 1), strings.Replace(body, `"title":"Monitor down",`, ``, 1), body + body, ``} {
		r, _ := f.request("1", "POST", "/v1/notifications", "application/json", bad)
		egrError(t, r, 400, "invalid")
	}
	select {
	case n := <-p.notices:
		t.Fatalf("an invalid notice reached Portal: %+v", n)
	case <-time.After(100 * time.Millisecond):
	}
	// A session without notifications.publish refuses the route, and network calls
	// need network.http.
	only := newEgrFixture(t)
	only.open(egrOptions{resources: []string{"network.http"}})
	r, _ = only.request("1", "POST", "/v1/notifications", "application/json", body)
	egrError(t, r, 403, "denied")
	notices := newEgrFixture(t)
	notices.open(egrOptions{resources: []string{"notifications.publish"}})
	r, _ = notices.request("1", "POST", "/v1/network/http", "application/json", egrGetRequest)
	egrError(t, r, 403, "denied")
}

func TestServiceEgressPeerUIDEnforced(t *testing.T) {
	f := newEgrFixture(t)
	f.open(egrOptions{})
	f.peerOK.Store(false)
	conn, err := f.listener("1").dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("GET /v1/status HTTP/1.1\r\nHost: b\r\n\r\n"))
	if n, err := conn.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("a peer of the wrong uid was served: %d %v", n, err)
	}
	f.peerOK.Store(true)
	if r, err := f.request("1", "GET", "/v1/status", "", ""); err != nil || r.status != 200 {
		t.Fatalf("%+v %v", r, err)
	}
	// The real check compares SO_PEERCRED: a socket from this process matches its
	// own uid and no other.
	dir := t.TempDir()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: dir + "/b.sock", Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	client, err := net.Dial("unix", dir+"/b.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()
	uid := uint32(os.Getuid())
	if !egressPeerUID(uid)(server) || egressPeerUID(uid+1)(server) || egressPeerUID(uid)(client.(*net.UnixConn)) != true {
		t.Fatal("SO_PEERCRED comparison wrong")
	}
	if egressPeerUID(uid)(&net.TCPConn{}) {
		t.Fatal("a non-unix connection passed")
	}
}

func TestServiceEgressReplacementOnlyAfterVerification(t *testing.T) {
	f := newEgrFixture(t)
	first := f.open(egrOptions{})
	status := func() string { r, _ := f.request("1", "GET", "/v1/status", "", ""); return r.body }
	// A tampered session is refused and the current one is untouched.
	command := f.sessionCommand(egrOptions{})
	raw, sig, err := control.SignServiceEgressSession(command, f.key, f.a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(sig)
	bad[0] ^= 1
	if p := f.openRaw(command, raw, bad); p.ack != EgressAckRefused {
		t.Fatal("tampered session accepted")
	}
	select {
	case <-first.mux.Done():
		t.Fatal("a refused replacement closed the current session")
	case <-time.After(100 * time.Millisecond):
	}
	if !strings.Contains(status(), "available") || strings.Contains(status(), "unavailable") {
		t.Fatal(status())
	}
	listens := f.listens.Load()
	// A verified one replaces it: same listener, the old session drains and ends.
	second := f.open(egrOptions{})
	if second.ack != EgressAckOpen || f.listens.Load() != listens {
		t.Fatal("renewal rebound the socket")
	}
	select {
	case <-first.mux.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced session was not closed")
	}
	if first.mux.PeerReason() != EgressCloseRenewed {
		t.Fatalf("reason %d", first.mux.PeerReason())
	}
	if r := f.network(t, egrGetRequest); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	select {
	case <-second.admits:
	case <-time.After(5 * time.Second):
		t.Fatal("the new session did not carry the call")
	}
	// An older generation is refused; a newer one rebinds.
	if older := f.open(egrOptions{generation: "1"}); older.ack != EgressAckOpen {
		t.Fatal("same generation refused")
	}
	// Open rights are bound by the controller epoch.
	wrongEpoch := f.sessionCommand(egrOptions{mutate: func(c *control.ServiceEgressSessionCommand) {
		c.ControllerEpoch = "20000000-0000-4000-8000-0000000000aa"
	}})
	if _, _, err := control.SignServiceEgressSession(wrongEpoch, f.key, f.a, time.Now().UnixMilli()); err == nil {
		t.Fatal("signed a session for another epoch")
	}
}

// --- DNS and executor classes through the real pool ---------------------------------

// fakeDNS answers every A query with ip and every other query with no answer.
func fakeDNS(t *testing.T, ip net.IP, queries *atomic.Int32) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			queries.Add(1)
			q := buf[12:n]
			end := 0
			for end < len(q) && q[end] != 0 {
				end += int(q[end]) + 1
			}
			question := q[:end+5]
			qtype := binary.BigEndian.Uint16(q[end+1:])
			resp := append([]byte{buf[0], buf[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}, question...)
			if qtype == 1 {
				resp[7] = 1
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
				resp = append(resp, ip.To4()...)
			}
			_, _ = conn.WriteTo(resp, addr)
		}
	}()
	return conn.LocalAddr().String()
}

func TestServiceEgressDnsAndRedirectThroughBroker(t *testing.T) {
	// The real pool and executor serve the call, with the OS resolver pointed at a
	// fake server that answers a private address: the answer is refused before any
	// dial, so the app sees denied, not unavailable and not a timeout.
	var queries atomic.Int32
	server := fakeDNS(t, net.ParseIP("10.1.2.3"), &queries)
	original := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", server)
	}}
	defer func() { net.DefaultResolver = original }()
	f := newEgrFixture(t)
	f.provider.background = f.real
	p := f.open(egrOptions{})
	egrError(t, f.network(t, egrGetRequest), 403, "denied")
	if queries.Load() == 0 {
		t.Fatal("the executor did not resolve through the OS resolver")
	}
	select {
	case o := <-p.outcomes:
		if o.Class != EgressOutcomeDenied || o.Status != 0 || o.ResponseBytes != 0 || o.RetryAfter != -1 {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outcome not reported")
	}
	// A redirect, a compressed body or an oversize body is uncertain in the
	// executor (see the broker tests, which run the same code); the egress path must
	// carry that class to the app with no body.
	g := newEgrFixture(t)
	g.provider.background = egrUncertain{}
	q := g.open(egrOptions{})
	r := g.network(t, egrGetRequest)
	egrError(t, r, 502, "uncertain")
	select {
	case o := <-q.outcomes:
		if o.Class != EgressOutcomeUncertain {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outcome not reported")
	}
}

type egrUncertain struct{}

func (egrUncertain) DoBackground(context.Context, []byte, []byte, []byte, control.Authority) (broker.HTTPResponse, error) {
	return broker.HTTPResponse{Status: 307, Body: []byte("private")}, broker.ErrUncertain
}

// A command may not outlive the session it was signed for, whatever else it says.
func TestServiceEgressCommandMustNotOutliveSession(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	f.registry.mu.Lock()
	session := f.registry.installs[f.installation].current
	f.registry.mu.Unlock()
	admit := EgressAdmit{Method: "GET", URL: "https://api.example.com/v1/x", BodySHA256: hex.EncodeToString(sha256Sum(nil))}
	command := p.networkCommand(admit)
	body := []byte(nil)
	raw, sig, err := control.SignNetworkHTTPRequest(command, f.key, f.a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.VerifyNetworkHTTPRequest(raw, sig, f.a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.checkCommand(verified.Command(), admit, body); err != nil {
		t.Fatalf("a correct command was refused: %v", err)
	}
	late := &egressSession{command: session.command, expires: time.UnixMilli(mustInt(t, command.ExpiresMS) - 1)}
	if err := late.checkCommand(verified.Command(), admit, body); err == nil {
		t.Fatal("a command that outlives its session was accepted")
	}
}

func sha256Sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A call that finds its session already closed sent nothing: it is unavailable,
// never uncertain.
func TestServiceEgressClosedSessionAtNewCallIsUnavailable(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	f.registry.mu.Lock()
	in := f.registry.installs[f.installation]
	session := in.current
	// Holding the registry lock keeps the session attached while its mux ends, so
	// the call is admitted and only NewCall sees the closed session.
	session.mux.fail(ErrEgressClosed)
	for name, tc := range map[string][3]string{
		"network": {"/v1/network/http", egrGetRequest, ""},
		"notice":  {"/v1/notifications", `{"occurrenceId":"o","title":"t","message":"m","severity":"info"}`, ""},
	} {
		req := httptest.NewRequest("POST", tc[0], strings.NewReader(tc[1]))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		in.ServeHTTP(rec, req)
		if rec.Code != 503 || rec.Body.String() != `{"error":"unavailable","retryable":false}` {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	f.registry.mu.Unlock()
	_ = p
}

func TestServiceEgressAdmitCanonicalFormIsNotHTMLEscaped(t *testing.T) {
	sum := sha256.Sum256(nil)
	url := "https://api.example.com/v1/x?" + strings.Repeat("a=1&b=<2>&", 140)
	admit := EgressAdmit{Method: "GET", URL: url, BodySHA256: hexOf(sum[:])}
	payload, err := EncodeEgressAdmit(admit)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(`\u00`)) || len(payload) > egressAdmitMaxBytes {
		t.Fatalf("escaped or oversized: %d bytes", len(payload))
	}
	back, err := DecodeEgressAdmit(payload)
	if err != nil || back != admit {
		t.Fatalf("round trip: %v", err)
	}
	// A URL that cannot fit a frame in any encoding is refused at encode time.
	huge := admit
	huge.URL = "https://api.example.com/" + strings.Repeat(`"`, 4050)
	if _, err := EncodeEgressAdmit(huge); err == nil {
		t.Fatal("an admit over the frame bound was encoded")
	}
}

func TestServiceEgressUnrepresentableInputIsInvalidAndSpendsNoToken(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	// An ampersand-heavy URL and message both fit their frames once they are not
	// HTML-escaped.
	amp := `{"requestId":"r","method":"GET","url":"https://api.example.com/v1/x?` + strings.Repeat("&", 3000) + `","accept":"","contentType":"","bodyBase64":""}`
	if r := f.network(t, amp); r.status != 200 {
		t.Fatalf("ampersands: %d %s", r.status, r.body)
	}
	<-p.admits
	message := strings.Repeat("&<", 7000)
	notice := `{"occurrenceId":"o","title":"t","message":"` + message + `","severity":"info"}`
	r, err := f.request("1", "POST", "/v1/notifications", "application/json", notice)
	if err != nil || r.status != 200 {
		t.Fatalf("notice with markup: %+v %v", r, err)
	}
	select {
	case n := <-p.notices:
		if n.Message != message {
			t.Fatal("message changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notice")
	}
	// Input that no frame can carry is invalid, before a rate token is taken: far
	// more of them than the burst leave the budget untouched.
	quotes := `{"requestId":"r","method":"GET","url":"https://api.example.com/` + strings.Repeat(`\"`, 4050) + `","accept":"","contentType":"","bodyBase64":""}`
	separators := `{"occurrenceId":"o","title":"t","message":"` + strings.Repeat(" ", 5400) + `","severity":"info"}`
	for i := 0; i < 30; i++ {
		egrError(t, f.network(t, quotes), 400, "invalid")
		r, _ := f.request("1", "POST", "/v1/notifications", "application/json", separators)
		egrError(t, r, 400, "invalid")
	}
	if r := f.network(t, egrGetRequest); r.status != 200 {
		t.Fatalf("budget spent by invalid input: %d %s", r.status, r.body)
	}
}

// Renewing the session of an installation does not grow the steady-state count,
// so a renewal passes the bound (a new installation does not: see the next test).
func TestServiceEgressBoundDoesNotBlockRenewal(t *testing.T) {
	f := newEgrFixture(t)
	f.registry.maxSessions = 1
	first := f.open(egrOptions{})
	if first.ack != EgressAckOpen {
		t.Fatal("first session refused")
	}
	renewed := f.open(egrOptions{})
	if renewed.ack != EgressAckOpen {
		t.Fatal("a renewal was refused at the session bound")
	}
}

// An open that loses a race with the installation's disabled close is refused;
// it is never acknowledged as open and then dropped.
func TestServiceEgressOpenRacingDisabledCloseIsRefused(t *testing.T) {
	f := newEgrFixture(t)
	first := f.open(egrOptions{})
	if first.ack != EgressAckOpen {
		t.Fatal("first session refused")
	}
	f.bindHook = func(listen bool) {
		if listen {
			return
		}
		f.registry.mu.Lock()
		in := f.registry.installs[f.installation]
		f.registry.mu.Unlock()
		f.registry.closeInstall(in, EgressCloseDisabled)
	}
	if second := f.open(egrOptions{}); second.ack != EgressAckRefused {
		t.Fatalf("ack %d for an open that lost to a disabled close", second.ack)
	}
}

// An idle registry does not read the journal's enrollment on every sweep.
func TestServiceEgressSweepWithoutSessionsReadsNothing(t *testing.T) {
	f := newEgrFixture(t)
	before := f.enrollment.reads.Load()
	for i := 0; i < 5; i++ {
		f.registry.sweep()
	}
	if got := f.enrollment.reads.Load(); got != before {
		t.Fatalf("%d enrollment reads with no sessions", got-before)
	}
	p := f.open(egrOptions{})
	before = f.enrollment.reads.Load()
	f.registry.sweep()
	if f.enrollment.reads.Load() == before {
		t.Fatal("a live session was swept without checking the enrollment")
	}
	_ = p
}

// Closing a server whose Serve never ran still ends the registry and its sweep.
func TestServerCloseWithoutServeStopsEgressRegistry(t *testing.T) {
	f := newEgrFixture(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "s.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{listener: listener, egress: f.registry}
	server.Close()
	done := make(chan struct{})
	go func() { f.registry.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep goroutine outlived a closed server")
	}
}

// The bound counts installations that hold a live session. One whose sessions
// all ended is a new installation when it opens again, so it cannot lift the
// registry past the bound.
func TestServiceEgressBoundCountsLiveInstallationsOnly(t *testing.T) {
	f := newEgrFixture(t)
	f.registry.maxSessions = 1
	p := f.open(egrOptions{})
	if p.ack != EgressAckOpen {
		t.Fatal("first session refused")
	}
	p.mux.Close(EgressCloseExpired)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.registry.mu.Lock()
		in := f.registry.installs[f.installation]
		in.mu.Lock()
		gone := in.current == nil
		in.mu.Unlock()
		f.registry.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session never detached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Another installation takes the one live slot.
	other := &egressInstall{id: "other", current: &egressSession{}, sessions: map[*egressSession]struct{}{}}
	f.registry.mu.Lock()
	f.registry.installs["other"] = other
	f.registry.mu.Unlock()
	again := f.open(egrOptions{})
	f.registry.mu.Lock()
	delete(f.registry.installs, "other")
	f.registry.mu.Unlock()
	if again.ack != EgressAckRefused {
		t.Fatal("an installation with no live session reopened past the bound")
	}
}
