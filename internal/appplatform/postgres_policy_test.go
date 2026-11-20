package appplatform

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testPostgresCA(t *testing.T, isCA bool) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: isCA, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func goodPostgresPolicy() PostgresPolicy {
	return PostgresPolicy{
		Targets: []PostgresTarget{
			{ID: "orders-primary", Host: "db.example.com", Port: 5432, Database: "orders", Network: HTTPNetworkPublic},
			{ID: "orders-replica", Host: "replica.example.com", Port: 5432, Database: "orders", Network: HTTPNetworkPublic},
		},
		Deadline: 10 * time.Second, Concurrency: 2,
	}
}

func TestPostgresPolicyRoundTripAndDigests(t *testing.T) {
	raw, err := CanonicalPostgresPolicy(goodPostgresPolicy())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePostgresPolicy(raw)
	if err != nil || len(parsed.Policy.Targets) != 2 || parsed.SHA256 == "" {
		t.Fatalf("parse: %+v %v", parsed, err)
	}
	if want := `{"format":"portal.postgres-probe-policy/1-dev.1","targets":[{"id":"orders-primary","host":"db.example.com","port":5432,"database":"orders","network":"public"},{"id":"orders-replica","host":"replica.example.com","port":5432,"database":"orders","network":"public"}],"deadline_ms":10000,"concurrency":2}`; string(raw) != want {
		t.Fatalf("canonical bytes %s", raw)
	}
	a, _ := PostgresTargetSHA256(parsed.Policy.Targets[0])
	b, _ := PostgresTargetSHA256(parsed.Policy.Targets[1])
	if a == b || len(a) != 64 {
		t.Fatal("target digests do not tell targets apart")
	}
	moved := parsed.Policy.Targets[0]
	moved.Port = 5433
	if c, _ := PostgresTargetSHA256(moved); c == a {
		t.Fatal("a port change kept the digest")
	}
}

