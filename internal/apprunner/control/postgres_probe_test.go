package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func postgresProbeFixture(t *testing.T) (PostgresProbeCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(public)
	a := Authority{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(sum[:])}
	policy, err := appplatform.CanonicalPostgresPolicy(appplatform.PostgresPolicy{Targets: []appplatform.PostgresTarget{{ID: "orders", Host: "db.example.com", Port: 5432, Database: "orders", Network: appplatform.HTTPNetworkPublic}}, Deadline: 10 * time.Second, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := appplatform.ParsePostgresPolicy(policy)
	now := time.Now().UnixMilli()
	c := PostgresProbeCommand{Format: PostgresProbeCommandFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: "40000000-0000-4000-8000-000000000001", Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: parsed.SHA256, TargetID: "orders", CredentialName: "orders-probe", Username: []byte("status_probe"), Password: []byte("a long password"), IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+30000, 10)}
	return c, key, a, now
}

func TestPostgresProbeCommandSignVerify(t *testing.T) {
	c, key, a, now := postgresProbeFixture(t)
	raw, sig, err := SignPostgresProbeCommand(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyPostgresProbeCommand(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	got := v.Command()
	if string(got.Username) != "status_probe" || string(got.Password) != "a long password" || got.TargetID != "orders" {
		t.Fatalf("round trip lost a field: %+v", got)
	}
	got.Clear()
	v.Clear()
	if v.command.Password != nil {
		t.Fatal("Clear left the password")
	}
	// The signature covers every byte, and another signer's key is refused.
	other := append([]byte(nil), raw...)
	other[len(other)/2] ^= 1
	if _, err := VerifyPostgresProbeCommand(other, sig, a, now); err == nil {
		t.Fatal("a changed command verified")
	}
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	forged := ed25519.Sign(otherKey, frame(postgresProbePurpose, raw))
	if _, err := VerifyPostgresProbeCommand(raw, forged, a, now); err == nil {
		t.Fatal("a command signed by another key verified")
	}
	if _, err := VerifyPostgresProbeCommand(raw, sig, a, now+31000); err == nil {
		t.Fatal("an expired command verified")
	}
}

func TestPostgresProbeCommandRefusals(t *testing.T) {
	cases := map[string]func(*PostgresProbeCommand){
		"unknown target":               func(c *PostgresProbeCommand) { c.TargetID = "other" },
		"empty username":               func(c *PostgresProbeCommand) { c.Username = nil },
		"long username":                func(c *PostgresProbeCommand) { c.Username = bytes.Repeat([]byte("u"), 64) },
		"nul in password":              func(c *PostgresProbeCommand) { c.Password = []byte("a\x00b") },
		"long password":                func(c *PostgresProbeCommand) { c.Password = bytes.Repeat([]byte("p"), 1025) },
		"lifetime below deadline":      func(c *PostgresProbeCommand) { c.ExpiresMS = strconv.FormatInt(mustCounter(c.IssuedMS)+9999, 10) },
		"lifetime above 30 s":          func(c *PostgresProbeCommand) { c.ExpiresMS = strconv.FormatInt(mustCounter(c.IssuedMS)+30001, 10) },
		"private digest not in policy": func(c *PostgresProbeCommand) { c.PrivateNetworksSHA256 = strings.Repeat("c", 64) },
		"policy digest of other bytes": func(c *PostgresProbeCommand) { c.PolicySHA256 = strings.Repeat("d", 64) },
		"other installation id":        func(c *PostgresProbeCommand) { c.InstallationID = "not-a-uuid" },
		"other format":                 func(c *PostgresProbeCommand) { c.Format = NetworkHTTPRequestFormat },
		"credential name":              func(c *PostgresProbeCommand) { c.CredentialName = "Bad Name" },
	}
	for name, mutate := range cases {
		c, key, a, now := postgresProbeFixture(t)
		mutate(&c)
		if _, _, err := SignPostgresProbeCommand(c, key, a, now); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
	// A command with an unknown member is refused on the wire even when correctly signed.
	c, key, a, now := postgresProbeFixture(t)
	raw, _, _ := SignPostgresProbeCommand(c, key, a, now)
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	members["skip_verify"] = json.RawMessage("true")
	extended, _ := json.Marshal(members)
	if _, err := VerifyPostgresProbeCommand(extended, ed25519.Sign(key, frame(postgresProbePurpose, extended)), a, now); err == nil {
		t.Fatal("a command with an unknown member verified")
	}
}

func mustCounter(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func TestPostgresProbeCheckIsDomainSeparated(t *testing.T) {
	c, key, a, now := postgresProbeFixture(t)
	raw, _, err := SignPostgresProbeCommand(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	challenge := strings.Repeat("e", 64)
	proof, sig, err := SignPostgresProbeCheckDigest(hex.EncodeToString(digest[:]), challenge, mustCounter(c.ExpiresMS), key, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPostgresProbeCheck(c, challenge, proof, sig, a, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPostgresProbeCheck(c, strings.Repeat("f", 64), proof, sig, a, now); err == nil {
		t.Fatal("a proof answered another challenge")
	}
	changed := c
	changed.Username = []byte("someone_else")
	if err := VerifyPostgresProbeCheck(changed, challenge, proof, sig, a, now); err == nil {
		t.Fatal("a proof bound another command")
	}
	if err := VerifyPostgresProbeCheck(c, challenge, proof, sig, a, now+1500); err == nil {
		t.Fatal("a proof outlived its second")
	}
	// An HTTP check proof, which has its own format and purpose, never answers a probe's challenge,
	// nor does a probe proof answer an HTTP check.
	httpProof, httpSig, err := SignNetworkHTTPCheckDigest(hex.EncodeToString(digest[:]), challenge, mustCounter(c.ExpiresMS), key, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPostgresProbeCheck(c, challenge, httpProof, httpSig, a, now); err == nil {
		t.Fatal("an HTTP check proof answered a probe challenge")
	}
	crossed := ed25519.Sign(key, frame(networkHTTPCheckPurpose, proof))
	if err := VerifyPostgresProbeCheck(c, challenge, proof, crossed, a, now); err == nil {
		t.Fatal("a proof signed for the HTTP purpose verified as a probe proof")
	}
}
