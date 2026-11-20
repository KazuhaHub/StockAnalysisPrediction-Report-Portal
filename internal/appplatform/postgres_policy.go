package appplatform

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The PostgreSQL liveness probe policy (doc 178). The broker terminates the PostgreSQL protocol
// itself and performs one fixed exchange (connect, TLS, authenticate, SELECT 1), so a policy only
// names where it may connect: reviewed host, port and database, in the network class doc 175
// defines, and optionally the one private certificate authority that signs the server.
const PostgresPolicyFormat = "portal.postgres-probe-policy/1-dev.1"

const (
	MaxPostgresPolicyBytes  = 16384
	MaxPostgresTargets      = 64
	MaxPostgresCAPEMBytes   = 4096
	MaxPostgresDeadlineMS   = 10000
	MaxPostgresConcurrency  = 2
	MaxPostgresUsernameLen  = 63
	MaxPostgresPasswordLen  = 1024
	PostgresCredentialLimit = 4096
)

var ErrInvalidPostgresPolicy = errors.New("invalid PostgreSQL probe policy")

var postgresTargetIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var postgresDatabasePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)

// ValidPostgresTargetID is the shape of a target id (the same as an HTTP target id).
func ValidPostgresTargetID(id string) bool { return postgresTargetIDPattern.MatchString(id) }

// PostgresTarget is one reviewed destination. CAPEM, when present, is the only certificate
// authority the server certificate may chain to; otherwise the system roots apply. There is no
// field that turns certificate verification off.
type PostgresTarget struct {
	ID       string
	Host     string
	Port     int
	Database string
	Network  string
	CAPEM    string
}

// PostgresPolicy holds the targets and the shared bounds. PrivateNetworksSHA256 is the digest of
// the runner allowlist the policy was reviewed against; it is present exactly when a target is
// private, and a command carries it so the runner can refuse another allowlist.
type PostgresPolicy struct {
	Targets               []PostgresTarget
	Deadline              time.Duration
	Concurrency           int
	PrivateNetworksSHA256 string
}

// ParsedPostgresPolicy is a parsed policy with the digest of its exact stored bytes.
type ParsedPostgresPolicy struct {
	Policy PostgresPolicy
	SHA256 string
}

type postgresTargetWire struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Network  string `json:"network"`
	CAPEM    string `json:"ca_pem,omitempty"`
}

type postgresPolicyWire struct {
	Format                string               `json:"format"`
	Targets               []postgresTargetWire `json:"targets"`
	DeadlineMS            int64                `json:"deadline_ms"`
	Concurrency           int                  `json:"concurrency"`
	PrivateNetworksSHA256 string               `json:"private_networks_sha256,omitempty"`
}

// CanonicalPostgresCA re-encodes a certificate authority PEM into its one canonical spelling and
// returns it with the SHA-256 of the DER certificate. The input must be at most 4096 bytes and
// hold exactly one CERTIFICATE block (nothing else, no trailing data) that is a CA certificate.
func CanonicalPostgresCA(input string) (canonical string, fingerprint string, err error) {
	if len(input) < 1 || len(input) > MaxPostgresCAPEMBytes || !utf8.ValidString(input) {
		return "", "", ErrInvalidPostgresPolicy
	}
	block, rest := pem.Decode([]byte(input))
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return "", "", ErrInvalidPostgresPolicy
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.BasicConstraintsValid || !cert.IsCA {
		return "", "", ErrInvalidPostgresPolicy
	}
	out := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}))
	if len(out) > MaxPostgresCAPEMBytes {
		return "", "", ErrInvalidPostgresPolicy
	}
	sum := sha256.Sum256(block.Bytes)
	return out, hex.EncodeToString(sum[:]), nil
}

// PostgresCARoots returns the pool a target's reviewed CA yields, or false when the target has none.
func PostgresCARoots(t PostgresTarget) (*x509.CertPool, bool) {
	if t.CAPEM == "" {
		return nil, false
	}
	pool := x509.NewCertPool()
	return pool, pool.AppendCertsFromPEM([]byte(t.CAPEM))
}

