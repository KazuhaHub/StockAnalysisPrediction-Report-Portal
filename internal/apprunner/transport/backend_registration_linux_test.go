//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func registrationRequestFixture(t *testing.T) (control.Authority, control.BackendRegistrationCommand, []byte, []byte, []byte, []byte) {
	t.Helper()
	a, ref, reference, signature, descriptor := candidateReadinessRequestFixture(t)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: parsed.Manifest.ID, DeploymentID: ref.DeploymentID, ControllerEpoch: ref.ControllerEpoch, InstallationID: ref.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: ref.Payload.ReleaseSHA256}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	document, err := registration.Document()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(document) })
	digest := sha256.Sum256(document)
	c := control.BackendRegistrationCommand{Format: control.BackendRegistrationFormat, EffectID: "11111111-1111-4111-8111-111111119901", CandidateRaw: reference, CandidateSignature: signature, DocumentSHA256: hex.EncodeToString(digest[:])}
	c.PayloadSHA256, err = control.BackendRegistrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignBackendRegistration(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return a, c, raw, sig, descriptor, document
}
func registrationReplyFixture(c control.BackendRegistrationCommand, now int64) []byte {
	m := map[string]string{"format": "portal.runner.backend-registration-claim/1-dev.1", "claim_id": "11111111-1111-4111-8111-111111119902", "effect_id": c.EffectID, "payload_sha256": c.PayloadSHA256, "source_sha256": strings.Repeat("a", 64), "native_id": strings.Repeat("b", 64), "document_sha256": c.DocumentSHA256, "claimed_ms": strconv.FormatInt(now, 10)}
	// Use typed ordered fields for the canonical journal marker; this mock tests
	// wire validation only and cannot mint native provenance or a live claim.
	marker := struct {
		Format         string `json:"format"`
		ClaimID        string `json:"claim_id"`
		EffectID       string `json:"effect_id"`
		PayloadSHA256  string `json:"payload_sha256"`
		SourceSHA256   string `json:"source_sha256"`
		NativeID       string `json:"native_id"`
		DocumentSHA256 string `json:"document_sha256"`
		ClaimedMS      string `json:"claimed_ms"`
	}{m["format"], m["claim_id"], m["effect_id"], m["payload_sha256"], m["source_sha256"], m["native_id"], m["document_sha256"], m["claimed_ms"]}
	encoded, _ := json.Marshal(marker)
	binding := sha256.Sum256(encoded)
	receipt := struct {
		Format   string `json:"format"`
		Claim    any    `json:"claim"`
		Binding  string `json:"binding_sha256"`
		Device   string `json:"device"`
		Inode    string `json:"inode"`
		Observed string `json:"observed_ms"`
		Settled  string `json:"settled_ms"`
	}{"portal.runner.backend-registration-delivery/1-dev.1", marker, hex.EncodeToString(binding[:]), "1", "2", strconv.FormatInt(now, 10), strconv.FormatInt(now, 10)}
	result, _ := json.Marshal(receipt)
	return result
}
func TestReadOnlyEndpointCannotDeliverRegistration(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	a, _, raw, sig, descriptor, document := registrationRequestFixture(t)
	proof, err := deliverRegistrationFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, document)
	if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
		t.Fatal("read-only endpoint accepted secret delivery")
	}
}
func TestRegistrationChannelFramingAndPrivateProvenance(t *testing.T) {
	for _, mode := range []string{"accepted", "historical", "nonce", "magic", "oversize", "truncated", "trailing", "future", "effect", "document", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			a, c, raw, sig, descriptor, document := registrationRequestFixture(t)
			dir, err := os.MkdirTemp("/tmp", "rbr-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "runner.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				request, err := io.ReadAll(io.LimitReader(conn, 250000))
				defer clear(request)
				if err != nil || len(request) < 44 || string(request[:8]) != backendRegistrationQueryMagic {
					return
				}
				n := int(binary.BigEndian.Uint32(request[40:44]))
				offset := 44 + n + 64
				if offset+4 > len(request) {
					return
				}
				descriptorSize := int(binary.BigEndian.Uint32(request[offset : offset+4]))
				offset += 4 + descriptorSize
				if offset+4 > len(request) {
					return
				}
				documentSize := int(binary.BigEndian.Uint32(request[offset : offset+4]))
				offset += 4
				if offset+documentSize != len(request) || !bytes.Equal(request[offset:], document) {
					return
				}
				replyCommand := c
				switch mode {
				case "effect":
					replyCommand.EffectID = "11111111-1111-4111-8111-111111119903"
				case "document":
					replyCommand.DocumentSHA256 = strings.Repeat("f", 64)
				}
				now := time.Now().UnixMilli()
				if mode == "historical" {
					now -= 1000
				}
				if mode == "future" {
					now += 10000
				}
				body := registrationReplyFixture(replyCommand, now)
				if mode == "unknown" {
					body = append(body[:len(body)-1], []byte(`,"extra":true}`)...)
				}
				header := make([]byte, 44)
				copy(header, backendRegistrationReplyMagic)
				digest := sha256.Sum256(request)
				copy(header[8:40], digest[:])
				binary.BigEndian.PutUint32(header[40:44], uint32(len(body)))
				switch mode {
				case "nonce":
					header[8] ^= 1
				case "magic":
					header[0] = 'X'
				case "oversize":
					binary.BigEndian.PutUint32(header[40:44], 4097)
				case "truncated":
					body = body[:len(body)/2]
				case "trailing":
					body = append(body, 'x')
				}
				conn.Write(append(header, body...))
			}()
			proof, err := deliverRegistrationFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, document)
			<-done
			if mode == "accepted" || mode == "historical" {
				if err != nil || proof.ReceivedAt().IsZero() || proof.Endpoint().RootController || !proof.Receipt().MatchesCommand(c) {
					t.Fatal("private channel proof", err)
				}
				cloned := proof.Controller()
				cloned.PublicKey[0] ^= 1
				if bytes.Equal(cloned.PublicKey, proof.Controller().PublicKey) {
					t.Fatal("authority getter aliased key")
				}
				if _, err := json.Marshal(proof); err == nil {
					t.Fatal("observation serialized into authority")
				}
			} else if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("invalid registration reply accepted", mode)
			}
		})
	}
}
func TestRegistrationChannelRejectsInvalidMaterialBeforeConnect(t *testing.T) {
	a, _, raw, sig, descriptor, document := registrationRequestFixture(t)
	for _, mode := range []string{"signature", "document", "descriptor", "oversize", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			r, s, d, p := bytes.Clone(raw), bytes.Clone(sig), bytes.Clone(descriptor), bytes.Clone(document)
			defer clear(p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "signature":
				s[0] ^= 1
			case "document":
				p[0] ^= 1
			case "descriptor":
				d[0] ^= 1
			case "oversize":
				p = make([]byte, 4097)
			case "cancelled":
				cancel()
			}
			proof, err := deliverRegistrationFrame(ctx, "/does-not-exist", privateProfile(uint32(os.Geteuid())), r, s, a, d, p)
			if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("invalid request minted proof")
			}
		})
	}
}
