package appplatform

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

type offlineTestEntry struct {
	name string
	body []byte
	mode fs.FileMode
}

func offlineTestArchive(
	t testing.TB,
	entries ...offlineTestEntry,
) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	for _, entry := range entries {
		header := &zip.FileHeader{
			Name: entry.name, Method: zip.Deflate,
		}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func offlineStaticFixture(t *testing.T) (
	descriptor, signature, frontend []byte,
	blob string,
) {
	t.Helper()
	frontend = frontendTestArchive(t, frontendTestEntry{
		name: "index.html", body: "<!doctype html><title>Offline</title>",
	})
	descriptor = descriptorFixture(t, false)
	var manifest Manifest
	if err := json.Unmarshal(descriptor, &manifest); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(frontend)
	manifest.Frontend.SHA256 = hex.EncodeToString(sum[:])
	var err error
	descriptor, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature = offlineSignatureFixture(t, descriptor)
	blob = "blobs/" + manifest.Frontend.SHA256 + "/ui.zip"
	return
}

func offlineSignatureFixture(t testing.TB, descriptor []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"format":            "portal.app.signature/1-dev.1",
		"algorithm":         "Ed25519",
		"publisher":         "example-publisher",
		"key_id":            "fixture-key",
		"descriptor_sha256": platformTestDigest(descriptor),
		"signature":         strings.Repeat("A", 86),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func offlineEnvelopeFixture(t *testing.T) []byte {
	t.Helper()
	descriptor, signature, frontend, blob := offlineStaticFixture(t)
	return offlineTestArchive(t,
		offlineTestEntry{name: "descriptor.json", body: descriptor, mode: 0644},
		offlineTestEntry{name: "descriptor.sig", body: signature, mode: 0644},
		offlineTestEntry{name: blob, body: frontend, mode: 0644},
	)
}

func TestOfflineEnvelopeBindsSignedDeclarationAndFrontend(t *testing.T) {
	raw := offlineEnvelopeFixture(t)
	envelope, err := InspectOfflineEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Inspection.Format != "portal.offline-envelope/1-dev.1" ||
		envelope.Inspection.AppID != "notes-sample" ||
		envelope.Inspection.Version != "1.0.0-beta.1" ||
		envelope.Inspection.Execution != "static" ||
		envelope.Inspection.DescriptorSHA256 != platformTestDigest(envelope.Descriptor) ||
		envelope.Inspection.Frontend.SHA256 != platformTestDigest(envelope.Frontend) ||
		envelope.Inspection.SignatureBytes != int64(len(envelope.Signature)) {
		t.Fatalf("offline identity mismatch: %+v", envelope.Inspection)
	}
}

func TestOfflineEnvelopeRejectsUnsafeOrMismatchedMembers(t *testing.T) {
	descriptor, signature, frontend, blob := offlineStaticFixture(t)
	valid := map[string]offlineTestEntry{
		"descriptor.json": {name: "descriptor.json", body: descriptor, mode: 0644},
		"descriptor.sig":  {name: "descriptor.sig", body: signature, mode: 0644},
		blob:              {name: blob, body: frontend, mode: 0644},
	}
	cases := map[string]func() []byte{
		"missing signature": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"], valid[blob])
		},
		"invalid signature schema": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"],
				offlineTestEntry{
					name: "descriptor.sig", body: []byte("invalid"),
					mode: 0644,
				},
				valid[blob])
		},
		"extra member": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"], valid["descriptor.sig"],
				valid[blob], offlineTestEntry{
					name: "private.txt", body: []byte("private"), mode: 0644,
				})
		},
		"wrong blob digest": func() []byte {
			wrong := offlineTestEntry{
				name: "blobs/" + platformTestDigest([]byte("wrong")) + "/ui.zip",
				body: frontend, mode: 0644,
			}
			return offlineTestArchive(t,
				valid["descriptor.json"], valid["descriptor.sig"], wrong)
		},
		"traversal": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"], valid["descriptor.sig"],
				offlineTestEntry{
					name: "../ui.zip", body: frontend, mode: 0644,
				})
		},
		"symlink": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"], valid["descriptor.sig"],
				offlineTestEntry{
					name: blob, body: frontend,
					mode: fs.ModeSymlink | 0777,
				})
		},
		"executable": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"], valid["descriptor.sig"],
				offlineTestEntry{name: blob, body: frontend, mode: 0755})
		},
		"duplicate": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"], valid["descriptor.json"],
				valid["descriptor.sig"])
		},
		"changed frontend": func() []byte {
			return offlineTestArchive(t,
				valid["descriptor.json"], valid["descriptor.sig"],
				offlineTestEntry{
					name: blob, body: append(frontend, 'x'), mode: 0644,
				})
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := InspectOfflineEnvelope(build())
			if !errors.Is(err, ErrInvalidOfflineEnvelope) ||
				len(result.Descriptor) != 0 ||
				len(result.Signature) != 0 ||
				len(result.Frontend) != 0 {
				t.Fatalf("unsafe envelope accepted or returned partial bytes: %+v %v",
					result.Inspection, err)
			}
		})
	}
}

func TestOfflineEnvelopeRefusesManagedUntilImagePayloadExists(t *testing.T) {
	frontend := frontendTestArchive(t, frontendTestEntry{
		name: "index.html", body: "<!doctype html>",
	})
	descriptor := descriptorFixture(t, true)
	var manifest Manifest
	if err := json.Unmarshal(descriptor, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Frontend.SHA256 = platformTestDigest(frontend)
	descriptor, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw := offlineTestArchive(t,
		offlineTestEntry{name: "descriptor.json", body: descriptor, mode: 0644},
		offlineTestEntry{
			name: "descriptor.sig",
			body: offlineSignatureFixture(t, descriptor), mode: 0644,
		},
		offlineTestEntry{
			name: "blobs/" + manifest.Frontend.SHA256 + "/ui.zip",
			body: frontend, mode: 0644,
		})
	if _, err := InspectOfflineEnvelope(raw); !errors.Is(
		err, ErrManagedOfflineNotReady) {
		t.Fatalf("managed frontend-only envelope accepted: %v", err)
	}
}

func TestOfflineEnvelopeLimits(t *testing.T) {
	for _, raw := range [][]byte{
		nil,
		make([]byte, MaxOfflineEnvelopeBytes+1),
		[]byte("not a ZIP"),
	} {
		if _, err := InspectOfflineEnvelope(raw); !errors.Is(
			err, ErrInvalidOfflineEnvelope) {
			t.Fatalf("invalid envelope accepted: %v", err)
		}
	}
}

func platformTestDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
