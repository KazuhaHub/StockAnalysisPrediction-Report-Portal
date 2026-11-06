package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
)

// These tests pin GO-2026-6909, which crewjam/saml v0.5.1 ships unfixed and the patched copy under
// third_party/crewjam-saml closes. They drive crewjam's ServiceProvider directly with a Response the
// test IdP really signed, because both defects only appear after signature verification has passed:
// a signed assertion without a Subject panicked in validateAssertion, and a signed assertion whose
// Subject carried a NameID in a foreign namespace had that foreign value accepted as the subject.

const (
	shapeIdPEntityID = "https://idp.example.test/metadata"
	shapeSPEntityID  = "https://sp.example.test/metadata"
	shapeACS         = "https://sp.example.test/acs"
	shapeRequestID   = "id-shape-request"
)

// shapeServiceProvider returns a crewjam SP that trusts a freshly generated IdP key, and a signer for
// that key.
func shapeServiceProvider(t *testing.T) (*crewjam.ServiceProvider, *dsig.SigningContext) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "shape test idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	signer := dsig.NewDefaultSigningContext(dsig.TLSCertKeyStore(tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}))
	signer.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	if err := signer.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatal(err)
	}

	acs, _ := url.Parse(shapeACS)
	sp := &crewjam.ServiceProvider{
		EntityID: shapeSPEntityID,
		AcsURL:   *acs,
		IDPMetadata: &crewjam.EntityDescriptor{
			EntityID: shapeIdPEntityID,
			IDPSSODescriptors: []crewjam.IDPSSODescriptor{{SSODescriptor: crewjam.SSODescriptor{RoleDescriptor: crewjam.RoleDescriptor{
				KeyDescriptors: []crewjam.KeyDescriptor{{Use: "signing", KeyInfo: crewjam.KeyInfo{X509Data: crewjam.X509Data{
					X509Certificates: []crewjam.X509Certificate{{Data: base64.StdEncoding.EncodeToString(der)}},
				}}}},
			}}}},
		},
	}
	return sp, signer
}

// signedShapeResponse wraps the subject and conditions fragments in an assertion and a Response,
// signs the Response with the IdP key, and returns the serialized document.
func signedShapeResponse(t *testing.T, signer *dsig.SigningContext, subjectAndConditions string) []byte {
	t.Helper()
	now := time.Now().UTC()
	instant := now.Format(time.RFC3339)
	later := now.Add(5 * time.Minute).Format(time.RFC3339)
	earlier := now.Add(-5 * time.Minute).Format(time.RFC3339)
	doc := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion"` +
		` ID="id-shape-response" Version="2.0" IssueInstant="` + instant + `" Destination="` + shapeACS + `" InResponseTo="` + shapeRequestID + `">` +
		`<saml:Issuer>` + shapeIdPEntityID + `</saml:Issuer>` +
		`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>` +
		`<saml:Assertion ID="id-shape-assertion" Version="2.0" IssueInstant="` + instant + `">` +
		`<saml:Issuer>` + shapeIdPEntityID + `</saml:Issuer>` +
		strings.NewReplacer("{later}", later, "{earlier}", earlier, "{acs}", shapeACS, "{req}", shapeRequestID, "{aud}", shapeSPEntityID).Replace(subjectAndConditions) +
		`</saml:Assertion></samlp:Response>`

	parsed := etree.NewDocument()
	if err := parsed.ReadFromString(doc); err != nil {
		t.Fatal(err)
	}
	signed, err := signer.SignEnveloped(parsed.Root())
	if err != nil {
		t.Fatal(err)
	}
	out := etree.NewDocument()
	out.SetRoot(signed)
	raw, err := out.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const shapeConditions = `<saml:Conditions NotBefore="{earlier}" NotOnOrAfter="{later}">` +
	`<saml:AudienceRestriction><saml:Audience>{aud}</saml:Audience></saml:AudienceRestriction></saml:Conditions>`

const shapeConfirmation = `<saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer">` +
	`<saml:SubjectConfirmationData NotOnOrAfter="{later}" Recipient="{acs}" InResponseTo="{req}"/></saml:SubjectConfirmation>`

func parseShapeResponse(t *testing.T, sp *crewjam.ServiceProvider, raw []byte) (assertion *crewjam.Assertion, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseXMLResponse panicked on a signed response: %v", r)
		}
	}()
	return sp.ParseXMLResponse(raw, []string{shapeRequestID}, sp.AcsURL)
}

