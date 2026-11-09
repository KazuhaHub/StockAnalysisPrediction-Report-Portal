package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
)

const blobOperation = "40000000-0000-4000-8000-000000000004"

type blobHarness struct {
	t         *testing.T
	store     *imagestore.Store
	provider  *imageBlobProvider
	key       ed25519.PrivateKey
	authority control.Authority
}

func newBlobHarness(t *testing.T) *blobHarness {
	t.Helper()
	dir, err := os.MkdirTemp("", "blob-session-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	root := filepath.Join(dir, "images")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := imagestore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(public)
	authority := control.Authority{
		DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000002",
		ControlRevision: 3, PublicKey: public, Fingerprint: hex.EncodeToString(fingerprint[:]),
	}
	return &blobHarness{t: t, store: store, key: key, authority: authority, provider: &imageBlobProvider{
		store: store, enrollment: func(context.Context) (control.Authority, error) { return authority, nil },
	}}
}

// layerBlob is an uncompressed layer whose diff_id is its own digest.
func layerBlob(body []byte) control.ImageBlobPayload {
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	return control.ImageBlobPayload{
		Digest: digest, Kind: "layer", MediaType: "application/vnd.oci.image.layer.v1.tar",
		Size: strconv.Itoa(len(body)), ResumeOffset: "0", DiffID: digest, UncompressedBytes: strconv.Itoa(len(body)),
	}
}

func (h *blobHarness) command(payload control.ImageBlobPayload) ([]byte, []byte) {
	h.t.Helper()
	now := time.Now().UnixMilli()
	c := control.ImageBlobCommand{
		Format: control.ImageBlobFormat, DeploymentID: h.authority.DeploymentID, ControllerEpoch: h.authority.ControllerEpoch,
		ControlRevision: "3", InstallationID: "30000000-0000-4000-8000-000000000003", OperationID: blobOperation,
		IssuedMS: strconv.FormatInt(now-1000, 10), ExpiresMS: strconv.FormatInt(now+20000, 10), Payload: payload,
	}
	raw, sig, err := control.SignImageBlob(c, h.key, h.authority, now)
	if err != nil {
		h.t.Fatal(err)
	}
	return raw, sig
}

// connect serves one session on the runner side of an in-memory connection, after consuming the request magic the
// way the Linux endpoint does.
func (h *blobHarness) connect() net.Conn {
	// A loopback socket buffers like the real control socket; an in-memory pipe would block a refusal that the
	// runner sends while the sender is still writing.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	go func() {
		defer listener.Close()
		server, err := listener.Accept()
		if err != nil {
			return
		}
		defer server.Close()
		var magic [8]byte
		if _, err := io.ReadFull(server, magic[:]); err != nil || string(magic[:]) != imageBlobRequestMagic {
			return
		}
		h.provider.serve(context.Background(), server)
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		h.t.Fatal(err)
	}
	return client
}

