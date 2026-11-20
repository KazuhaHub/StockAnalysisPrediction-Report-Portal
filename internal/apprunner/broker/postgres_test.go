package broker

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	canonical, _, err := appplatform.CanonicalPostgresCA(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, pem: canonical}
}

func (ca testCA) serverCert(t *testing.T, dns string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: dns}, DNSNames: []string{dns}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakePG is a minimal PostgreSQL backend: TLS, SCRAM-SHA-256 (or md5, or a stall), and one fixed
// answer to a simple query. It records what it was sent.
type fakePG struct {
	t        *testing.T
	listener net.Listener
	tls      *tls.Config
	password string
	mode     string // "scram", "md5", "stall", "notls"
	release  chan struct{}

	mu           sync.Mutex
	accepted     int
	statements   []string
	startup      map[string]string
	passwordSeen bool
}

func newFakePG(t *testing.T, mode, password string, config *tls.Config) *fakePG {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakePG{t: t, listener: l, tls: config, password: password, mode: mode, release: make(chan struct{})}
	t.Cleanup(func() {
		close(s.release)
		l.Close()
	})
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.accepted++
			s.mu.Unlock()
			go s.handle(conn)
		}
	}()
	return s
}

func (s *fakePG) addr() string { return s.listener.Addr().String() }

func (s *fakePG) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