func TestPostgresPolicyRefusals(t *testing.T) {
	ca := testPostgresCA(t, true)
	canonicalCA, _, err := CanonicalPostgresCA(ca)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(*PostgresPolicy)) PostgresPolicy {
		p := goodPostgresPolicy()
		p.Targets = append([]PostgresTarget(nil), p.Targets...)
		f(&p)
		return p
	}
	cases := map[string]PostgresPolicy{
		"ip literal in a public target":      mutate(func(p *PostgresPolicy) { p.Targets[0].Host = "93.184.216.34" }),
		"private literal in a public target": mutate(func(p *PostgresPolicy) { p.Targets[0].Host = "10.1.2.3" }),
		"oversize database name":             mutate(func(p *PostgresPolicy) { p.Targets[0].Database = strings.Repeat("d", 64) }),
		"bad database characters":            mutate(func(p *PostgresPolicy) { p.Targets[0].Database = "or ders" }),
		"port zero":                          mutate(func(p *PostgresPolicy) { p.Targets[0].Port = 0 }),
		"port above 65535":                   mutate(func(p *PostgresPolicy) { p.Targets[0].Port = 65536 }),
		"duplicate id":                       mutate(func(p *PostgresPolicy) { p.Targets[1].ID = p.Targets[0].ID }),
		"same endpoint twice":                mutate(func(p *PostgresPolicy) { p.Targets[1].Host, p.Targets[1].ID = p.Targets[0].Host, "again" }),
		"deadline above the cap":             mutate(func(p *PostgresPolicy) { p.Deadline = 10001 * time.Millisecond }),
		"concurrency three":                  mutate(func(p *PostgresPolicy) { p.Concurrency = 3 }),
		"no targets":                         mutate(func(p *PostgresPolicy) { p.Targets = nil }),
		"private target without digest":      mutate(func(p *PostgresPolicy) { p.Targets[0].Network, p.Targets[0].Host = HTTPNetworkPrivate, "10.1.2.3" }),
		"digest without private target":      mutate(func(p *PostgresPolicy) { p.PrivateNetworksSHA256 = strings.Repeat("a", 64) }),
		"a CA that is not a CA":              mutate(func(p *PostgresPolicy) { p.Targets[0].CAPEM = testPostgresCA(t, false) }),
		"a CA with a different spelling":     mutate(func(p *PostgresPolicy) { p.Targets[0].CAPEM = strings.TrimSuffix(canonicalCA, "\n") }),
		"two certificates":                   mutate(func(p *PostgresPolicy) { p.Targets[0].CAPEM = canonicalCA + canonicalCA }),
	}
	for name, p := range cases {
		if _, err := CanonicalPostgresPolicy(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good, _ := CanonicalPostgresPolicy(goodPostgresPolicy())
	for name, raw := range map[string]string{
		"unsorted targets":   `{"format":"portal.postgres-probe-policy/1-dev.1","targets":[{"id":"orders-replica","host":"replica.example.com","port":5432,"database":"orders","network":"public"},{"id":"orders-primary","host":"db.example.com","port":5432,"database":"orders","network":"public"}],"deadline_ms":10000,"concurrency":2}`,
		"unknown key":        strings.Replace(string(good), `"concurrency":2`, `"concurrency":2,"skip_verify":true`, 1),
		"unknown target key": strings.Replace(string(good), `"network":"public"}`, `"network":"public","insecure":true}`, 1),
		"trailing data":      string(good) + " ",
		"whitespace":         strings.Replace(string(good), `"targets":`, `"targets": `, 1),
	} {
		if _, err := ParsePostgresPolicy([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPostgresPolicyCAAndPrivateTargets(t *testing.T) {
	canonicalCA, fingerprint, err := CanonicalPostgresCA(testPostgresCA(t, true))
	if err != nil || len(fingerprint) != 64 {
		t.Fatal(err)
	}
	p := PostgresPolicy{Targets: []PostgresTarget{
		{ID: "lan", Host: "10.20.0.5", Port: 5433, Database: "orders", Network: HTTPNetworkPrivate, CAPEM: canonicalCA},
		{ID: "lan-name", Host: "db.corp.example", Port: 5432, Database: "orders", Network: HTTPNetworkPrivate},
	}, Deadline: time.Second, Concurrency: 1, PrivateNetworksSHA256: strings.Repeat("c", 64)}
	raw, err := CanonicalPostgresPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePostgresPolicy(raw)
	if err != nil || !parsed.Policy.HasPrivate() {
		t.Fatalf("%v", err)
	}
	if _, ok := PostgresCARoots(parsed.Policy.Targets[0]); !ok {
		t.Fatal("the reviewed CA yields no roots")
	}
	if !parsed.Policy.CheckPrivate([]PrivateNetwork{{Prefix: "10.20.0.0/16", Ports: []int{5432, 5433}}}) {
		t.Fatal("an allowlist that lists both ports refused")
	}
	if parsed.Policy.CheckPrivate([]PrivateNetwork{{Prefix: "10.20.0.0/16", Ports: []int{5432}}}) {
		t.Fatal("a literal on a port its entry does not list was admitted")
	}
	if parsed.Policy.CheckPrivate([]PrivateNetwork{{Prefix: "10.99.0.0/16", Ports: []int{5432, 5433}}}) {
		t.Fatal("a literal outside every entry was admitted")
	}
	if parsed.Policy.CheckPrivate(nil) {
		t.Fatal("an empty allowlist admitted a private target")
	}
}

func TestPostgresCredential(t *testing.T) {
	good := `{"username":"status_probe","password":"p4ss word"}`
	c, err := ParsePostgresCredential([]byte(good))
	if err != nil || string(c.Username) != "status_probe" || string(c.Password) != "p4ss word" {
		t.Fatalf("%v", err)
	}
	c.Clear()
	for name, raw := range map[string]string{
		"empty username":   `{"username":"","password":"x"}`,
		"empty password":   `{"username":"u","password":""}`,
		"long username":    `{"username":"` + strings.Repeat("u", 64) + `","password":"x"}`,
		"long password":    `{"username":"u","password":"` + strings.Repeat("p", 1025) + `"}`,
		"missing password": `{"username":"u"}`,
		"unknown member":   `{"username":"u","password":"x","host":"h"}`,
		"duplicate member": `{"username":"u","username":"v","password":"x"}`,
		"upper case key":   `{"Username":"u","password":"x"}`,
		"null":             `{"username":null,"password":"x"}`,
		"nul escape":       `{"username":"u","password":"a\u0000b"}`,
		"trailing data":    good + "x",
		"empty object":     `{}`,
	} {
		if _, err := ParsePostgresCredential([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	long := `{"username":"u","password":"` + strings.Repeat("p", 1024) + `"}`
	if c, err := ParsePostgresCredential([]byte(long)); err != nil {
		t.Fatalf("a 1024 byte password: %v", err)
	} else {
		c.Clear()
	}
}
