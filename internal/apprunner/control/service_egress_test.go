package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func serviceEgressFixture(t *testing.T) (ServiceEgressSessionCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	base, key, authority, now, _, _ := backendRequestFixture(t)
	registration, err := VerifyBackendRegistration(base.RegistrationRaw, base.RegistrationSignature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate := registration.Candidate()
	identity := sha256.Sum256([]byte("service identity fixture"))
	return ServiceEgressSessionCommand{
		Format:                ServiceEgressSessionFormat,
		DeploymentID:          authority.DeploymentID,
		ControllerEpoch:       candidate.ControllerEpoch,
		ControlRevision:       strconv.FormatInt(authority.ControlRevision, 10),
		SessionID:             "10000000-0000-4000-8000-000000000001",
		InstallationID:        candidate.InstallationID,
		Generation:            candidate.TargetGeneration,
		PolicyRevision:        "1",
		ReleaseSHA256:         candidate.Payload.ReleaseSHA256,
		ServiceIdentitySHA256: hex.EncodeToString(identity[:]),
		Resources:             []string{"network.http", "notifications.publish"},
		Concurrency:           "2",
		RatePerMinute:         "120",
		Burst:                 "20",
		RegistrationRaw:       base.RegistrationRaw,
		RegistrationSignature: base.RegistrationSignature,
		IssuedMS:              strconv.FormatInt(now, 10),
		ExpiresMS:             strconv.FormatInt(now+300000, 10),
	}, key, authority, now
}

func TestServiceEgressSessionCanonicalSignedAndBounded(t *testing.T) {
	command, key, authority, now := serviceEgressFixture(t)
	raw, signature, err := SignServiceEgressSession(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyServiceEgressSession(raw, signature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	got := verified.Command()
	if got.SessionID != command.SessionID || got.Concurrency != "2" || strings.Join(got.Resources, ",") != "network.http,notifications.publish" {
		t.Fatal("verified command changed")
	}
	if verified.RegistrationIdentity().InstallationID != command.InstallationID || verified.RegistrationIdentity().Generation != command.Generation {
		t.Fatal("registration identity not exposed")
	}
	if _, ok := verified.InitialRegistration(); !ok {
		t.Fatal("registration kind not exposed")
	}
	// Accessors return copies.
	got.Resources[0] = "tasks.schedule"
	got.RegistrationRaw[0] ^= 1
	if again := verified.Command(); again.Resources[0] != "network.http" || again.RegistrationRaw[0] == got.RegistrationRaw[0] {
		t.Fatal("verified command aliased its caller")
	}
	// A single-resource session and the shortest and longest lifetimes are valid.
	for name, edit := range map[string]func(*ServiceEgressSessionCommand){
		"network only":       func(c *ServiceEgressSessionCommand) { c.Resources = []string{"network.http"} },
		"notifications only": func(c *ServiceEgressSessionCommand) { c.Resources = []string{"notifications.publish"} },
		"shortest":           func(c *ServiceEgressSessionCommand) { c.ExpiresMS = strconv.FormatInt(now+60000, 10) },
		"concurrency 1":      func(c *ServiceEgressSessionCommand) { c.Concurrency = "1" },
		"concurrency 4":      func(c *ServiceEgressSessionCommand) { c.Concurrency = "4" },
	} {
		c := command
		edit(&c)
		if _, _, err := SignServiceEgressSession(c, key, authority, now); err != nil {
			t.Fatalf("%s refused: %v", name, err)
		}
	}
	// Every field is bound: a session cannot be signed for any other tuple,
	// resource set, budget or lifetime.
	other := "20000000-0000-4000-8000-000000000009"
	for name, edit := range map[string]func(*ServiceEgressSessionCommand){
		"format":            func(c *ServiceEgressSessionCommand) { c.Format = "portal.service-egress-session/2" },
		"deployment":        func(c *ServiceEgressSessionCommand) { c.DeploymentID = other },
		"epoch":             func(c *ServiceEgressSessionCommand) { c.ControllerEpoch = other },
		"revision":          func(c *ServiceEgressSessionCommand) { c.ControlRevision = "2" },
		"session":           func(c *ServiceEgressSessionCommand) { c.SessionID = "not-a-uuid" },
		"installation":      func(c *ServiceEgressSessionCommand) { c.InstallationID = other },
		"generation":        func(c *ServiceEgressSessionCommand) { c.Generation = "2" },
		"generation zero":   func(c *ServiceEgressSessionCommand) { c.Generation = "0" },
		"policy revision":   func(c *ServiceEgressSessionCommand) { c.PolicyRevision = "0" },
		"policy leading 0":  func(c *ServiceEgressSessionCommand) { c.PolicyRevision = "01" },
		"release":           func(c *ServiceEgressSessionCommand) { c.ReleaseSHA256 = strings.Repeat("b", 64) },
		"identity digest":   func(c *ServiceEgressSessionCommand) { c.ServiceIdentitySHA256 = "abc" },
		"identity zero":     func(c *ServiceEgressSessionCommand) { c.ServiceIdentitySHA256 = strings.Repeat("0", 64) },
		"no resources":      func(c *ServiceEgressSessionCommand) { c.Resources = nil },
		"empty resources":   func(c *ServiceEgressSessionCommand) { c.Resources = []string{} },
		"unsorted":          func(c *ServiceEgressSessionCommand) { c.Resources = []string{"notifications.publish", "network.http"} },
		"duplicate":         func(c *ServiceEgressSessionCommand) { c.Resources = []string{"network.http", "network.http"} },
		"foreign resource":  func(c *ServiceEgressSessionCommand) { c.Resources = []string{"network.http", "secrets.use"} },
		"task resource":     func(c *ServiceEgressSessionCommand) { c.Resources = []string{"tasks.schedule"} },
		"concurrency 0":     func(c *ServiceEgressSessionCommand) { c.Concurrency = "0" },
		"concurrency 5":     func(c *ServiceEgressSessionCommand) { c.Concurrency = "5" },
		"concurrency 02":    func(c *ServiceEgressSessionCommand) { c.Concurrency = "02" },
		"concurrency empty": func(c *ServiceEgressSessionCommand) { c.Concurrency = "" },
		"rate":              func(c *ServiceEgressSessionCommand) { c.RatePerMinute = "121" },
		"burst":             func(c *ServiceEgressSessionCommand) { c.Burst = "21" },
		"too short":         func(c *ServiceEgressSessionCommand) { c.ExpiresMS = strconv.FormatInt(now+59999, 10) },
		"too long":          func(c *ServiceEgressSessionCommand) { c.ExpiresMS = strconv.FormatInt(now+300001, 10) },
		"not yet issued": func(c *ServiceEgressSessionCommand) {
			c.IssuedMS = strconv.FormatInt(now+1, 10)
			c.ExpiresMS = strconv.FormatInt(now+300001, 10)
		},
		"already expired": func(c *ServiceEgressSessionCommand) {
			c.IssuedMS = strconv.FormatInt(now-300000, 10)
			c.ExpiresMS = strconv.FormatInt(now, 10)
		},
		"no registration": func(c *ServiceEgressSessionCommand) { c.RegistrationRaw = nil },
		"registration signature": func(c *ServiceEgressSessionCommand) {
			c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
			c.RegistrationSignature[0] ^= 1
		},
		"registration body": func(c *ServiceEgressSessionCommand) {
			c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
			c.RegistrationRaw[len(c.RegistrationRaw)/2] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := command
			c.Resources = append([]string(nil), command.Resources...)
			edit(&c)
			if raw, sig, err := SignServiceEgressSession(c, key, authority, now); err == nil || len(raw) != 0 || len(sig) != 0 {
				t.Fatal("invalid session was signed")
			}
		})
	}
	// The registration proof must be for the very tuple the session names.
	for name, edit := range map[string]func(*ServiceEgressSessionCommand){
		"installation": func(c *ServiceEgressSessionCommand) { c.InstallationID = other },
		"generation":   func(c *ServiceEgressSessionCommand) { c.Generation = "2" },
		"epoch":        func(c *ServiceEgressSessionCommand) { c.ControllerEpoch = other },
		"release":      func(c *ServiceEgressSessionCommand) { c.ReleaseSHA256 = strings.Repeat("c", 64) },
	} {
		c := command
		edit(&c)
		if _, _, err := SignServiceEgressSession(c, key, authority, now); err == nil {
			t.Fatalf("session tuple %s differing from its registration was signed", name)
		}
	}
	// Verification is closed over the exact canonical bytes and the signature.
	sign := func(raw []byte) []byte { return ed25519.Sign(key, frame(serviceEgressSessionPurpose, raw)) }
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", " "); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	fields["extra"] = json.RawMessage(`"x"`)
	extra, _ := json.Marshal(fields)
	duplicate := append(bytes.Clone(raw[:len(raw)-1]), []byte(`,"burst":"20"}`)...)
	for name, candidate := range map[string][]byte{"indented": pretty.Bytes(), "unknown field": extra, "duplicate key": duplicate, "empty": nil, "oversize": bytes.Repeat([]byte(" "), MaxCommandBytes+1), "not json": []byte("{")} {
		if _, err := VerifyServiceEgressSession(candidate, sign(candidate), authority, now); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	tampered := bytes.Clone(signature)
	tampered[0] ^= 1
	for name, sig := range map[string][]byte{"tampered": tampered, "short": signature[:63], "empty": nil, "long": append(bytes.Clone(signature), 0)} {
		if _, err := VerifyServiceEgressSession(raw, sig, authority, now); err == nil {
			t.Fatalf("%s signature accepted", name)
		}
	}
	// Another purpose's signature over the same bytes is not a session signature.
	foreign := ed25519.Sign(key, frame(serviceBackendTaskSignaturePurpose, raw))
	if _, err := VerifyServiceEgressSession(raw, foreign, authority, now); err == nil {
		t.Fatal("cross-purpose signature accepted")
	}
	// Expiry is enforced at verification time, not only at signing time.
	if _, err := VerifyServiceEgressSession(raw, signature, authority, now+300000); err == nil {
		t.Fatal("expired session verified")
	}
	if _, err := VerifyServiceEgressSession(raw, signature, authority, now-1); err == nil {
		t.Fatal("session verified before it was issued")
	}
	wrong := authority
	wrong.ControlRevision++
	if _, err := VerifyServiceEgressSession(raw, signature, wrong, now); err == nil {
		t.Fatal("session verified for another control revision")
	}
}
