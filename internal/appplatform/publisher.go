package appplatform

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type PublisherNamespace struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type PublisherKey struct {
	ID          string  `json:"id"`
	Algorithm   string  `json:"algorithm"`
	PublicKey   string  `json:"public_key"`
	NotBeforeMS string  `json:"not_before_ms"`
	NotAfterMS  *string `json:"not_after_ms"`
	RevokedMS   *string `json:"revoked_ms"`
	Revision    string  `json:"revision"`
}
type TrustedPublisher struct {
	ID         string               `json:"id"`
	Enabled    bool                 `json:"enabled"`
	Revision   string               `json:"revision"`
	Namespaces []PublisherNamespace `json:"namespaces"`
	Keys       []PublisherKey       `json:"keys"`
}
type PublisherTrust struct {
	Publishers []TrustedPublisher `json:"publishers"`
}
type VerifiedPublisherDescriptor struct {
	Descriptor                            ParsedDescriptor
	KeyID, PublisherRevision, KeyRevision string
}

var ErrUntrustedDescriptor = errors.New("untrusted application descriptor")

const MaxDescriptorSignatureBytes = 4096

// VerifyPublisherDescriptor checks exact bytes against a caller-supplied current
// deployment trust snapshot. It does not fetch trust, persist a release or grant
// execution authority. Callers must recheck trust revisions at activation commit.
func VerifyPublisherDescriptor(raw, envelope []byte, trust PublisherTrust, nowMS string) (VerifiedPublisherDescriptor, error) {
	fail := func() (VerifiedPublisherDescriptor, error) {
		return VerifiedPublisherDescriptor{}, ErrUntrustedDescriptor
	}
	now, ok := publisherCounter(nowMS, false)
	if !ok || !validPublisherTrust(trust) {
		return fail()
	}
	detached, ok := parseDescriptorSignature(envelope)
	if !ok {
		return fail()
	}
	parsed, err := ParseDescriptor(raw)
	if err != nil || parsed.SHA256 != detached.DescriptorSHA256 || parsed.Manifest.Publisher != detached.Publisher {
		return fail()
	}
	for _, publisher := range trust.Publishers {
		if publisher.ID != detached.Publisher {
			continue
		}
		if !publisher.Enabled {
			return fail()
		}
		allowed := false
		for _, ns := range publisher.Namespaces {
			allowed = allowed || namespaceContains(ns, parsed.Manifest.ID)
		}
		if !allowed {
			return fail()
		}
		for _, key := range publisher.Keys {
			if key.ID != detached.KeyID {
				continue
			}
			before, _ := publisherCounter(key.NotBeforeMS, false)
			if now < before || key.RevokedMS != nil {
				return fail()
			}
			if key.NotAfterMS != nil {
				after, _ := publisherCounter(*key.NotAfterMS, false)
				if now >= after {
					return fail()
				}
			}
			public, _ := decodePublisherBase64(key.PublicKey, ed25519.PublicKeySize)
			signature, _ := decodePublisherBase64(detached.Signature, ed25519.SignatureSize)
			if !ed25519.Verify(ed25519.PublicKey(public), descriptorSignatureInput(raw), signature) {
				return fail()
			}
			return VerifiedPublisherDescriptor{Descriptor: parsed, KeyID: key.ID, PublisherRevision: publisher.Revision, KeyRevision: key.Revision}, nil
		}
	}
	return fail()
}

type descriptorSignature struct {
	Format           string `json:"format"`
	Algorithm        string `json:"algorithm"`
	Publisher        string `json:"publisher"`
	KeyID            string `json:"key_id"`
	DescriptorSHA256 string `json:"descriptor_sha256"`
	Signature        string `json:"signature"`
}