func normalizePostgresTarget(t PostgresTarget) (PostgresTarget, error) {
	var zero PostgresTarget
	if !postgresTargetIDPattern.MatchString(t.ID) || !postgresDatabasePattern.MatchString(t.Database) || t.Port < 1 || t.Port > 65535 {
		return zero, ErrInvalidPostgresPolicy
	}
	if _, ok := validNetworkTargetHost(t.Host, t.Network); !ok {
		return zero, ErrInvalidPostgresPolicy
	}
	if t.CAPEM != "" {
		canonical, _, err := CanonicalPostgresCA(t.CAPEM)
		if err != nil || canonical != t.CAPEM {
			return zero, ErrInvalidPostgresPolicy
		}
	}
	return t, nil
}

func encodePostgresTarget(t PostgresTarget) ([]byte, error) {
	raw, err := json.Marshal(postgresTargetWire(t))
	if err != nil {
		return nil, ErrInvalidPostgresPolicy
	}
	return raw, nil
}

// CanonicalPostgresTarget returns the canonical bytes a target digest covers: keys in the order
// id, host, port, database, network, then ca_pem when the target has one.
func CanonicalPostgresTarget(t PostgresTarget) ([]byte, error) {
	t, err := normalizePostgresTarget(t)
	if err != nil {
		return nil, err
	}
	return encodePostgresTarget(t)
}

