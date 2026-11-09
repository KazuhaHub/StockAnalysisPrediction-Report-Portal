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
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
)

// The blob channel carries image blobs from Portal to the runner, in the pattern of the network stream channel but
// in the other direction: a magic switch on the control socket, a signed session-opening command, a fresh nonce
// challenge and proof, then bounded, individually checksummed chunks. Uploading is staging only: no effect row and
// no native call.
const (
	imageBlobRequestMagic   = "PRIB0001"
	imageBlobChallengeMagic = "PRIC0001"
	imageBlobProofMagic     = "PRIP0001"
	imageBlobPositionMagic  = "PRIO0001"
	imageBlobChunkMagic     = "PRIK0001"
	imageBlobEndMagic       = "PRIE0001"
	imageBlobAnswerMagic    = "PRIA0001"

	// ImageBlobChunkBytes is the largest chunk either side accepts.
	ImageBlobChunkBytes = 1 << 20
	// imageBlobSessionTimeout bounds a whole session, counted after the short control context is released.
	imageBlobSessionTimeout = 10 * time.Minute
)

// imageBlobChunkTimeout bounds the wait for each chunk, so a stalled sender cannot hold a session slot. A variable
// only so a test can shorten it.
var imageBlobChunkTimeout = 10 * time.Second

// Position codes, sent once the runner has opened the upload.
const (
	imageBlobReady    byte = 1
	imageBlobStored   byte = 2
	imageBlobResumeAt byte = 3
	imageBlobRefused  byte = 4
)

// Answer codes, sent after the end frame.
const (
	imageBlobAnswerStored  byte = 1
	imageBlobAnswerRefused byte = 4
)

var imageBlobRefusals = []imagestore.Refusal{
	imagestore.RefusalDigestMismatch, imagestore.RefusalDiffIDMismatch, imagestore.RefusalUncompressedBound,
	imagestore.RefusalSizeBound, imagestore.RefusalMediaType, imagestore.RefusalScratchFull, imagestore.RefusalBusy,
}

func imageBlobRefusalCode(r imagestore.Refusal) byte {
	for i, known := range imageBlobRefusals {
		if known == r {
			return byte(i + 1)
		}
	}
	return 0
}

func imageBlobRefusalFromCode(code byte) imagestore.Refusal {
	if code < 1 || int(code) > len(imageBlobRefusals) {
		return ""
	}
	return imageBlobRefusals[code-1]
}

var (
	// ErrImageBlobRefused is returned by the sender when the runner refuses a blob with a typed reason.
	ErrImageBlobRefused = errors.New("runner refused the image blob")
	// ErrImageBlobSession is any other failure of a session: the blob may be partly stored, and the sender resumes.
	ErrImageBlobSession = errors.New("image blob session failed")
)

// ImageBlobResult is the outcome of one session from the sender's side.
type ImageBlobResult struct {
	// Stored is true once the runner holds the complete, verified blob.
	Stored bool
	// ResumeAt, when non-zero with Stored false, is the offset the runner holds: the sender opens a new session
	// with a command that names it.
	ResumeAt int64
	// Refusal is the typed reason, when the runner refused the blob.
	Refusal imagestore.Refusal
}

// ImageBlobProver signs the runner's nonce for the exact command bytes. It runs inside the controller's key
// boundary; the transport never sees the key.
type ImageBlobProver func(ctx context.Context, raw []byte, nonceHex string) ([]byte, error)

// ProveImageBlobWithKey adapts a private key to an ImageBlobProver.
func ProveImageBlobWithKey(key ed25519.PrivateKey) ImageBlobProver {
	return func(_ context.Context, raw []byte, nonceHex string) ([]byte, error) {
		if len(key) != ed25519.PrivateKeySize {
			return nil, ErrImageBlobSession
		}
		return control.SignImageBlobProof(raw, nonceHex, key), nil
	}
}

type imageBlobProvider struct {
	store      *imagestore.Store
	enrollment func(context.Context) (control.Authority, error)
}