func parseDescriptorSignature(raw []byte) (descriptorSignature, bool) {
	var value descriptorSignature
	if len(raw) == 0 || len(raw) > MaxDescriptorSignatureBytes || !utf8.Valid(raw) {
		return value, false
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	token, err := scan.Token()
	if err != nil || token != json.Delim('{') || !scanObject(scan, 1) {
		return value, false
	}
	if _, err = scan.Token(); err != io.EOF {
		return value, false
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if decode.Decode(&value) != nil || value.Format != "portal.app.signature/1-dev.1" || value.Algorithm != "Ed25519" || !slugPattern.MatchString(value.Publisher) || !publisherKeyID(value.KeyID) || !digestPattern.MatchString(value.DescriptorSHA256) {
		return value, false
	}
	_, ok := decodePublisherBase64(value.Signature, ed25519.SignatureSize)
	return value, ok
}

func descriptorSignatureInput(raw []byte) []byte {
	prefix := []byte("portal.app/descriptor/v2\x00")
	framed := make([]byte, len(prefix)+8+len(raw))
	copy(framed, prefix)
	binary.BigEndian.PutUint64(framed[len(prefix):], uint64(len(raw)))
	copy(framed[len(prefix)+8:], raw)
	return framed
}

func decodePublisherBase64(value string, size int) ([]byte, bool) {
	if len(value) != (size*8+5)/6 {
		return nil, false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return decoded, err == nil && len(decoded) == size && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func publisherCounter(value string, positive bool) (int64, bool) {
	if len(value) == 0 || len(value) > 19 || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && (!positive || n > 0)
}

func publisherKeyID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (i > 0 && (c == '.' || c == '_' || c == '-')) {
			continue
		}
		return false
	}
	return true
}

func validPublisherTrust(trust PublisherTrust) bool {
	if len(trust.Publishers) > 128 {
		return false
	}
	publishers := map[string]bool{}
	var namespaces []PublisherNamespace
	keyCount := 0
	for _, publisher := range trust.Publishers {
		_, revisionOK := publisherCounter(publisher.Revision, true)
		if !slugPattern.MatchString(publisher.ID) || publishers[publisher.ID] || !revisionOK || len(publisher.Namespaces) > 64 {
			return false
		}
		publishers[publisher.ID] = true
		for _, ns := range publisher.Namespaces {
			if len(namespaces) >= 256 || !validPublisherNamespace(ns) {
				return false
			}
			for _, previous := range namespaces {
				if namespaceOverlap(ns, previous) {
					return false
				}
			}
			namespaces = append(namespaces, ns)
		}
		keys := map[string]bool{}
		keyCount += len(publisher.Keys)
		if keyCount > 256 {
			return false
		}
		for _, key := range publisher.Keys {
			before, beforeOK := publisherCounter(key.NotBeforeMS, false)
			_, revisionOK = publisherCounter(key.Revision, true)
			_, publicOK := decodePublisherBase64(key.PublicKey, ed25519.PublicKeySize)
			if !publisherKeyID(key.ID) || keys[key.ID] || key.Algorithm != "Ed25519" || !beforeOK || !revisionOK || !publicOK {
				return false
			}
			keys[key.ID] = true
			if key.NotAfterMS != nil {
				after, ok := publisherCounter(*key.NotAfterMS, false)
				if !ok || after <= before {
					return false
				}
			}
			if key.RevokedMS != nil {
				if _, ok := publisherCounter(*key.RevokedMS, false); !ok {
					return false
				}
			}
		}
	}
	return true
}

func validPublisherNamespace(ns PublisherNamespace) bool {
	if ns.Kind == "exact" {
		return slugPattern.MatchString(ns.Value)
	}
	return ns.Kind == "prefix" && len(ns.Value) <= 64 && strings.HasSuffix(ns.Value, "-") && slugPattern.MatchString(strings.TrimSuffix(ns.Value, "-"))
}
func namespaceContains(ns PublisherNamespace, id string) bool {
	return (ns.Kind == "exact" && ns.Value == id) || (ns.Kind == "prefix" && strings.HasPrefix(id, ns.Value))
}
func namespaceOverlap(a, b PublisherNamespace) bool {
	if a.Kind == "exact" {
		return namespaceContains(b, a.Value)
	}
	if b.Kind == "exact" {
		return namespaceContains(a, b.Value)
	}
	return strings.HasPrefix(a.Value, b.Value) || strings.HasPrefix(b.Value, a.Value)
}
