package appplatform

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestSharedPublisherSignatureVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/publisher-signatures.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		FrameSHA256 string `json:"frame_sha256"`
		Cases       []struct {
			Name       string         `json:"name"`
			Accept     bool           `json:"accept"`
			Descriptor string         `json:"descriptor_base64"`
			Envelope   string         `json:"envelope_base64"`
			Trust      PublisherTrust `json:"trust"`
			Now        string         `json:"now_ms"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "f0553de5a701b0d8f665a2e997a525c8022ba5039ab6bce4c62da18f3118d641" {
		t.Fatal("shared signature corpus bytes changed")
	}
	if len(corpus.Cases) != 55 {
		t.Fatalf("unexpected corpus size: %d", len(corpus.Cases))
	}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			descriptor, err := base64.StdEncoding.Strict().DecodeString(c.Descriptor)
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := base64.StdEncoding.Strict().DecodeString(c.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			result, err := VerifyPublisherDescriptor(descriptor, envelope, c.Trust, c.Now)
			if (err == nil) != c.Accept {
				t.Fatalf("accept=%v, got %v", c.Accept, err)
			}
			if c.Accept {
				if result.Descriptor.Manifest.ID != "notes-sample" || result.PublisherRevision != c.Trust.Publishers[0].Revision || result.KeyRevision != c.Trust.Publishers[0].Keys[0].Revision || result.KeyID != "test-key-1" {
					t.Fatal("verification result lost reviewed identity")
				}
				sum := sha256.Sum256(descriptorSignatureInput(descriptor))
				if hex.EncodeToString(sum[:]) != corpus.FrameSHA256 {
					t.Fatal("signature frame differs from shared vector")
				}
			}
		})
	}
}

func TestPublisherSignatureSchema(t *testing.T) {
	raw, err := os.ReadFile("contract/publisher-signature-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "76fc2d3bb471e21934782dd2fbc7006bcbfc79068c087f6c48c645db9a6026f4" {
		t.Fatal("signature schema bytes changed")
	}
	var schema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if json.Unmarshal(raw, &schema) != nil {
		t.Fatal("invalid signature schema")
	}
	typ := reflect.TypeFor[descriptorSignature]()
	if len(schema.Properties) != typ.NumField() || len(schema.Required) != typ.NumField() || schema.AdditionalProperties {
		t.Fatal("closed signature fields drifted")
	}
	required := map[string]bool{}
	for _, key := range schema.Required {
		required[key] = true
	}
	for i := range typ.NumField() {
		key := typ.Field(i).Tag.Get("json")
		if _, ok := schema.Properties[key]; !ok || !required[key] {
			t.Fatalf("signature field missing: %s", key)
		}
	}
}

func TestPublisherTrustBounds(t *testing.T) {
	raw, err := os.ReadFile("testdata/publisher-signatures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Trust PublisherTrust `json:"trust"`
		} `json:"cases"`
	}
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid fixture")
	}
	makeTrust := func() PublisherTrust {
		var copy PublisherTrust
		encoded, _ := json.Marshal(fixture.Cases[0].Trust)
		_ = json.Unmarshal(encoded, &copy)
		return copy
	}
	trust := makeTrust()
	for i := 1; i < 128; i++ {
		trust.Publishers = append(trust.Publishers, TrustedPublisher{ID: fmt.Sprintf("bounded-%d", i), Revision: "1"})
	}
	if !validPublisherTrust(trust) {
		t.Fatal("128 distinct publishers should fit")
	}
	trust.Publishers = append(trust.Publishers, TrustedPublisher{ID: "overflow", Revision: "1"})
	if validPublisherTrust(trust) {
		t.Fatal("publisher limit not enforced")
	}
	trust = makeTrust()
	for i := 0; i < 4; i++ {
		p := TrustedPublisher{ID: fmt.Sprintf("bounded-%d", i), Revision: "1"}
		for j := 0; j < 64; j++ {
			p.Namespaces = append(p.Namespaces, PublisherNamespace{Kind: "exact", Value: fmt.Sprintf("owned-%d-%d", i, j)})
		}
		trust.Publishers = append(trust.Publishers, p)
	}
	// The original namespace plus 255 non-overlapping grants fits the total cap.
	last := &trust.Publishers[len(trust.Publishers)-1]
	extra := last.Namespaces[len(last.Namespaces)-1]
	last.Namespaces = last.Namespaces[:63]
	if !validPublisherTrust(trust) {
		t.Fatal("256 distinct namespaces should fit")
	}
	last.Namespaces = append(last.Namespaces, extra)
	if validPublisherTrust(trust) {
		t.Fatal("namespace total cap not enforced")
	}
	trust = makeTrust()
	for i := 0; i < 64; i++ {
		trust.Publishers[0].Namespaces = append(trust.Publishers[0].Namespaces, PublisherNamespace{Kind: "exact", Value: fmt.Sprintf("owned-%d", i)})
	}
	if validPublisherTrust(trust) {
		t.Fatal("per-publisher namespace cap not enforced")
	}
}

func TestStoredPublisherNamespaces(t *testing.T) {
	good := []byte(`[{"kind":"prefix","value":"notes-"}]`)
	if ns, err := ParsePublisherNamespaces(good); err != nil || len(ns) != 1 {
		t.Fatalf("valid namespaces: %v", err)
	}
	for _, raw := range []string{`null`, `{}`, `[{"kind":"prefix","kind":"exact","value":"notes-"}]`, `[{"Kind":"exact","value":"notes"}]`, `[{"kind":"exact","value":"notes","extra":"ignored"}]`, `[{"kind":"prefix","value":"notes"}]`, `[] {}`} {
		if _, err := ParsePublisherNamespaces([]byte(raw)); err == nil {
			t.Fatalf("accepted ambiguous namespace bytes: %s", raw)
		}
	}
	if err := ValidatePublisherTrust(PublisherTrust{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePublisherTrust(PublisherTrust{Publishers: []TrustedPublisher{{ID: "bad"}}}); err == nil {
		t.Fatal("invalid persisted trust accepted")
	}
}