func (h *blobHarness) send(payload control.ImageBlobPayload, body []byte) (ImageBlobResult, error) {
	raw, sig := h.command(payload)
	conn := h.connect()
	defer conn.Close()
	return SendImageBlob(context.Background(), conn, raw, sig, h.authority, ProveImageBlobWithKey(h.key), bytes.NewReader(body))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func TestBlobChannelStoresAMultiChunkBlob(t *testing.T) {
	h := newBlobHarness(t)
	body := randomBytes(2*ImageBlobChunkBytes + 12345)
	payload := layerBlob(body)
	result, err := h.send(payload, body)
	if err != nil || !result.Stored {
		t.Fatalf("send: %+v %v", result, err)
	}
	if !h.store.Has(blobOperation, payload.Digest) {
		t.Fatal("blob not stored")
	}
	// A second send of a stored blob needs no bytes.
	if result, err := h.send(payload, nil); err != nil || !result.Stored {
		t.Fatalf("resend of a stored blob: %+v %v", result, err)
	}
}

// A sender that dies mid-blob leaves a partial; a new session resumes at exactly the stored length.
func TestBlobChannelResumesAfterALostConnection(t *testing.T) {
	h := newBlobHarness(t)
	body := randomBytes(2*ImageBlobChunkBytes + 99)
	payload := layerBlob(body)
	raw, sig := h.command(payload)
	conn := h.connect()
	// The body fails after one full chunk, like a dropped upload.
	broken := io.MultiReader(bytes.NewReader(body[:ImageBlobChunkBytes]), errReader{})
	if _, err := SendImageBlob(context.Background(), conn, raw, sig, h.authority, ProveImageBlobWithKey(h.key), broken); !errors.Is(err, ErrImageBlobSession) {
		t.Fatalf("broken upload: %v", err)
	}
	conn.Close()
	time.Sleep(50 * time.Millisecond) // the runner notices the closed connection and keeps the partial
	// Resuming at the wrong place names the right one.
	wrong := payload
	wrong.ResumeOffset = "7"
	result, err := h.send(wrong, body[7:])
	if err != nil || result.Stored || result.ResumeAt != ImageBlobChunkBytes {
		t.Fatalf("wrong resume: %+v %v", result, err)
	}
	resume := payload
	resume.ResumeOffset = strconv.Itoa(ImageBlobChunkBytes)
	if result, err := h.send(resume, body[ImageBlobChunkBytes:]); err != nil || !result.Stored {
		t.Fatalf("resume: %+v %v", result, err)
	}
	if !h.store.Has(blobOperation, payload.Digest) {
		t.Fatal("resumed blob not stored")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("upload dropped") }

func TestBlobChannelRefusalsAreTypedAndStoreNothing(t *testing.T) {
	body := randomBytes(4096)
	for name, tc := range map[string]struct {
		payload func() control.ImageBlobPayload
		body    []byte
		want    imagestore.Refusal
	}{
		"bytes that are not the declared digest": {func() control.ImageBlobPayload { return layerBlob(body) }, randomBytes(4096), imagestore.RefusalDigestMismatch},
		"diff id that is not the tar's": {func() control.ImageBlobPayload {
			p := layerBlob(body)
			p.DiffID = strings.Repeat("a", 64)
			return p
		}, body, imagestore.RefusalDiffIDMismatch},
		"uncompressed size that is not the tar's": {func() control.ImageBlobPayload {
			p := layerBlob(body)
			p.UncompressedBytes = "4095"
			return p
		}, body, imagestore.RefusalUncompressedBound},
	} {
		t.Run(name, func(t *testing.T) {
			h := newBlobHarness(t)
			payload := tc.payload()
			// The sender cannot make bytes it does not have match; it sends what it has.
			result, err := h.send(payload, tc.body)
			if !errors.Is(err, ErrImageBlobRefused) || result.Refusal != tc.want {
				t.Fatalf("refusal %q err=%v, want %q", result.Refusal, err, tc.want)
			}
			if h.store.Has(blobOperation, payload.Digest) || h.store.Used() != 0 {
				t.Fatal("a refused blob is stored or accounted")
			}
		})
	}
}

func TestBlobChannelRefusesAZstdLayerBeforeAnyChunk(t *testing.T) {
	h := newBlobHarness(t)
	p := layerBlob(randomBytes(100))
	p.MediaType = "application/vnd.oci.image.layer.v1.tar+zstd"
	raw, sig, err := control.SignImageBlob(commandFor(h, p), h.key, h.authority, time.Now().UnixMilli())
	if err == nil {
		t.Fatalf("a zstd layer command was signed: %s %x", raw, sig)
	}
}

func commandFor(h *blobHarness, p control.ImageBlobPayload) control.ImageBlobCommand {
	now := time.Now().UnixMilli()
	return control.ImageBlobCommand{
		Format: control.ImageBlobFormat, DeploymentID: h.authority.DeploymentID, ControllerEpoch: h.authority.ControllerEpoch,
		ControlRevision: "3", InstallationID: "30000000-0000-4000-8000-000000000003", OperationID: blobOperation,
		IssuedMS: strconv.FormatInt(now-1000, 10), ExpiresMS: strconv.FormatInt(now+20000, 10), Payload: p,
	}
}

// rawSession speaks the protocol by hand, so a hostile sender can do what the real one never does.
type rawSession struct {
	t    *testing.T
	conn net.Conn
}

func (h *blobHarness) rawOpen(payload control.ImageBlobPayload) (*rawSession, byte, uint64) {
	h.t.Helper()
	raw, sig := h.command(payload)
	conn := h.connect()
	h.t.Cleanup(func() { conn.Close() })
	frame := append(binary.BigEndian.AppendUint32([]byte(imageBlobRequestMagic), uint32(len(raw))), raw...)
	if _, err := conn.Write(append(frame, sig...)); err != nil {
		h.t.Fatal(err)
	}
	var challenge [40]byte
	if _, err := io.ReadFull(conn, challenge[:]); err != nil {
		h.t.Fatal(err)
	}
	proof := control.SignImageBlobProof(raw, hex.EncodeToString(challenge[8:]), h.key)
	if _, err := conn.Write(append([]byte(imageBlobProofMagic), proof...)); err != nil {
		h.t.Fatal(err)
	}
	var position [17]byte
	if _, err := io.ReadFull(conn, position[:]); err != nil {
		h.t.Fatal(err)
	}
	return &rawSession{t: h.t, conn: conn}, position[8], binary.BigEndian.Uint64(position[9:])
}

func (r *rawSession) chunk(data []byte, sum []byte) {
	if sum == nil {
		s := sha256.Sum256(data)
		sum = s[:]
	}
	frame := append(binary.BigEndian.AppendUint32([]byte(imageBlobChunkMagic), uint32(len(data))), sum...)
	_, _ = r.conn.Write(append(frame, data...))
}

func (r *rawSession) answer() (byte, byte, bool) {
	var a [10]byte
	if _, err := io.ReadFull(r.conn, a[:]); err != nil {
		return 0, 0, false
	}
	return a[8], a[9], string(a[:8]) == imageBlobAnswerMagic
}

func TestBlobChannelRefusesTrafficBeyondTheDeclaredSize(t *testing.T) {
	h := newBlobHarness(t)
	body := randomBytes(1000)
	session, code, _ := h.rawOpen(layerBlob(body))
	if code != imageBlobReady {
		t.Fatalf("position %d", code)
	}
	session.chunk(randomBytes(1001), nil) // one byte past the declared size
	status, refusal, ok := session.answer()
	if !ok || status != imageBlobAnswerRefused || imageBlobRefusalFromCode(refusal) != imagestore.RefusalSizeBound {
		t.Fatalf("answer %d %d %v", status, refusal, ok)
	}
	if h.store.Used() != 0 {
		t.Fatal("an oversized sender left bytes accounted")
	}
}

func TestBlobChannelChunkChecksumFailureKeepsTheStoredPrefix(t *testing.T) {
	h := newBlobHarness(t)
	body := randomBytes(3000)
	payload := layerBlob(body)
	session, _, _ := h.rawOpen(payload)
	session.chunk(body[:1000], nil)
	session.chunk(body[1000:2000], bytes.Repeat([]byte{1}, 32)) // damaged in transit
	status, refusal, ok := session.answer()
	if !ok || status != imageBlobAnswerRefused || imageBlobRefusalFromCode(refusal) != imagestore.RefusalDigestMismatch {
		t.Fatalf("answer %d %d %v", status, refusal, ok)
	}
	time.Sleep(50 * time.Millisecond)
	resume := payload
	resume.ResumeOffset = "1000"
	if result, err := h.send(resume, body[1000:]); err != nil || !result.Stored {
		t.Fatalf("resume after damage: %+v %v", result, err)
	}
}

func TestBlobChannelClosesOnMalformedFramesAndStalls(t *testing.T) {
	old := imageBlobChunkTimeout
	imageBlobChunkTimeout = 150 * time.Millisecond
	t.Cleanup(func() { imageBlobChunkTimeout = old })
	body := randomBytes(1000)
	for name, act := range map[string]func(*rawSession){
		"a zero length chunk": func(s *rawSession) { s.chunk(nil, nil) },
		"a chunk over one MiB": func(s *rawSession) {
			frame := append(binary.BigEndian.AppendUint32([]byte(imageBlobChunkMagic), ImageBlobChunkBytes+1), make([]byte, 32)...)
			_, _ = s.conn.Write(frame)
		},
		"an unknown frame": func(s *rawSession) { _, _ = s.conn.Write([]byte("PRIX0001")) },
		"a stalled sender": func(s *rawSession) {},
	} {
		t.Run(name, func(t *testing.T) {
			h := newBlobHarness(t)
			session, code, _ := h.rawOpen(layerBlob(body))
			if code != imageBlobReady {
				t.Fatalf("position %d", code)
			}
			act(session)
			_ = session.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			// Whatever the runner says, it then closes: reading ends at EOF, not at the deadline.
			if _, err := io.Copy(io.Discard, session.conn); err != nil {
				t.Fatalf("the runner kept the session open: %v", err)
			}
			// The slot is free again.
			time.Sleep(50 * time.Millisecond)
			if result, err := h.send(layerBlob(body), body); err != nil || !result.Stored {
				t.Fatalf("session slot not released: %+v %v", result, err)
			}
		})
	}
}

// The command alone opens nothing: the sender must also answer a fresh nonce with a signature only the controller
// can make, so a captured command is useless on another connection.
func TestBlobChannelRefusesUnauthenticatedSessions(t *testing.T) {
	body := randomBytes(500)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	for name, prove := range map[string]ImageBlobProver{
		"a proof from another key": ProveImageBlobWithKey(otherKey),
		"a replayed proof": func(_ context.Context, raw []byte, _ string) ([]byte, error) {
			return control.SignImageBlobProof(raw, strings.Repeat("ab", 32), ed25519.PrivateKey(nil)), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newBlobHarness(t)
			raw, sig := h.command(layerBlob(body))
			// The sender's own check of the proof would refuse; send it anyway as a hostile peer would.
			client := h.connect()
			defer client.Close()
			frame := append(binary.BigEndian.AppendUint32([]byte(imageBlobRequestMagic), uint32(len(raw))), raw...)
			_, _ = client.Write(append(frame, sig...))
			var challenge [40]byte
			_, _ = io.ReadFull(client, challenge[:])
			var proof []byte
			if name == "a proof from another key" {
				proof = control.SignImageBlobProof(raw, hex.EncodeToString(challenge[8:]), otherKey)
			} else {
				proof = control.SignImageBlobProof(raw, strings.Repeat("ab", 32), h.key)
			}
			_ = prove
			_, _ = client.Write(append([]byte(imageBlobProofMagic), proof...))
			_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
			var b [1]byte
			if _, err := client.Read(b[:]); err == nil {
				t.Fatal("an unauthenticated session got a reply")
			}
			if h.store.Used() != 0 {
				t.Fatal("an unauthenticated session reserved scratch")
			}
		})
	}
	t.Run("a command signed by another key", func(t *testing.T) {
		h := newBlobHarness(t)
		other := newBlobHarness(t)
		raw, sig := other.command(layerBlob(body))
		conn := h.connect()
		defer conn.Close()
		if _, err := SendImageBlob(context.Background(), conn, raw, sig, h.authority, ProveImageBlobWithKey(h.key), bytes.NewReader(body)); err == nil {
			t.Fatal("a foreign command opened a session")
		}
	})
	t.Run("an expired command", func(t *testing.T) {
		h := newBlobHarness(t)
		c := commandFor(h, layerBlob(body))
		c.IssuedMS, c.ExpiresMS = "1000", "2000"
		if _, _, err := control.SignImageBlob(c, h.key, h.authority, time.Now().UnixMilli()); err == nil {
			t.Fatal("an expired command was signed")
		}
	})
}

func TestBlobChannelBoundsConcurrentSessionsAndDuplicateBlobs(t *testing.T) {
	h := newBlobHarness(t)
	a, b, c := randomBytes(100), randomBytes(101), randomBytes(102)
	_, codeA, _ := h.rawOpen(layerBlob(a))
	if codeA != imageBlobReady {
		t.Fatal("first session not ready")
	}
	if _, code, offset := h.rawOpen(layerBlob(a)); code != imageBlobRefused || imageBlobRefusalFromCode(byte(offset)) != imagestore.RefusalBusy {
		t.Fatalf("a duplicate upload of one blob: %d %d", code, offset)
	}
	if _, code, _ := h.rawOpen(layerBlob(b)); code != imageBlobReady {
		t.Fatal("second session not ready")
	}
	if _, code, offset := h.rawOpen(layerBlob(c)); code != imageBlobRefused || imageBlobRefusalFromCode(byte(offset)) != imagestore.RefusalBusy {
		t.Fatalf("a third session: %d %d", code, offset)
	}
}

// Uploading is staging only. The provider holds a scratch store and an enrollment lookup and nothing else, so no
// effect row can be written and no Docker call made from this channel.
func TestBlobProviderHoldsNoJournalOrNative(t *testing.T) {
	typ := reflect.TypeFor[imageBlobProvider]()
	if typ.NumField() != 2 || typ.Field(0).Name != "store" || typ.Field(1).Name != "enrollment" {
		t.Fatalf("the blob provider gained a capability: %v", typ)
	}
}