// TestSAMLShapeFixtureIsAccepted proves the fixture is a valid signed login, so the refusals below are
// caused by the shapes they test and not by a broken signature or clock.
func TestSAMLShapeFixtureIsAccepted(t *testing.T) {
	sp, signer := shapeServiceProvider(t)
	raw := signedShapeResponse(t, signer, `<saml:Subject><saml:NameID>alice@example.test</saml:NameID>`+shapeConfirmation+`</saml:Subject>`+shapeConditions)
	assertion, err := parseShapeResponse(t, sp, raw)
	if err != nil {
		t.Fatalf("a well-formed signed response must be accepted: %v", err)
	}
	if assertion.Subject == nil || assertion.Subject.NameID == nil || assertion.Subject.NameID.Value != "alice@example.test" {
		t.Fatalf("the SAML-namespace NameID must be read, got %+v", assertion.Subject)
	}
}

// requireShapeRefusal asserts that crewjam refused the response for the given reason, so a broken
// signature or clock in the fixture cannot make a refusal test pass.
func requireShapeRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	var invalid *crewjam.InvalidResponseError
	if !errors.As(err, &invalid) {
		t.Fatalf("want an InvalidResponseError carrying %q, got %v", reason, err)
	}
	if invalid.PrivateErr == nil || invalid.PrivateErr.Error() != reason {
		t.Fatalf("refused for %v, want %q", invalid.PrivateErr, reason)
	}
}

// TestSAMLSignedAssertionWithoutSubjectIsRefused: before the patch this dereferenced a nil Subject.
func TestSAMLSignedAssertionWithoutSubjectIsRefused(t *testing.T) {
	sp, signer := shapeServiceProvider(t)
	_, err := parseShapeResponse(t, sp, signedShapeResponse(t, signer, shapeConditions))
	requireShapeRefusal(t, err, "assertion has no Subject")
}

// TestSAMLSignedAssertionWithoutConditionsIsRefused: before the patch this dereferenced nil Conditions.
func TestSAMLSignedAssertionWithoutConditionsIsRefused(t *testing.T) {
	sp, signer := shapeServiceProvider(t)
	raw := signedShapeResponse(t, signer, `<saml:Subject><saml:NameID>alice@example.test</saml:NameID>`+shapeConfirmation+`</saml:Subject>`)
	_, err := parseShapeResponse(t, sp, raw)
	requireShapeRefusal(t, err, "assertion has no Conditions")
}

// TestSAMLSignedConfirmationWithoutDataIsRefused: before the patch this dereferenced a nil
// SubjectConfirmationData.
func TestSAMLSignedConfirmationWithoutDataIsRefused(t *testing.T) {
	sp, signer := shapeServiceProvider(t)
	raw := signedShapeResponse(t, signer, `<saml:Subject><saml:NameID>alice@example.test</saml:NameID>`+
		`<saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"/></saml:Subject>`+shapeConditions)
	_, err := parseShapeResponse(t, sp, raw)
	requireShapeRefusal(t, err, "assertion SubjectConfirmation has no SubjectConfirmationData")
}

// TestSAMLForeignNamespaceNameIDIsNotTheSubject: before the patch a NameID in any namespace filled
// Subject.NameID, so the portal would have keyed the account on the foreign value.
func TestSAMLForeignNamespaceNameIDIsNotTheSubject(t *testing.T) {
	sp, signer := shapeServiceProvider(t)
	raw := signedShapeResponse(t, signer, `<saml:Subject><x:NameID xmlns:x="urn:example:foreign">mallory@example.test</x:NameID>`+
		shapeConfirmation+`</saml:Subject>`+shapeConditions)
	assertion, err := parseShapeResponse(t, sp, raw)
	if err != nil {
		t.Fatalf("the signed response itself is valid and must parse: %v", err)
	}
	if assertion.Subject == nil {
		t.Fatal("the SAML-namespace Subject must still be read")
	}
	if assertion.Subject.NameID != nil {
		t.Fatalf("a foreign-namespace NameID was taken as the subject: %q", assertion.Subject.NameID.Value)
	}
}