// PostgresTargetSHA256 is the lowercase hex SHA-256 of CanonicalPostgresTarget. A change of host,
// port, database, class or CA changes it, so a binding reviewed for one target never follows
// another.
func PostgresTargetSHA256(t PostgresTarget) (string, error) {
	raw, err := CanonicalPostgresTarget(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// NormalizePostgresPolicy validates and clones a policy with its targets sorted by id.
func NormalizePostgresPolicy(p PostgresPolicy) (PostgresPolicy, error) {
	var zero PostgresPolicy
	if len(p.Targets) < 1 || len(p.Targets) > MaxPostgresTargets ||
		p.Deadline < time.Millisecond || p.Deadline > MaxPostgresDeadlineMS*time.Millisecond || p.Deadline%time.Millisecond != 0 ||
		p.Concurrency < 1 || p.Concurrency > MaxPostgresConcurrency {
		return zero, ErrInvalidPostgresPolicy
	}
	targets := make([]PostgresTarget, 0, len(p.Targets))
	endpoints := make(map[string]bool, len(p.Targets))
	private := false
	for _, t := range p.Targets {
		n, err := normalizePostgresTarget(t)
		if err != nil {
			return zero, ErrInvalidPostgresPolicy
		}
		// One endpoint and database is one target: a second spelling of it would give a binding two
		// digests for the same destination.
		endpoint := n.Network + "|" + n.Host + "|" + n.Database + "|" + strconv.Itoa(n.Port)
		if endpoints[endpoint] {
			return zero, ErrInvalidPostgresPolicy
		}
		endpoints[endpoint] = true
		private = private || n.Network == HTTPNetworkPrivate
		targets = append(targets, n)
	}
	slices.SortFunc(targets, func(a, b PostgresTarget) int { return strings.Compare(a.ID, b.ID) })
	for i := 1; i < len(targets); i++ {
		if targets[i].ID == targets[i-1].ID {
			return zero, ErrInvalidPostgresPolicy
		}
	}
	// The allowlist digest is bound exactly when a target needs the allowlist.
	if private != (p.PrivateNetworksSHA256 != "") || (private && !shaHexPattern.MatchString(p.PrivateNetworksSHA256)) {
		return zero, ErrInvalidPostgresPolicy
	}
	p.Targets = targets
	if _, err := encodePostgresPolicy(p); err != nil {
		return zero, err
	}
	return p, nil
}

var shaHexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func encodePostgresPolicy(p PostgresPolicy) ([]byte, error) {
	wires := make([]postgresTargetWire, 0, len(p.Targets))
	for _, t := range p.Targets {
		wires = append(wires, postgresTargetWire(t))
	}
	raw, err := json.Marshal(postgresPolicyWire{Format: PostgresPolicyFormat, Targets: wires, DeadlineMS: p.Deadline.Milliseconds(), Concurrency: p.Concurrency, PrivateNetworksSHA256: p.PrivateNetworksSHA256})
	if err != nil || len(raw) > MaxPostgresPolicyBytes {
		return nil, ErrInvalidPostgresPolicy
	}
	return raw, nil
}

// CanonicalPostgresPolicy returns the canonical bytes of a valid policy.
func CanonicalPostgresPolicy(p PostgresPolicy) ([]byte, error) {
	p, err := NormalizePostgresPolicy(p)
	if err != nil {
		return nil, err
	}
	return encodePostgresPolicy(p)
}

// ParsePostgresPolicy accepts only the exact canonical bytes of a valid policy: closed keys,
// sorted targets, canonical certificate spellings and no trailing data.
func ParsePostgresPolicy(raw []byte) (ParsedPostgresPolicy, error) {
	var zero ParsedPostgresPolicy
	if len(raw) == 0 || len(raw) > MaxPostgresPolicyBytes || !utf8.Valid(raw) {
		return zero, ErrInvalidPostgresPolicy
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !scanObject(scan, 1) {
		return zero, ErrInvalidPostgresPolicy
	}
	if _, err := scan.Token(); err != io.EOF {
		return zero, ErrInvalidPostgresPolicy
	}
	var w postgresPolicyWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&w) != nil || w.Format != PostgresPolicyFormat || len(w.Targets) > MaxPostgresTargets {
		return zero, ErrInvalidPostgresPolicy
	}
	policy := PostgresPolicy{Deadline: time.Duration(w.DeadlineMS) * time.Millisecond, Concurrency: w.Concurrency, PrivateNetworksSHA256: w.PrivateNetworksSHA256}
	for _, t := range w.Targets {
		policy.Targets = append(policy.Targets, PostgresTarget(t))
	}
	p, err := NormalizePostgresPolicy(policy)
	if err != nil {
		return zero, err
	}
	canonical, err := encodePostgresPolicy(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, ErrInvalidPostgresPolicy
	}
	sum := sha256.Sum256(raw)
	return ParsedPostgresPolicy{Policy: p, SHA256: hex.EncodeToString(sum[:])}, nil
}

// ValidPostgresPolicy is the shape test of a stored grant policy: it parses, and nothing more.
func ValidPostgresPolicy(stored string) bool {
	_, err := ParsePostgresPolicy([]byte(stored))
	return err == nil
}

// HasPrivate reports whether any target is a private-network target.
func (p PostgresPolicy) HasPrivate() bool {
	return slices.ContainsFunc(p.Targets, func(t PostgresTarget) bool { return t.Network == HTTPNetworkPrivate })
}

// Select returns the target with the given id.
func (p PostgresPolicy) Select(id string) (PostgresTarget, bool) {
	for _, t := range p.Targets {
		if t.ID == id {
			return t, true
		}
	}
	return PostgresTarget{}, false
}

// Literal is the target's IP literal, or the invalid address for a DNS name.
func (t PostgresTarget) Literal() netip.Addr {
	literal, _ := validNetworkTargetHost(t.Host, t.Network)
	return literal
}

// CheckPrivate says whether the runner allowlist admits every private target of the policy: an IP
// literal must lie in an entry that lists its port, and a DNS name needs some entry that lists its
// port (the addresses it resolves to are judged at call time, as an HTTP target is). Public targets
// need nothing from the allowlist. PostgreSQL always runs over TLS, so the https rule applies.
func (p PostgresPolicy) CheckPrivate(networks []PrivateNetwork) bool {
	for _, t := range p.Targets {
		if t.Network != HTTPNetworkPrivate {
			continue
		}
		admitted := false
		for _, n := range networks {
			if !slices.Contains(n.Ports, t.Port) {
				continue
			}
			if literal := t.Literal(); literal.IsValid() && !Admit([]PrivateNetwork{n}, literal, uint16(t.Port), "https") {
				continue
			}
			admitted = true
			break
		}
		if !admitted {
			return false
		}
	}
	return true
}