// SendImageBlob drives one session over an authenticated connection. body is positioned at the resume offset the
// command names and yields exactly the remaining bytes of the blob.
func SendImageBlob(parent context.Context, conn net.Conn, raw, signature []byte, a control.Authority, prove ImageBlobProver, body io.Reader) (ImageBlobResult, error) {
	var zero ImageBlobResult
	if parent == nil || conn == nil || prove == nil || body == nil || len(signature) != 64 || len(a.PublicKey) != ed25519.PublicKeySize {
		return zero, ErrImageBlobSession
	}
	verified, err := control.VerifyImageBlob(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, ErrImageBlobSession
	}
	declaration, _ := verified.Command().Declaration()
	ctx, cancel := context.WithTimeout(parent, imageBlobSessionTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	frame := make([]byte, 12, 12+len(raw)+len(signature))
	copy(frame, imageBlobRequestMagic)
	binary.BigEndian.PutUint32(frame[8:], uint32(len(raw)))
	frame = append(append(frame, raw...), signature...)
	if _, err := conn.Write(frame); err != nil {
		return zero, ErrImageBlobSession
	}
	var challenge [40]byte
	if _, err := io.ReadFull(conn, challenge[:]); err != nil || string(challenge[:8]) != imageBlobChallengeMagic {
		return zero, ErrImageBlobSession
	}
	proof, err := prove(ctx, raw, hex.EncodeToString(challenge[8:]))
	if err != nil || len(proof) != ed25519.SignatureSize || !control.VerifyImageBlobProof(raw, hex.EncodeToString(challenge[8:]), proof, a) {
		return zero, ErrImageBlobSession
	}
	if _, err := conn.Write(append([]byte(imageBlobProofMagic), proof...)); err != nil {
		return zero, ErrImageBlobSession
	}
	var position [17]byte
	if _, err := io.ReadFull(conn, position[:]); err != nil || string(position[:8]) != imageBlobPositionMagic {
		return zero, ErrImageBlobSession
	}
	offset := int64(binary.BigEndian.Uint64(position[9:]))
	switch position[8] {
	case imageBlobStored:
		return ImageBlobResult{Stored: offset == declaration.Blob.Size}, nil
	case imageBlobResumeAt:
		if offset < 0 || offset >= declaration.Blob.Size || offset == declaration.ResumeOffset {
			return zero, ErrImageBlobSession
		}
		return ImageBlobResult{ResumeAt: offset}, nil
	case imageBlobRefused:
		return ImageBlobResult{Refusal: imageBlobRefusalFromCode(byte(offset))}, ErrImageBlobRefused
	case imageBlobReady:
		if offset != declaration.ResumeOffset {
			return zero, ErrImageBlobSession
		}
	default:
		return zero, ErrImageBlobSession
	}
	// The runner rebuilds its hashers from what it stored, so a resumed sender cannot know the whole-blob digest from
	// what it sends: the end frame names the declared digest and the runner compares it with its own computation.
	buffer := make([]byte, ImageBlobChunkBytes)
	sent := declaration.ResumeOffset
	for sent < declaration.Blob.Size {
		n, readErr := io.ReadFull(body, buffer[:min(int64(len(buffer)), declaration.Blob.Size-sent)])
		if n > 0 {
			sum := sha256.Sum256(buffer[:n])
			head := make([]byte, 12, 12+32)
			copy(head, imageBlobChunkMagic)
			binary.BigEndian.PutUint32(head[8:], uint32(n))
			if _, err := conn.Write(append(append(head, sum[:]...), buffer[:n]...)); err != nil {
				return answeredRefusal(conn)
			}
			sent += int64(n)
		}
		if readErr != nil && sent < declaration.Blob.Size {
			return zero, ErrImageBlobSession
		}
	}
	end := make([]byte, 48)
	copy(end, imageBlobEndMagic)
	copy(end[8:40], mustDecodeDigest(declaration.Blob.Digest))
	binary.BigEndian.PutUint64(end[40:], uint64(declaration.Blob.Size))
	if _, err := conn.Write(end); err != nil {
		return zero, ErrImageBlobSession
	}
	var answer [10]byte
	if _, err := io.ReadFull(conn, answer[:]); err != nil || string(answer[:8]) != imageBlobAnswerMagic {
		return zero, ErrImageBlobSession
	}
	switch answer[8] {
	case imageBlobAnswerStored:
		return ImageBlobResult{Stored: true}, nil
	case imageBlobAnswerRefused:
		return ImageBlobResult{Refusal: imageBlobRefusalFromCode(answer[9])}, ErrImageBlobRefused
	}
	return zero, ErrImageBlobSession
}

// answeredRefusal reads the typed refusal a runner may have sent before it closed a session the sender was still
// writing to. Without one the failure is a plain session failure and the sender resumes.
func answeredRefusal(conn net.Conn) (ImageBlobResult, error) {
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var answer [10]byte
	if _, err := io.ReadFull(conn, answer[:]); err == nil && string(answer[:8]) == imageBlobAnswerMagic && answer[8] == imageBlobAnswerRefused {
		return ImageBlobResult{Refusal: imageBlobRefusalFromCode(answer[9])}, ErrImageBlobRefused
	}
	return ImageBlobResult{}, ErrImageBlobSession
}

func mustDecodeDigest(digest string) []byte {
	raw, _ := hex.DecodeString(digest)
	return raw
}

// serveImageBlob answers one session. The Linux endpoint has already verified the kernel peer and consumed the
// request magic. Every failure ends the connection without a reply, except typed refusals the sender can act on.
func (p *imageBlobProvider) serve(parent context.Context, conn net.Conn) {
	ctx, cancel := context.WithTimeout(parent, imageBlobSessionTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > control.MaxCommandBytes {
		return
	}
	raw, signature := make([]byte, length), make([]byte, 64)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, signature); err != nil {
		return
	}
	a, err := p.enrollment(ctx)
	if err != nil {
		return
	}
	verified, err := control.VerifyImageBlob(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return
	}
	command := verified.Command()
	declaration, ok := command.Declaration()
	if !ok {
		return
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return
	}
	if _, err := conn.Write(append([]byte(imageBlobChallengeMagic), nonce[:]...)); err != nil {
		return
	}
	var proof [72]byte
	if _, err := io.ReadFull(conn, proof[:]); err != nil || string(proof[:8]) != imageBlobProofMagic ||
		!control.VerifyImageBlobProof(raw, hex.EncodeToString(nonce[:]), proof[8:], a) {
		return
	}
	position := func(code byte, value uint64) bool {
		frame := make([]byte, 17)
		copy(frame, imageBlobPositionMagic)
		frame[8] = code
		binary.BigEndian.PutUint64(frame[9:], value)
		_, err := conn.Write(frame)
		return err == nil
	}
	upload, at, complete, refusal, err := p.store.Begin(command.OperationID, declaration.Blob, declaration.ResumeOffset)
	switch {
	case err != nil:
		return
	case refusal != imagestore.RefusalNone:
		position(imageBlobRefused, uint64(imageBlobRefusalCode(refusal)))
		return
	case complete:
		position(imageBlobStored, uint64(at))
		return
	case upload == nil:
		position(imageBlobResumeAt, uint64(at))
		return
	}
	finished := false
	defer func() {
		if !finished {
			// A lost connection is not a verdict on the bytes already stored: the partial stays for a resume.
			upload.Abort()
		}
	}()
	if !position(imageBlobReady, uint64(at)) {
		return
	}
	answer := func(code byte, refusal imagestore.Refusal) {
		frame := make([]byte, 10)
		copy(frame, imageBlobAnswerMagic)
		frame[8], frame[9] = code, imageBlobRefusalCode(refusal)
		_, _ = conn.Write(frame)
	}
	buffer := make([]byte, ImageBlobChunkBytes)
	for {
		_ = conn.SetReadDeadline(minTime(time.Now().Add(imageBlobChunkTimeout), deadline))
		var magic [8]byte
		if _, err := io.ReadFull(conn, magic[:]); err != nil {
			return
		}
		switch string(magic[:]) {
		case imageBlobChunkMagic:
			var head [36]byte
			if _, err := io.ReadFull(conn, head[:]); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint32(head[:4]))
			if n < 1 || n > ImageBlobChunkBytes || upload.Written()+int64(n) > declaration.Blob.Size {
				finished = refuseSession(upload, answer, imagestore.RefusalSizeBound)
				return
			}
			if _, err := io.ReadFull(conn, buffer[:n]); err != nil {
				return
			}
			if sum := sha256.Sum256(buffer[:n]); !bytes.Equal(sum[:], head[4:]) {
				// Damage in transit, not a verdict on the blob: the stored prefix stays valid for a resume.
				answer(imageBlobAnswerRefused, imagestore.RefusalDigestMismatch)
				return
			}
			if refusal := upload.Write(buffer[:n]); refusal != imagestore.RefusalNone {
				finished = true
				answer(imageBlobAnswerRefused, refusal)
				return
			}
		case imageBlobEndMagic:
			var end [40]byte
			if _, err := io.ReadFull(conn, end[:]); err != nil {
				return
			}
			total := int64(binary.BigEndian.Uint64(end[32:]))
			if refusal := upload.Commit(hex.EncodeToString(end[:32]), total); refusal != imagestore.RefusalNone {
				finished = true
				answer(imageBlobAnswerRefused, refusal)
				return
			}
			finished = true
			answer(imageBlobAnswerStored, imagestore.RefusalNone)
			return
		default:
			return
		}
	}
}

// refuseSession discards the partial and answers with a typed refusal. The partial is dropped because the sender
// broke the declared bound, which no resume can repair.
func refuseSession(upload *imagestore.Upload, answer func(byte, imagestore.Refusal), refusal imagestore.Refusal) bool {
	upload.Discard()
	answer(imageBlobAnswerRefused, refusal)
	return true
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