func (s *fakePG) handle(conn net.Conn) {
	defer conn.Close()
	be := pgproto3.NewBackend(conn, conn)
	msg, err := be.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := msg.(*pgproto3.SSLRequest); ok {
		if s.mode == "notls" {
			_, _ = conn.Write([]byte{'N'})
			return
		}
		_, _ = conn.Write([]byte{'S'})
		tconn := tls.Server(conn, s.tls)
		if tconn.Handshake() != nil {
			return
		}
		conn = tconn
		be = pgproto3.NewBackend(tconn, tconn)
		if msg, err = be.ReceiveStartupMessage(); err != nil {
			return
		}
	}
	startup, ok := msg.(*pgproto3.StartupMessage)
	if !ok {
		return
	}
	s.mu.Lock()
	s.startup = map[string]string{}
	for k, v := range startup.Parameters {
		s.startup[k] = v
	}
	s.mu.Unlock()
	switch s.mode {
	case "stall":
		<-s.release
		return
	case "md5":
		be.Send(&pgproto3.AuthenticationMD5Password{Salt: [4]byte{1, 2, 3, 4}})
		_ = be.Flush()
		_ = be.SetAuthType(pgproto3.AuthTypeMD5Password)
		_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		if m, err := be.Receive(); err == nil {
			if _, ok := m.(*pgproto3.PasswordMessage); ok {
				s.mu.Lock()
				s.passwordSeen = true
				s.mu.Unlock()
			}
		}
		return
	}
	if !s.scram(be) {
		return
	}
	be.Send(&pgproto3.AuthenticationOk{})
	be.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "16.0"})
	be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{1, 2, 3, 4}})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if be.Flush() != nil {
		return
	}
	for {
		m, err := be.Receive()
		if err != nil {
			return
		}
		switch m := m.(type) {
		case *pgproto3.Query:
			s.mu.Lock()
			s.statements = append(s.statements, m.String)
			s.mu.Unlock()
			be.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("?column?"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
			be.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
			be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if be.Flush() != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		default:
			return
		}
	}
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func (s *fakePG) scram(be *pgproto3.Backend) bool {
	be.Send(&pgproto3.AuthenticationSASL{AuthMechanisms: []string{"SCRAM-SHA-256"}})
	if be.Flush() != nil || be.SetAuthType(pgproto3.AuthTypeSASL) != nil {
		return false
	}
	m, err := be.Receive()
	first, ok := m.(*pgproto3.SASLInitialResponse)
	if err != nil || !ok {
		return false
	}
	clientFirst := string(first.Data)
	parts := strings.SplitN(clientFirst, ",", 3)
	if len(parts) != 3 {
		return false
	}
	gs2, bare := parts[0]+","+parts[1]+",", parts[2]
	var cnonce string
	for _, attr := range strings.Split(bare, ",") {
		if v, ok := strings.CutPrefix(attr, "r="); ok {
			cnonce = v
		}
	}
	salt := []byte("fixture-salt-123")
	serverFirst := "r=" + cnonce + "server-nonce,s=" + base64.StdEncoding.EncodeToString(salt) + ",i=4096"
	be.Send(&pgproto3.AuthenticationSASLContinue{Data: []byte(serverFirst)})
	if be.Flush() != nil || be.SetAuthType(pgproto3.AuthTypeSASLContinue) != nil {
		return false
	}
	m, err = be.Receive()
	final, ok := m.(*pgproto3.SASLResponse)
	if err != nil || !ok {
		return false
	}
	clientFinal := string(final.Data)
	at := strings.LastIndex(clientFinal, ",p=")
	if at < 0 {
		return false
	}
	withoutProof := clientFinal[:at]
	proof, err := base64.StdEncoding.DecodeString(clientFinal[at+3:])
	if err != nil || !strings.HasPrefix(withoutProof, "c="+base64.StdEncoding.EncodeToString([]byte(gs2))+",") {
		return false
	}
	salted, err := pbkdf2.Key(sha256.New, s.password, salt, 4096, 32)
	if err != nil {
		return false
	}
	clientKey := hmacSHA256(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	authMessage := bare + "," + serverFirst + "," + withoutProof
	signature := hmacSHA256(stored[:], authMessage)
	expected := make([]byte, len(clientKey))
	for i := range clientKey {
		expected[i] = clientKey[i] ^ signature[i]
	}
	if !hmac.Equal(expected, proof) {
		be.Send(&pgproto3.ErrorResponse{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: "28P01", Message: "password authentication failed"})
		_ = be.Flush()
		return false
	}
	serverKey := hmacSHA256(salted, "Server Key")
	be.Send(&pgproto3.AuthenticationSASLFinal{Data: []byte("v=" + base64.StdEncoding.EncodeToString(hmacSHA256(serverKey, authMessage)))})
	return true
}

type probeFixture struct {
	probe *PostgresProbe
	auth  control.Authority
	key   ed25519.PrivateKey
	ca    testCA
}

const (
	probeHost         = "db.example.com"
	probeInstallation = "40000000-0000-4000-8000-000000000001"
)

func newProbeFixture(t *testing.T, options ...NetworkPoolOption) *probeFixture {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(public)
	f := &probeFixture{key: key, ca: newTestCA(t, "Reviewed Test CA"), auth: control.Authority{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(fingerprint[:])}}
	pool, err := NewNetworkPool(4, 2, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) {
		return f.auth, nil
	}, options...)
	if err != nil {
		t.Fatal(err)
	}
	f.probe, err = NewPostgresProbe(pool)
	if err != nil {
		t.Fatal(err)
	}
	f.probe.interfaces = func() ([]hostnet.Interface, error) { return nil, nil }
	f.probe.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	return f
}

// serve points the probe's dial at a fake server, whatever address it was handed.
func (f *probeFixture) serve(addr string, dials *int) {
	f.probe.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		if dials != nil {
			*dials++
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
}

func (f *probeFixture) policy(t *testing.T, target appplatform.PostgresTarget, deadline time.Duration, concurrency int, privateSHA string) []byte {
	t.Helper()
	raw, err := appplatform.CanonicalPostgresPolicy(appplatform.PostgresPolicy{Targets: []appplatform.PostgresTarget{target}, Deadline: deadline, Concurrency: concurrency, PrivateNetworksSHA256: privateSHA})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *probeFixture) command(t *testing.T, policy []byte, target, username, password string, privateSHA string) ([]byte, []byte) {
	t.Helper()
	parsed, err := appplatform.ParsePostgresPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	c := control.PostgresProbeCommand{Format: control.PostgresProbeCommandFormat, DeploymentID: f.auth.DeploymentID, ControllerEpoch: f.auth.ControllerEpoch, ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: probeInstallation, Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: parsed.SHA256, TargetID: target, PrivateNetworksSHA256: privateSHA, CredentialName: "orders-probe", Username: []byte(username), Password: []byte(password), IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+30000, 10)}
	raw, sig, err := control.SignPostgresProbeCommand(c, f.key, f.auth, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func probePublicTarget(ca testCA) appplatform.PostgresTarget {
	return appplatform.PostgresTarget{ID: "orders", Host: probeHost, Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPublic, CAPEM: ca.pem}
}

func allow(context.Context) error { return nil }

func TestPostgresProbeOutcomes(t *testing.T) {
	good := "correct horse"
	cases := []struct {
		name     string
		mode     string
		password string
		wrongCA  bool
		deadline time.Duration
		want     string
	}{
		{"reachable", "scram", good, false, 5 * time.Second, PostgresReachable},
		{"wrong password", "scram", "another password", false, 5 * time.Second, PostgresAuthFailed},
		{"md5 offered", "md5", good, false, 5 * time.Second, PostgresAuthFailed},
		{"certificate from another authority", "scram", good, true, 5 * time.Second, PostgresTLSFailed},
		{"server without TLS", "notls", good, false, 5 * time.Second, PostgresTLSFailed},
		{"stall after startup", "stall", good, false, 400 * time.Millisecond, PostgresTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newProbeFixture(t)
			signer := f.ca
			if tc.wrongCA {
				signer = newTestCA(t, "Unreviewed CA")
			}
			server := newFakePG(t, tc.mode, tc.password, &tls.Config{Certificates: []tls.Certificate{signer.serverCert(t, probeHost)}})
			f.serve(server.addr(), nil)
			policy := f.policy(t, probePublicTarget(f.ca), tc.deadline, 2, "")
			raw, sig := f.command(t, policy, "orders", "status_probe", good, "")
			checks := 0
			started := time.Now()
			result, err := f.probe.Run(context.Background(), raw, sig, f.auth, func(context.Context) error { checks++; return nil })
			if err != nil || result.Outcome != tc.want {
				t.Fatalf("outcome %q, %v; want %q", result.Outcome, err, tc.want)
			}
			if checks != 3 {
				t.Fatalf("%d authority checks, want 3", checks)
			}
			if tc.want == PostgresTimeout && time.Since(started) > 3*time.Second {
				t.Fatalf("a stalled server held the check for %s", time.Since(started))
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if tc.mode == "md5" && server.passwordSeen {
				t.Fatal("a password was sent in answer to an md5 challenge")
			}
			if tc.want != PostgresReachable {
				if len(server.statements) != 0 {
					t.Fatalf("statements after a failed connect: %v", server.statements)
				}
				return
			}
			if !slices.Equal(server.statements, []string{"SELECT 1"}) {
				t.Fatalf("statements %v, want exactly one SELECT 1", server.statements)
			}
			if len(server.startup) != 3 || server.startup["user"] != "status_probe" || server.startup["database"] != "orders" || server.startup["application_name"] != "portal-app-probe" {
				t.Fatalf("startup parameters %v", server.startup)
			}
		})
	}
}

func TestPostgresProbeAuthorityRefusalsWithholdTheCheck(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		f := newProbeFixture(t)
		server := newFakePG(t, "scram", "pw", &tls.Config{Certificates: []tls.Certificate{f.ca.serverCert(t, probeHost)}})
		dials := 0
		f.serve(server.addr(), &dials)
		policy := f.policy(t, probePublicTarget(f.ca), 5*time.Second, 2, "")
		raw, sig := f.command(t, policy, "orders", "status_probe", "pw", "")
		n := 0
		result, err := f.probe.Run(context.Background(), raw, sig, f.auth, func(context.Context) error {
			n++
			if n == failAt {
				return errors.New("revoked")
			}
			return nil
		})
		if !errors.Is(err, ErrDenied) || result != (PostgresResult{}) {
			t.Fatalf("check %d refused: %q, %v", failAt, result.Outcome, err)
		}
		if failAt < 3 && dials != 0 {
			t.Fatalf("check %d refused after a dial", failAt)
		}
	}
}

func TestPostgresProbeThirdConcurrentCheckIsBusy(t *testing.T) {
	f := newProbeFixture(t)
	server := newFakePG(t, "stall", "pw", &tls.Config{Certificates: []tls.Certificate{f.ca.serverCert(t, probeHost)}})
	f.serve(server.addr(), nil)
	policy := f.policy(t, probePublicTarget(f.ca), 2*time.Second, 2, "")
	raw, sig := f.command(t, policy, "orders", "status_probe", "pw", "")
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.probe.Run(context.Background(), raw, sig, f.auth, allow)
		}()
	}
	for deadline := time.Now().Add(2 * time.Second); server.count() < 2; {
		if time.Now().After(deadline) {
			t.Fatal("the first two checks never connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := f.probe.Run(context.Background(), raw, sig, f.auth, allow); !errors.Is(err, ErrBusy) {
		t.Fatalf("third concurrent check: %v, want busy", err)
	}
	wg.Wait()
}

func TestPostgresProbePrivateTargetIsJudgedBeforeTheDial(t *testing.T) {
	networks := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/16", Ports: []int{5432}}}
	sum, err := appplatform.PrivateNetworksSHA256(networks)
	if err != nil {
		t.Fatal(err)
	}
	for name, answer := range map[string]string{"loopback": "127.0.0.1", "public": "93.184.216.34", "outside the allowlist": "172.16.0.9"} {
		f := newProbeFixture(t, WithPrivateNetworks(networks))
		dials := 0
		f.serve("127.0.0.1:1", &dials)
		f.probe.lookup = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(answer)}, nil
		}
		target := appplatform.PostgresTarget{ID: "orders", Host: "db.corp.example", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPrivate}
		policy := f.policy(t, target, 5*time.Second, 2, sum)
		raw, sig := f.command(t, policy, "orders", "status_probe", "pw", sum)
		if _, err := f.probe.Run(context.Background(), raw, sig, f.auth, allow); !errors.Is(err, ErrDenied) || dials != 0 {
			t.Fatalf("%s: %v after %d dials, want denied before any dial", name, err, dials)
		}
	}
	// A runner without that allowlist serves no private target at all.
	f := newProbeFixture(t)
	target := appplatform.PostgresTarget{ID: "orders", Host: "db.corp.example", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPrivate}
	policy := f.policy(t, target, 5*time.Second, 2, sum)
	raw, sig := f.command(t, policy, "orders", "status_probe", "pw", sum)
	if _, err := f.probe.Run(context.Background(), raw, sig, f.auth, allow); !errors.Is(err, ErrDenied) {
		t.Fatalf("private target without a runner allowlist: %v", err)
	}
}
