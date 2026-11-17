package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// egressPair returns a runner-side and a Portal-side mux over an in-memory
// duplex connection, both running.
func egressPair(t *testing.T) (*EgressMux, *EgressMux) {
	t.Helper()
	a, b := net.Pipe()
	runner, portal := NewEgressMux(a, EgressRunner), NewEgressMux(b, EgressPortal)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, m := range []*EgressMux{runner, portal} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.Run(ctx) }()
	}
	t.Cleanup(func() { cancel(); a.Close(); b.Close(); wg.Wait() })
	return runner, portal
}

// rawFrame is a frame header and payload written without any of the mux's checks.
func rawFrame(t byte, call EgressCallID, payload []byte) []byte {
	out := make([]byte, egressFrameHeaderBytes)
	out[0] = t
	copy(out[4:20], call[:])
	binary.BigEndian.PutUint32(out[20:], uint32(len(payload)))
	return append(out, payload...)
}

// onlyHeader serves one header and then fails the test if anything more is read:
// a frame's payload must not be allocated or read after a refused header.
type onlyHeader struct {
	t      *testing.T
	header []byte
}

func (r *onlyHeader) Read(p []byte) (int, error) {
	if len(r.header) == 0 {
		r.t.Error("payload read after a refused header")
		return 0, io.EOF
	}
	n := copy(p, r.header)
	r.header = r.header[n:]
	return n, nil
}