// TestSAMLForeignNamespaceStatementsAreIgnored: attributes drive the SSO role and organizational unit
// rules and SessionIndex drives logout, so a foreign-namespace AttributeStatement, Attribute,
// AttributeValue or AuthnStatement must not be read as the assertion's. This goes beyond
// crewjam/saml#674, which bound only the subject and conditions.
func TestSAMLForeignNamespaceStatementsAreIgnored(t *testing.T) {
	sp, signer := shapeServiceProvider(t)
	subject := `<saml:Subject><saml:NameID>alice@example.test</saml:NameID>` + shapeConfirmation + `</saml:Subject>` + shapeConditions
	cases := map[string]string{
		"statement": `<x:AttributeStatement xmlns:x="urn:example:foreign"><x:Attribute Name="role"><x:AttributeValue>admin</x:AttributeValue></x:Attribute></x:AttributeStatement>`,
		"attribute": `<saml:AttributeStatement><x:Attribute xmlns:x="urn:example:foreign" Name="role"><saml:AttributeValue>admin</saml:AttributeValue></x:Attribute></saml:AttributeStatement>`,
		"value":     `<saml:AttributeStatement><saml:Attribute Name="role"><x:AttributeValue xmlns:x="urn:example:foreign">admin</x:AttributeValue></saml:Attribute></saml:AttributeStatement>`,
		"authn":     `<x:AuthnStatement xmlns:x="urn:example:foreign" AuthnInstant="2026-01-01T00:00:00Z" SessionIndex="forged"/>`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			assertion, err := parseShapeResponse(t, sp, signedShapeResponse(t, signer, subject+statement))
			if err != nil {
				t.Fatalf("the signed response itself is valid and must parse: %v", err)
			}
			for _, as := range assertion.AttributeStatements {
				for _, attr := range as.Attributes {
					if len(attr.Values) > 0 {
						t.Fatalf("a foreign-namespace element produced attribute %q = %q", attr.Name, attr.Values[0].Value)
					}
				}
			}
			for _, authn := range assertion.AuthnStatements {
				if authn.SessionIndex != "" {
					t.Fatalf("a foreign-namespace AuthnStatement set SessionIndex %q", authn.SessionIndex)
				}
			}
		})
	}
}

// TestSAMLNativeStatementsAreRead is the positive control for the binding above.
func TestSAMLNativeStatementsAreRead(t *testing.T) {
	sp, signer := shapeServiceProvider(t)
	raw := signedShapeResponse(t, signer, `<saml:Subject><saml:NameID>alice@example.test</saml:NameID>`+shapeConfirmation+`</saml:Subject>`+shapeConditions+
		`<saml:AuthnStatement AuthnInstant="2026-01-01T00:00:00Z" SessionIndex="s-1"/>`+
		`<saml:AttributeStatement><saml:Attribute Name="role"><saml:AttributeValue>reader</saml:AttributeValue></saml:Attribute></saml:AttributeStatement>`)
	assertion, err := parseShapeResponse(t, sp, raw)
	if err != nil {
		t.Fatalf("a well-formed signed response must be accepted: %v", err)
	}
	if len(assertion.AuthnStatements) != 1 || assertion.AuthnStatements[0].SessionIndex != "s-1" {
		t.Fatalf("SessionIndex not read: %+v", assertion.AuthnStatements)
	}
	if len(assertion.AttributeStatements) != 1 || len(assertion.AttributeStatements[0].Attributes) != 1 ||
		len(assertion.AttributeStatements[0].Attributes[0].Values) != 1 || assertion.AttributeStatements[0].Attributes[0].Values[0].Value != "reader" {
		t.Fatalf("attribute not read: %+v", assertion.AttributeStatements)
	}
}