func TestServiceEgressFramesBoundedBeforeAllocation(t *testing.T) {
	var id EgressCallID
	id[0] = 1
	limits := map[byte]int{EgressFrameAdmit: 8192, EgressFrameCommand: 4 + control.MaxCommandBytes + 64, EgressFrameRefusal: 1, EgressFrameChallenge: 33, EgressFrameProof: 4 + control.MaxNetworkHTTPCheckBytes + 64, EgressFrameNotice: 16384, EgressFrameNoticeResult: 256, EgressFrameOutcome: 11, EgressFrameClose: 1}
	for typ, limit := range limits {
		if limit > control.MaxCommandBytes+1024 {
			t.Fatalf("type %d exceeds the general bound", typ)
		}
		// One byte over is refused from the header alone.
		over := rawFrame(typ, id, nil)
		binary.BigEndian.PutUint32(over[20:], uint32(limit+1))
		if _, err := readEgressFrame(&onlyHeader{t: t, header: over}); !errors.Is(err, ErrEgressProtocol) {
			t.Fatalf("type %d: %d bytes accepted: %v", typ, limit+1, err)
		}
		huge := rawFrame(typ, id, nil)
		binary.BigEndian.PutUint32(huge[20:], 0xFFFFFFFF)
		if _, err := readEgressFrame(&onlyHeader{t: t, header: huge}); !errors.Is(err, ErrEgressProtocol) {
			t.Fatalf("type %d: 4 GiB accepted: %v", typ, err)
		}
		// Exactly the limit is read in full.
		frame, err := readEgressFrame(bytes.NewReader(rawFrame(typ, id, bytes.Repeat([]byte{7}, limit))))
		if err != nil || len(frame.Payload) != limit || frame.Type != typ || frame.Call != id {
			t.Fatalf("type %d: %d bytes refused: %v", typ, limit, err)
		}
		if _, err := encodeEgressFrame(typ, id, make([]byte, limit+1)); err == nil {
			t.Fatalf("type %d: encoder wrote an over-long frame", typ)
		}
	}
	for name, header := range map[string][]byte{
		"unknown type zero": rawFrame(0x00, id, nil),
		"unknown type 0x0a": rawFrame(0x0a, id, nil),
		"unknown type 0xff": rawFrame(0xff, id, nil),
		"reserved byte 1":   func() []byte { f := rawFrame(EgressFrameClose, id, nil); f[1] = 1; return f }(),
		"reserved byte 3":   func() []byte { f := rawFrame(EgressFrameClose, id, nil); f[3] = 1; return f }(),
	} {
		if _, err := readEgressFrame(&onlyHeader{t: t, header: header}); !errors.Is(err, ErrEgressProtocol) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// The open frame bounds its two lengths before allocating either.
	for name, mutate := range map[string]func([]byte){
		"command":    func(f []byte) { binary.BigEndian.PutUint32(f[40:], control.MaxCommandBytes+1) },
		"zero":       func(f []byte) { binary.BigEndian.PutUint32(f[40:], 0) },
		"huge":       func(f []byte) { binary.BigEndian.PutUint32(f[40:], 0xFFFFFFFF) },
		"wrongMagic": func(f []byte) { copy(f, "PAEGRX01") },
	} {
		header := make([]byte, 44)
		copy(header, ServiceEgressOpenMagic)
		mutate(header)
		if _, err := ReadEgressOpen(&onlyHeader{t: t, header: header}, false); !errors.Is(err, ErrEgressProtocol) {
			t.Fatalf("open %s accepted: %v", name, err)
		}
	}
	open, err := EncodeEgressOpen(bytes.Repeat([]byte("x"), 100), make([]byte, 64), bytes.Repeat([]byte("d"), 10))
	if err != nil {
		t.Fatal(err)
	}
	descriptorAt := 44 + 100 + 64
	over := bytes.Clone(open)
	binary.BigEndian.PutUint32(over[descriptorAt:], appplatform.MaxDescriptorBytes+1)
	if _, err := ReadEgressOpen(bytes.NewReader(over), false); !errors.Is(err, ErrEgressProtocol) {
		t.Fatalf("oversize descriptor accepted: %v", err)
	}
	if _, err := EncodeEgressOpen(nil, make([]byte, 64), []byte("d")); err == nil {
		t.Fatal("empty command encoded")
	}
	if _, err := EncodeEgressOpen([]byte("x"), make([]byte, 63), []byte("d")); err == nil {
		t.Fatal("short signature encoded")
	}
	// Payload codecs refuse lengths that disagree with their prefix.
	if _, _, err := DecodeEgressCommand([]byte{0, 0, 0, 5, 1, 2, 3}); err == nil {
		t.Fatal("truncated command accepted")
	}
	short := make([]byte, 4+10+64)
	binary.BigEndian.PutUint32(short, 11)
	if _, _, err := DecodeEgressCommand(short); err == nil {
		t.Fatal("length/size mismatch accepted")
	}
	long := make([]byte, 4+control.MaxNetworkHTTPCheckBytes+1+64)
	binary.BigEndian.PutUint32(long, control.MaxNetworkHTTPCheckBytes+1)
	if _, _, err := DecodeEgressProof(long); err == nil {
		t.Fatal("over-long proof accepted")
	}
}

func TestServiceEgressUnknownFrameClosesSession(t *testing.T) {
	var known, other EgressCallID
	known[0], other[0] = 1, 2
	nonce := make([]byte, 33)
	nonce[0] = 1
	admit := mustAdmit(t)
	type step struct {
		name string
		do   func(w io.Writer)
	}
	for _, tc := range []step{
		{"unknown type", func(w io.Writer) { w.Write(rawFrame(0x42, known, nil)) }},
		{"over-length", func(w io.Writer) {
			f := rawFrame(EgressFrameAdmit, known, nil)
			binary.BigEndian.PutUint32(f[20:], 8193)
			w.Write(f)
		}},
		{"wrong direction", func(w io.Writer) { w.Write(rawFrame(EgressFrameCommand, known, make([]byte, 69))) }},
		{"unknown call", func(w io.Writer) {
			w.Write(rawFrame(EgressFrameAdmit, known, admit))
			w.Write(rawFrame(EgressFrameChallenge, other, nonce))
		}},
		{"zero call id", func(w io.Writer) { w.Write(rawFrame(EgressFrameAdmit, EgressCallID{}, admit)) }},
		{"duplicate admit", func(w io.Writer) {
			w.Write(rawFrame(EgressFrameAdmit, known, admit))
			w.Write(rawFrame(EgressFrameAdmit, known, admit))
		}},
		{"challenge out of order", func(w io.Writer) {
			w.Write(rawFrame(EgressFrameAdmit, known, admit))
			n := bytes.Clone(nonce)
			n[0] = 2
			w.Write(rawFrame(EgressFrameChallenge, known, n))
		}},
		{"challenge repeated", func(w io.Writer) {
			w.Write(rawFrame(EgressFrameAdmit, known, admit))
			w.Write(rawFrame(EgressFrameChallenge, known, nonce))
			w.Write(rawFrame(EgressFrameChallenge, known, nonce))
		}},
		{"too many challenges", func(w io.Writer) {
			w.Write(rawFrame(EgressFrameAdmit, known, admit))
			for i := byte(1); i <= 5; i++ {
				n := bytes.Clone(nonce)
				n[0] = i
				w.Write(rawFrame(EgressFrameChallenge, known, n))
			}
		}},
		{"close with a call id", func(w io.Writer) { w.Write(rawFrame(EgressFrameClose, known, []byte{1})) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			portal := NewEgressMux(b, EgressPortal)
			result := make(chan error, 1)
			go func() { result <- portal.Run(context.Background()) }()
			// Drain what the Portal side may write back (a close frame).
			go io.Copy(io.Discard, a)
			go tc.do(a)
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("session survived")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("session not closed")
			}
			select {
			case <-portal.Done():
			default:
				t.Fatal("Done is not closed")
			}
			// Every call and every accept is released by the close.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for {
				call, err := portal.Accept(ctx)
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("Accept did not observe the closed session")
					}
					break
				}
				if _, err := call.Recv(ctx); errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("Recv did not observe the closed session")
				}
			}
		})
	}
	t.Run("late answer to an abandoned call is dropped", func(t *testing.T) {
		runner, portal := egressPair(t)
		call, err := runner.NewCall()
		if err != nil {
			t.Fatal(err)
		}
		if err := call.Send(EgressFrameAdmit, admit); err != nil {
			t.Fatal(err)
		}
		accepted, err := portal.Accept(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		call.Finish() // the runner gives up
		if err := accepted.Send(EgressFrameRefusal, []byte{EgressRefusalDenied}); err != nil {
			t.Fatal(err)
		}
		next, err := runner.NewCall()
		if err != nil {
			t.Fatal(err)
		}
		if err := next.Send(EgressFrameAdmit, admit); err != nil {
			t.Fatal(err)
		}
		if _, err := portal.Accept(context.Background()); err != nil {
			t.Fatal("a late frame for an abandoned call closed the session:", err)
		}
		select {
		case <-runner.Done():
			t.Fatal("runner session closed")
		default:
		}
	})
	t.Run("call id is never reused", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		portal := NewEgressMux(b, EgressPortal)
		go io.Copy(io.Discard, a)
		go func() {
			a.Write(rawFrame(EgressFrameAdmit, known, admit))
		}()
		go portal.Run(context.Background())
		call, err := portal.Accept(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		call.Finish()
		a.Write(rawFrame(EgressFrameAdmit, known, admit))
		select {
		case <-portal.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("reused call id accepted")
		}
	})
	t.Run("peer close frame ends the session with its reason", func(t *testing.T) {
		runner, portal := egressPair(t)
		portal.Close(EgressCloseDrift)
		select {
		case <-runner.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("close frame not honoured")
		}
		if runner.PeerReason() != EgressCloseDrift || !errors.Is(runner.Err(), ErrEgressClosed) {
			t.Fatalf("reason %d err %v", runner.PeerReason(), runner.Err())
		}
		if _, err := runner.NewCall(); err == nil {
			t.Fatal("call started on a closed session")
		}
	})
	t.Run("sender enforces direction and order", func(t *testing.T) {
		runner, portal := egressPair(t)
		call, _ := runner.NewCall()
		if err := call.Send(EgressFrameCommand, make([]byte, 69)); err == nil {
			t.Fatal("runner sent a Portal frame")
		}
		if err := call.Send(EgressFrameProof, make([]byte, 69)); err == nil {
			t.Fatal("proof sent by the runner")
		}
		if err := call.Send(EgressFrameChallenge, EncodeEgressChallenge(2, [32]byte{})); err == nil {
			t.Fatal("challenge 2 sent before challenge 1")
		}
		if err := call.Send(EgressFrameAdmit, admit); err != nil {
			t.Fatal(err)
		}
		accepted, err := portal.Accept(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := accepted.Send(EgressFrameProof, make([]byte, 69)); err == nil {
			t.Fatal("proof sent without a challenge")
		}
		if err := accepted.Send(EgressFrameAdmit, admit); err == nil {
			t.Fatal("Portal sent a runner frame")
		}
	})
}

func mustAdmit(t *testing.T) []byte {
	t.Helper()
	sum := sha256.Sum256(nil)
	out, err := EncodeEgressAdmit(EgressAdmit{Method: "GET", URL: "https://api.example.com/v1/x", BodySHA256: hexOf(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

// echoCode maps a payload marker to a refusal code so a caller can tell its own
// answer from another call's.
func echoCode(marker byte) byte {
	if code := marker%7 + 1; code != 4 {
		return code
	}
	return EgressRefusalBusy
}

func TestServiceEgressConcurrentCallsDoNotInterleaveWrites(t *testing.T) {
	runner, portal := egressPair(t)
	const callers, rounds = 8, 25
	var senders sync.WaitGroup
	for i := 0; i < callers; i++ {
		senders.Add(1)
		go func() {
			defer senders.Done()
			for r := 0; r < rounds; r++ {
				call, err := runner.NewCall()
				if err != nil {
					t.Error(err)
					return
				}
				// A notice that fills a large frame, and its answer, per round.
				payload := bytes.Repeat([]byte{byte(i + 1)}, 1+r*600)
				if len(payload) > 16384 {
					payload = payload[:16384]
				}
				if err := call.Send(EgressFrameNotice, payload); err != nil {
					t.Error(err)
					call.Finish()
					return
				}
				frame, err := call.Recv(context.Background())
				if err != nil || frame.Type != EgressFrameRefusal || len(frame.Payload) != 1 || frame.Payload[0] != echoCode(byte(i+1)) {
					t.Errorf("answer interleaved or lost: %v %+v", err, frame)
				}
				call.Finish()
			}
		}()
	}
	served := make(chan int, 1)
	go func() {
		count := 0
		for count < callers*rounds {
			call, err := portal.Accept(context.Background())
			if err != nil {
				break
			}
			frame, err := call.Recv(context.Background())
			if err != nil || frame.Type != EgressFrameNotice || len(frame.Payload) == 0 {
				t.Errorf("bad frame %v", err)
				break
			}
			marker := frame.Payload[0]
			for _, b := range frame.Payload {
				if b != marker {
					t.Error("payload bytes of two frames interleaved")
					break
				}
			}
			// The refusal code carries the marker back so the caller can check it.
			err = call.Send(EgressFrameRefusal, []byte{echoCode(marker)})
			call.Finish()
			if err != nil {
				t.Error(err)
				break
			}
			count++
		}
		served <- count
	}()
	senders.Wait()
	select {
	case n := <-served:
		if n != callers*rounds {
			t.Fatalf("served %d", n)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("calls did not complete")
	}
}

func TestServiceEgressOpenAckBindsFrameDigest(t *testing.T) {
	raw, signature, descriptor := bytes.Repeat([]byte("r"), 300), bytes.Repeat([]byte("s"), 64), bytes.Repeat([]byte("d"), 2000)
	frame, err := EncodeEgressOpen(raw, signature, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := EncodeEgressOpen(raw, signature, descriptor)
	if bytes.Equal(frame, other) {
		t.Fatal("two opens of the same session are identical: no per-frame nonce")
	}
	open, err := ReadEgressOpen(bytes.NewReader(frame), false)
	if err != nil || !bytes.Equal(open.Raw, raw) || !bytes.Equal(open.Signature, signature) || !bytes.Equal(open.Descriptor, descriptor) {
		t.Fatal("open frame did not round-trip", err)
	}
	if open.FrameSHA256 != sha256.Sum256(frame) {
		t.Fatal("digest is not over the exact frame bytes")
	}
	consumed, err := ReadEgressOpen(bytes.NewReader(frame[8:]), true)
	if err != nil || consumed.FrameSHA256 != open.FrameSHA256 {
		t.Fatal("the magic the dispatcher consumed must still be part of the digest", err)
	}
	if _, err := ReadEgressOpen(bytes.NewReader(append(bytes.Clone(frame), 0)[:len(frame)-1]), false); err == nil {
		t.Fatal("truncated open accepted")
	}
	for _, status := range []byte{EgressAckOpen, EgressAckRefused} {
		ack := EncodeEgressAck(open.FrameSHA256, status)
		if got, err := ParseEgressAck(ack, open.FrameSHA256); err != nil || got != status {
			t.Fatalf("ack %d: %v %d", status, err, got)
		}
	}
	good := EncodeEgressAck(open.FrameSHA256, EgressAckOpen)
	otherSHA := sha256.Sum256(other)
	for name, ack := range map[string][]byte{
		"another frame":  EncodeEgressAck(otherSHA, EgressAckOpen),
		"status zero":    EncodeEgressAck(open.FrameSHA256, 0),
		"status three":   EncodeEgressAck(open.FrameSHA256, 3),
		"wrong magic":    append([]byte("PAEGRB01"), good[8:]...),
		"short":          good[:40],
		"long":           append(bytes.Clone(good), 0),
		"flipped digest": func() []byte { b := bytes.Clone(good); b[20] ^= 1; return b }(),
	} {
		if _, err := ParseEgressAck(ack, open.FrameSHA256); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestServiceEgressPayloadCodecsAreClosed(t *testing.T) {
	admit := mustAdmit(t)
	if a, err := DecodeEgressAdmit(admit); err != nil || a.Method != "GET" {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"unknown field": `{"method":"GET","url":"https://a.example.com/x","accept":"","contentType":"","bodyBytes":0,"bodySha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","x":1}`,
		"whitespace":    `{"method":"GET", "url":"https://a.example.com/x","accept":"","contentType":"","bodyBytes":0,"bodySha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`,
		"bad method":    `{"method":"CONNECT","url":"https://a.example.com/x","accept":"","contentType":"","bodyBytes":0,"bodySha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`,
		"big body":      `{"method":"GET","url":"https://a.example.com/x","accept":"","contentType":"","bodyBytes":65537,"bodySha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`,
		"negative":      `{"method":"GET","url":"https://a.example.com/x","accept":"","contentType":"","bodyBytes":-1,"bodySha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`,
		"upper digest":  `{"method":"GET","url":"https://a.example.com/x","accept":"","contentType":"","bodyBytes":0,"bodySha256":"E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855"}`,
		"duplicate":     `{"method":"GET","method":"GET","url":"https://a.example.com/x","accept":"","contentType":"","bodyBytes":0,"bodySha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`,
	} {
		if _, err := DecodeEgressAdmit([]byte(raw)); err == nil {
			t.Fatalf("admit %s accepted", name)
		}
	}
	for _, o := range []EgressOutcome{{Class: 1, Status: 200, ResponseBytes: 12, RetryAfter: -1}, {Class: 5, RetryAfter: 30}, {Class: 4, RetryAfter: -1}} {
		raw, err := EncodeEgressOutcome(o)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := DecodeEgressOutcome(raw); err != nil || got != o {
			t.Fatalf("outcome %+v: %+v %v", o, got, err)
		}
	}
	for _, o := range []EgressOutcome{{Class: 0, RetryAfter: -1}, {Class: 6, RetryAfter: -1}, {Class: 1, RetryAfter: -2}, {Class: 1, RetryAfter: 86401}} {
		if _, err := EncodeEgressOutcome(o); err == nil {
			t.Fatalf("outcome %+v encoded", o)
		}
	}
	if _, err := DecodeEgressOutcome(make([]byte, 10)); err == nil {
		t.Fatal("short outcome accepted")
	}
	if code, err := DecodeEgressRefusal([]byte{EgressRefusalBusy}); err != nil || code != EgressRefusalBusy {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, {0}, {4}, {8}, {2, 2}} {
		if _, err := DecodeEgressRefusal(bad); err == nil {
			t.Fatalf("refusal %v accepted", bad)
		}
	}
	seq, nonce, err := DecodeEgressChallenge(EncodeEgressChallenge(3, [32]byte{9}))
	if err != nil || seq != 3 || nonce[0] != 9 {
		t.Fatal(err)
	}
	for _, bad := range []byte{0, 5} {
		if _, _, err := DecodeEgressChallenge(EncodeEgressChallenge(bad, [32]byte{})); err == nil {
			t.Fatalf("challenge %d accepted", bad)
		}
	}
	result, _ := EncodeEgressNoticeResult(EgressNoticeResult{EventID: "01HX", Created: true})
	if string(result) != `{"eventId":"01HX","created":true}` {
		t.Fatal(string(result))
	}
	if got, err := DecodeEgressNoticeResult(result); err != nil || got.EventID != "01HX" || !got.Created {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"eventId":"","created":true}`, `{"eventId":"a","created":true,"x":1}`, `{"created":true,"eventId":"a"}`, ``} {
		if _, err := DecodeEgressNoticeResult([]byte(bad)); err == nil {
			t.Fatalf("notice result %q accepted", bad)
		}
	}
	if _, err := DecodeEgressNotice([]byte(`{"title":"t","occurrenceId":"o","message":"m","severity":"info"}`)); err == nil {
		t.Fatal("non-canonical notice accepted")
	}
	if _, err := DecodeEgressNotice([]byte(`{"occurrenceId":"o","title":"t","message":"m","severity":"info"}`)); err != nil {
		t.Fatal(err)
	}
}

// hostilePeer runs a mux of the given role against a raw peer that the test
// writes frames to by hand. For the runner role a call is already started
// (kind is EgressFrameAdmit or EgressFrameNotice) and its ID returned; for
// Portal the returned ID is the one the peer must start itself.
func hostilePeer(t *testing.T, role EgressRole, kind byte) (mux *EgressMux, peer net.Conn, id EgressCallID, result chan error) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	mux = NewEgressMux(a, role)
	go io.Copy(io.Discard, b)
	result = make(chan error, 1)
	go func() { result <- mux.Run(context.Background()) }()
	if role == EgressRunner {
		call, err := mux.NewCall()
		if err != nil {
			t.Fatal(err)
		}
		payload := mustAdmit(t)
		if kind == EgressFrameNotice {
			payload = []byte(`{}`)
		}
		if err := call.Send(kind, payload); err != nil {
			t.Fatal(err)
		}
		id = call.ID()
	} else {
		id[0] = 7
		if _, err := b.Write(rawFrame(kind, id, startPayload(t, kind))); err != nil {
			t.Fatal(err)
		}
	}
	return mux, b, id, result
}

func startPayload(t *testing.T, kind byte) []byte {
	if kind == EgressFrameNotice {
		return []byte(`{}`)
	}
	return mustAdmit(t)
}

func expectClosed(t *testing.T, mux *EgressMux, result chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("session survived")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session not closed")
	}
	select {
	case <-mux.Done():
	default:
		t.Fatal("Done is not closed")
	}
}

// A hostile peer can only close the session: a frame below its type's minimum
// length is a protocol violation on both roles, never a panic.
func TestServiceEgressShortFramesCloseSessionWithoutPanic(t *testing.T) {
	type tc struct {
		role EgressRole
		typ  byte
		kind byte // the start frame of the live call
		min  int
	}
	cases := []tc{
		{EgressPortal, EgressFrameAdmit, EgressFrameAdmit, 1},
		{EgressPortal, EgressFrameNotice, EgressFrameAdmit, 1},
		{EgressPortal, EgressFrameChallenge, EgressFrameAdmit, 33},
		{EgressPortal, EgressFrameOutcome, EgressFrameAdmit, 11},
		{EgressRunner, EgressFrameCommand, EgressFrameAdmit, 4 + 1 + 64},
		{EgressRunner, EgressFrameRefusal, EgressFrameAdmit, 1},
		{EgressRunner, EgressFrameProof, EgressFrameAdmit, 4 + 1 + 64},
		{EgressRunner, EgressFrameNoticeResult, EgressFrameNotice, 1},
	}
	for _, c := range cases {
		lengths := []int{0}
		if c.min > 1 {
			lengths = append(lengths, c.min-1)
		}
		for _, n := range lengths {
			t.Run(fmt.Sprintf("role%d/type%d/len%d", c.role, c.typ, n), func(t *testing.T) {
				mux, peer, id, result := hostilePeer(t, c.role, c.kind)
				if c.role == EgressPortal && (c.typ == EgressFrameAdmit || c.typ == EgressFrameNotice) {
					// A start frame is its own call: use a fresh ID.
					id[0] = 9
				}
				peer.Write(rawFrame(c.typ, id, make([]byte, n)))
				expectClosed(t, mux, result)
			})
		}
	}
}

// A second answer on a live call is an out-of-order frame, not a queue entry.
func TestServiceEgressRunnerRejectsSecondAnswer(t *testing.T) {
	command := make([]byte, 4+1+64)
	binary.BigEndian.PutUint32(command, 1)
	notice := []byte(`{"eventId":"e","created":true}`)
	for _, tc := range []struct {
		name   string
		kind   byte
		frames [][2]any
	}{
		{"command twice", EgressFrameAdmit, [][2]any{{EgressFrameCommand, command}, {EgressFrameCommand, command}}},
		{"refusal twice", EgressFrameAdmit, [][2]any{{EgressFrameRefusal, []byte{EgressRefusalDenied}}, {EgressFrameRefusal, []byte{EgressRefusalDenied}}}},
		{"command then refusal", EgressFrameAdmit, [][2]any{{EgressFrameCommand, command}, {EgressFrameRefusal, []byte{EgressRefusalDenied}}}},
		{"refusal then command", EgressFrameAdmit, [][2]any{{EgressFrameRefusal, []byte{EgressRefusalDenied}}, {EgressFrameCommand, command}}},
		{"notice result twice", EgressFrameNotice, [][2]any{{EgressFrameNoticeResult, notice}, {EgressFrameNoticeResult, notice}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, peer, id, result := hostilePeer(t, EgressRunner, tc.kind)
			for _, f := range tc.frames {
				peer.Write(rawFrame(f[0].(byte), id, f[1].([]byte)))
			}
			expectClosed(t, mux, result)
		})
	}
}
