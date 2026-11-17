package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// This file is the wire of a background egress session
// (control.ServiceEgressSessionFormat): the open and acknowledgement frames, and
// the multiplexed call frames that follow on the same connection. It has no
// build tag so Portal's controller side shares one codec with the runner. It
// decides no authority: the runner's registry verifies the signed session and
// Portal signs every call, as in the foreground network exchange.

const (
	// ServiceEgressOpenMagic starts a session: Portal -> runner.
	ServiceEgressOpenMagic = "PAEGRS01"
	// ServiceEgressAckMagic answers it: runner -> Portal.
	ServiceEgressAckMagic = "PAEGRA01"
	// ServiceEgressProfileQueryMagic asks a runner whether it can host sessions.
	// An older runner does not know it and closes the connection at EOF.
	ServiceEgressProfileQueryMagic = "PAEGRV01"
	// ServiceEgressProfileReplyMagic answers the profile query.
	ServiceEgressProfileReplyMagic = "PAEGRW01"
)

// Egress frame types. Direction is fixed per type; a frame in the other
// direction is a protocol violation.
const (
	EgressFrameAdmit        byte = 0x01 // runner -> Portal: a call wants to start
	EgressFrameCommand      byte = 0x02 // Portal -> runner: the signed request for that call
	EgressFrameRefusal      byte = 0x03 // Portal -> runner: the call (or notice) is refused
	EgressFrameChallenge    byte = 0x04 // runner -> Portal: an authority check, with a nonce
	EgressFrameProof        byte = 0x05 // Portal -> runner: the signed answer to it
	EgressFrameNotice       byte = 0x06 // runner -> Portal: a notification to publish
	EgressFrameNoticeResult byte = 0x07 // Portal -> runner: its outcome
	EgressFrameOutcome      byte = 0x08 // runner -> Portal: how the call ended, for audit
	EgressFrameClose        byte = 0x09 // either: the session ends
)

// Refusal codes (EgressFrameRefusal payload).
const (
	EgressRefusalInvalid     byte = 1
	EgressRefusalDenied      byte = 2
	EgressRefusalUnavailable byte = 3
	EgressRefusalBusy        byte = 5
	EgressRefusalConflict    byte = 6
	EgressRefusalCapacity    byte = 7
)

// Outcome classes (EgressFrameOutcome payload).
const (
	EgressOutcomeOK          byte = 1
	EgressOutcomeDenied      byte = 2
	EgressOutcomeUnavailable byte = 3
	EgressOutcomeUncertain   byte = 4
	EgressOutcomeBusy        byte = 5
)

// Close reasons (EgressFrameClose payload). They mirror the audit reasons that
// can be observed on the wire; expired, drift and the like are decided by the
// side that closes.
const (
	EgressCloseExpired        byte = 1
	EgressCloseRenewed        byte = 2
	EgressCloseDrift          byte = 3
	EgressCloseRestorePause   byte = 4
	EgressCloseEpoch          byte = 5
	EgressCloseDisabled       byte = 6
	EgressCloseConnectionLost byte = 7
	EgressCloseRefused        byte = 8
	EgressCloseShutdown       byte = 9
)

const (
	egressFrameHeaderBytes = 24
	// egressMaxPayload bounds every frame whatever its type.
	egressMaxPayload = control.MaxCommandBytes + 1024
	// EgressMaxChecks is the number of authority checks one call receives: the
	// executor's three plus the pool's own, as in the foreground exchange.
	EgressMaxChecks = 4
	// egressMaxCalls bounds the calls one session may have open at once: the
	// session concurrency (at most 4) plus notices, with room for calls that are
	// finishing. More is a protocol violation, not a queue.
	egressMaxCalls = 32
	// egressCallBuffer is the frames a call may hold unread. The longest legal
	// exchange has command, four proofs and a close in flight one at a time.
	egressCallBuffer     = 8
	egressTombstones     = 256
	egressTombstoneTTL   = time.Minute
	egressAdmitMaxBytes  = 8192
	egressNoticeMaxBytes = broker.MaxAppNoticeBytes
	egressResultMaxBytes = 256
	// EgressWriteTimeout bounds one frame write. It is a per-write deadline: the
	// connection never carries a whole-connection deadline, because a session
	// outlives any single call.
	EgressWriteTimeout = 5 * time.Second
)

// Errors a session reports.
var (
	// ErrEgressProtocol is any malformed, unknown, over-long, duplicate, unknown-call
	// or out-of-order frame. It closes the session.
	ErrEgressProtocol = errors.New("service egress protocol violation")
	// ErrEgressClosed means the session ended, by either side, before the call did.
	ErrEgressClosed = errors.New("service egress session closed")
)

// EgressCallID names one call on a session. It is chosen by the side that
// starts the call (the runner) and is never reused within a session.
type EgressCallID [16]byte

// NewEgressCallID returns a random call ID.
func NewEgressCallID() (EgressCallID, error) {
	var id EgressCallID
	_, err := rand.Read(id[:])
	return id, err
}

// EgressFrame is one decoded multiplexed frame.
type EgressFrame struct {
	Type    byte
	Call    EgressCallID
	Payload []byte
}

// egressFrameLimit is the payload bound of a frame type; ok is false for an
// unknown type.
func egressFrameLimit(t byte) (int, bool) {
	switch t {
	case EgressFrameAdmit:
		return egressAdmitMaxBytes, true
	case EgressFrameCommand:
		return 4 + control.MaxCommandBytes + 64, true
	case EgressFrameRefusal:
		return 1, true
	case EgressFrameChallenge:
		return 33, true
	case EgressFrameProof:
		return 4 + control.MaxNetworkHTTPCheckBytes + 64, true
	case EgressFrameNotice:
		return egressNoticeMaxBytes, true
	case EgressFrameNoticeResult:
		return egressResultMaxBytes, true
	case EgressFrameOutcome:
		return 11, true
	case EgressFrameClose:
		return 1, true
	}
	return 0, false
}

// readEgressFrame reads one frame. The type and the length are checked against
// the type's bound before any payload is allocated or read.
func readEgressFrame(r io.Reader) (EgressFrame, error) {
	var header [egressFrameHeaderBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return EgressFrame{}, err
	}
	limit, known := egressFrameLimit(header[0])
	length := binary.BigEndian.Uint32(header[20:])
	if !known || header[1] != 0 || header[2] != 0 || header[3] != 0 || length > uint32(limit) || length > egressMaxPayload {
		return EgressFrame{}, ErrEgressProtocol
	}
	frame := EgressFrame{Type: header[0], Payload: make([]byte, int(length))}
	copy(frame.Call[:], header[4:20])
	if _, err := io.ReadFull(r, frame.Payload); err != nil {
		return EgressFrame{}, err
	}
	return frame, nil
}

// encodeEgressFrame is the single encoding of a frame, written with one Write.
func encodeEgressFrame(t byte, call EgressCallID, payload []byte) ([]byte, error) {
	limit, known := egressFrameLimit(t)
	if !known || len(payload) > limit {
		return nil, ErrEgressProtocol
	}
	out := make([]byte, egressFrameHeaderBytes, egressFrameHeaderBytes+len(payload))
	out[0] = t
	copy(out[4:20], call[:])
	binary.BigEndian.PutUint32(out[20:], uint32(len(payload)))
	return append(out, payload...), nil
}

// EgressRole is which end of a session a mux is.
type EgressRole byte

const (
	// EgressRunner starts calls (admit, notice) and receives Portal's answers.
	EgressRunner EgressRole = 1
	// EgressPortal receives calls and answers them.
	EgressPortal EgressRole = 2
)

func (r EgressRole) receives(t byte) bool {
	switch t {
	case EgressFrameClose:
		return true
	case EgressFrameCommand, EgressFrameRefusal, EgressFrameProof, EgressFrameNoticeResult:
		return r == EgressRunner
	case EgressFrameAdmit, EgressFrameChallenge, EgressFrameNotice, EgressFrameOutcome:
		return r == EgressPortal
	}
	return false
}

func (r EgressRole) sends(t byte) bool {
	other := EgressRunner
	if r == EgressRunner {
		other = EgressPortal
	}
	return other.receives(t) || t == EgressFrameClose
}

// EgressMux multiplexes calls over one connection. It owns the connection's
// writes behind one mutex, sets only per-write deadlines, and treats any
// protocol violation as the end of the whole session.
type EgressMux struct {
	conn net.Conn
	role EgressRole
	now  func() time.Time

	wmu sync.Mutex

	mu         sync.Mutex
	calls      map[EgressCallID]*EgressCall
	finished   map[EgressCallID]time.Time
	finishedQ  []EgressCallID
	incoming   chan *EgressCall
	closeOnce  sync.Once
	done       chan struct{}
	err        error
	peerReason byte
}

// NewEgressMux wraps an authenticated connection. The caller must run Run.
func NewEgressMux(conn net.Conn, role EgressRole) *EgressMux {
	return &EgressMux{conn: conn, role: role, now: time.Now, calls: make(map[EgressCallID]*EgressCall), finished: make(map[EgressCallID]time.Time), incoming: make(chan *EgressCall, egressMaxCalls), done: make(chan struct{})}
}

// Done is closed when the session has ended.
func (m *EgressMux) Done() <-chan struct{} { return m.done }

// Err is why the session ended: ErrEgressProtocol, ErrEgressClosed (a peer
// close, see PeerReason) or the connection error. Nil while it is open.
func (m *EgressMux) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// PeerReason is the reason in the close frame the peer sent, or 0.
func (m *EgressMux) PeerReason() byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peerReason
}

func (m *EgressMux) fail(err error) {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.err = err
		m.mu.Unlock()
		close(m.done)
		_ = m.conn.Close()
	})
}

// Close sends a close frame with reason on a best-effort basis, then ends the
// session. The frame is bounded by the write deadline and never blocks on a
// stuck peer for longer than that.
func (m *EgressMux) Close(reason byte) {
	select {
	case <-m.done:
		return
	default:
	}
	_ = m.write(EgressFrameClose, EgressCallID{}, []byte{reason})
	m.fail(ErrEgressClosed)
}

// Run reads frames until the session ends and returns why. It stops when ctx is
// done. It must run exactly once.
func (m *EgressMux) Run(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { m.fail(ctx.Err()) })
	defer stop()
	for {
		frame, err := readEgressFrame(m.conn)
		if err != nil {
			select {
			case <-m.done:
			default:
				if errors.Is(err, ErrEgressProtocol) {
					m.fail(err)
				} else {
					m.fail(ErrEgressClosed)
				}
			}
			return m.Err()
		}
		if err := m.dispatch(frame); err != nil {
			m.fail(err)
			return m.Err()
		}
	}
}

func (m *EgressMux) dispatch(f EgressFrame) error {
	if f.Type == EgressFrameClose {
		if f.Call != (EgressCallID{}) || len(f.Payload) != 1 {
			return ErrEgressProtocol
		}
		m.mu.Lock()
		m.peerReason = f.Payload[0]
		m.mu.Unlock()
		return ErrEgressClosed
	}
	if f.Call == (EgressCallID{}) || !m.role.receives(f.Type) {
		return ErrEgressProtocol
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	call := m.calls[f.Call]
	if call == nil {
		if _, finished := m.finished[f.Call]; finished {
			if f.Type == EgressFrameAdmit || f.Type == EgressFrameNotice {
				return ErrEgressProtocol // a call ID is never reused
			}
			// A late answer to a call its owner already abandoned.
			return nil
		}
		if m.role != EgressPortal || (f.Type != EgressFrameAdmit && f.Type != EgressFrameNotice) || len(m.calls) >= egressMaxCalls {
			return ErrEgressProtocol
		}
		call = &EgressCall{mux: m, id: f.Call, in: make(chan EgressFrame, egressCallBuffer)}
		select {
		case m.incoming <- call:
		default:
			return ErrEgressProtocol
		}
		m.calls[f.Call] = call
	} else if f.Type == EgressFrameAdmit || f.Type == EgressFrameNotice {
		return ErrEgressProtocol // duplicate start
	}
	switch f.Type {
	case EgressFrameChallenge:
		if f.Payload[0] != call.rxSeq+1 || call.rxSeq >= EgressMaxChecks {
			return ErrEgressProtocol
		}
		call.rxSeq++
	case EgressFrameProof:
		if call.txSeq <= call.proofs {
			return ErrEgressProtocol // a proof nobody asked for
		}
		call.proofs++
	}
	select {
	case call.in <- f:
		return nil
	default:
		return ErrEgressProtocol
	}
}

// Accept returns the next call the peer started (Portal role). It returns an
// error when the session ends or ctx is done.
func (m *EgressMux) Accept(ctx context.Context) (*EgressCall, error) {
	select {
	case call := <-m.incoming:
		return call, nil
	case <-m.done:
		return nil, m.sessionErr()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *EgressMux) sessionErr() error {
	if err := m.Err(); err != nil {
		return err
	}
	return ErrEgressClosed
}

// NewCall starts a call (runner role). It fails when the session already has
// the most calls it allows, which the caller reports as busy.
func (m *EgressMux) NewCall() (*EgressCall, error) {
	select {
	case <-m.done:
		return nil, m.sessionErr()
	default:
	}
	if m.role != EgressRunner {
		return nil, ErrEgressProtocol
	}
	id, err := NewEgressCallID()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) >= egressMaxCalls {
		return nil, broker.ErrBusy
	}
	call := &EgressCall{mux: m, id: id, in: make(chan EgressFrame, egressCallBuffer)}
	m.calls[id] = call
	return call, nil
}

func (m *EgressMux) write(t byte, id EgressCallID, payload []byte) error {
	frame, err := encodeEgressFrame(t, id, payload)
	if err != nil {
		return err
	}
	m.wmu.Lock()
	defer m.wmu.Unlock()
	// Per-write deadline only: SetDeadline would also cut the shared read side.
	if err := m.conn.SetWriteDeadline(m.now().Add(EgressWriteTimeout)); err != nil {
		m.fail(ErrEgressClosed)
		return ErrEgressClosed
	}
	if _, err := m.conn.Write(frame); err != nil {
		m.fail(ErrEgressClosed)
		return ErrEgressClosed
	}
	return nil
}

// EgressCall is one call on a session.
type EgressCall struct {
	mux *EgressMux
	id  EgressCallID
	in  chan EgressFrame

	// Challenge accounting, guarded by mux.mu: txSeq/rxSeq are the last challenge
	// sequence sent/received, proofs the proofs received (runner) or sent (Portal).
	txSeq, rxSeq, proofs byte
	finished             bool
}

// ID is the call's identifier.
func (c *EgressCall) ID() EgressCallID { return c.id }

// Send writes one frame of this call. Only the types this side may send are
// accepted, challenges must run 1..EgressMaxChecks in order, and a proof needs
// a challenge outstanding.
func (c *EgressCall) Send(t byte, payload []byte) error {
	m := c.mux
	if !m.role.sends(t) || t == EgressFrameClose {
		return ErrEgressProtocol
	}
	m.mu.Lock()
	switch {
	case c.finished:
		m.mu.Unlock()
		return ErrEgressClosed
	case t == EgressFrameChallenge:
		if len(payload) != 33 || payload[0] != c.txSeq+1 || c.txSeq >= EgressMaxChecks {
			m.mu.Unlock()
			return ErrEgressProtocol
		}
		c.txSeq++
	case t == EgressFrameProof && m.role == EgressPortal:
		if c.rxSeq <= c.proofs {
			m.mu.Unlock()
			return ErrEgressProtocol
		}
		c.proofs++
	}
	m.mu.Unlock()
	select {
	case <-m.done:
		return m.sessionErr()
	default:
	}
	return m.write(t, c.id, payload)
}

// Recv returns the next frame of this call. It ends with ctx, the session, or
// the frames the call was sent.
func (c *EgressCall) Recv(ctx context.Context) (EgressFrame, error) {
	select {
	case f := <-c.in:
		return f, nil
	default:
	}
	select {
	case f := <-c.in:
		return f, nil
	case <-c.mux.done:
		return EgressFrame{}, c.mux.sessionErr()
	case <-ctx.Done():
		return EgressFrame{}, ctx.Err()
	}
}

// Finish releases the call. Frames that still arrive for it are dropped for a
// while, so abandoning a call (a deadline, a cancelled caller) never costs the
// session; reusing its ID does.
func (c *EgressCall) Finish() {
	m := c.mux
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.finished {
		return
	}
	c.finished = true
	delete(m.calls, c.id)
	now := m.now()
	m.finished[c.id] = now
	m.finishedQ = append(m.finishedQ, c.id)
	for len(m.finishedQ) > 0 && (len(m.finishedQ) > egressTombstones || now.Sub(m.finished[m.finishedQ[0]]) > egressTombstoneTTL) {
		delete(m.finished, m.finishedQ[0])
		m.finishedQ = m.finishedQ[1:]
	}
}

// --- Payload codecs -------------------------------------------------------

// EgressAdmit is what the runner tells Portal about a request before Portal
// signs it: Portal must sign exactly these bytes' worth of request, and the
// runner refuses a command that differs.
type EgressAdmit struct {
	Method      string `json:"method"`
	URL         string `json:"url"`
	Accept      string `json:"accept"`
	ContentType string `json:"contentType"`
	BodyBytes   int    `json:"bodyBytes"`
	BodySHA256  string `json:"bodySha256"`
}

// EncodeEgressAdmit encodes the closed admit JSON.
func EncodeEgressAdmit(a EgressAdmit) ([]byte, error) {
	if !validEgressAdmit(a) {
		return nil, ErrEgressProtocol
	}
	return json.Marshal(a)
}

// DecodeEgressAdmit accepts only the exact canonical encoding of a valid admit.
func DecodeEgressAdmit(payload []byte) (EgressAdmit, error) {
	var a EgressAdmit
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if len(payload) == 0 || len(payload) > egressAdmitMaxBytes || d.Decode(&a) != nil || !validEgressAdmit(a) {
		return EgressAdmit{}, ErrEgressProtocol
	}
	if canonical, err := json.Marshal(a); err != nil || !bytes.Equal(canonical, payload) {
		return EgressAdmit{}, ErrEgressProtocol
	}
	return a, nil
}

func validEgressAdmit(a EgressAdmit) bool {
	digest, err := hex.DecodeString(a.BodySHA256)
	return appplatform.ValidHTTPRequestMethod(a.Method) && len(a.URL) > 0 && len(a.URL) <= appplatform.MaxHTTPTargetBytes &&
		appplatform.ValidHTTPPolicyHeaderValue(a.Accept) && appplatform.ValidHTTPPolicyHeaderValue(a.ContentType) &&
		a.BodyBytes >= 0 && a.BodyBytes <= appplatform.MaxHTTPRequestBytes &&
		err == nil && len(digest) == sha256.Size && hex.EncodeToString(digest) == a.BodySHA256
}

// EncodeEgressSigned encodes the command and proof frames: u32 length, the
// signed bytes, a 64-byte signature.
func EncodeEgressSigned(raw, signature []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, ErrEgressProtocol
	}
	out := make([]byte, 4, 4+len(raw)+64)
	binary.BigEndian.PutUint32(out, uint32(len(raw)))
	out = append(out, raw...)
	return append(out, signature...), nil
}

// DecodeEgressSigned is the inverse; max is the bound of raw (the command's or
// the check proof's).
func DecodeEgressSigned(payload []byte, max int) (raw, signature []byte, err error) {
	if len(payload) < 4+1+64 {
		return nil, nil, ErrEgressProtocol
	}
	n := binary.BigEndian.Uint32(payload)
	if n == 0 || int64(n) > int64(max) || int64(len(payload)) != 4+int64(n)+64 {
		return nil, nil, ErrEgressProtocol
	}
	return bytes.Clone(payload[4 : 4+n]), bytes.Clone(payload[4+n:]), nil
}

// DecodeEgressCommand reads a command frame payload.
func DecodeEgressCommand(payload []byte) (raw, signature []byte, err error) {
	return DecodeEgressSigned(payload, control.MaxCommandBytes)
}

// DecodeEgressProof reads a proof frame payload.
func DecodeEgressProof(payload []byte) (proof, signature []byte, err error) {
	return DecodeEgressSigned(payload, control.MaxNetworkHTTPCheckBytes)
}

// EncodeEgressChallenge encodes check number seq (1..EgressMaxChecks) with its nonce.
func EncodeEgressChallenge(seq byte, nonce [32]byte) []byte {
	return append([]byte{seq}, nonce[:]...)
}

// DecodeEgressChallenge is the inverse.
func DecodeEgressChallenge(payload []byte) (seq byte, nonce [32]byte, err error) {
	if len(payload) != 33 || payload[0] < 1 || payload[0] > EgressMaxChecks {
		return 0, nonce, ErrEgressProtocol
	}
	copy(nonce[:], payload[1:])
	return payload[0], nonce, nil
}

// EgressNoticeResult is the answer to a notice.
type EgressNoticeResult struct {
	EventID string `json:"eventId"`
	Created bool   `json:"created"`
}

// EncodeEgressNoticeResult encodes the closed result JSON.
func EncodeEgressNoticeResult(r EgressNoticeResult) ([]byte, error) {
	out, err := json.Marshal(r)
	if err != nil || r.EventID == "" || len(out) > egressResultMaxBytes {
		return nil, ErrEgressProtocol
	}
	return out, nil
}

// DecodeEgressNoticeResult accepts only the exact canonical encoding.
func DecodeEgressNoticeResult(payload []byte) (EgressNoticeResult, error) {
	var r EgressNoticeResult
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if len(payload) == 0 || len(payload) > egressResultMaxBytes || d.Decode(&r) != nil || r.EventID == "" {
		return EgressNoticeResult{}, ErrEgressProtocol
	}
	if canonical, err := json.Marshal(r); err != nil || !bytes.Equal(canonical, payload) {
		return EgressNoticeResult{}, ErrEgressProtocol
	}
	return r, nil
}

// DecodeEgressNotice reads a notice frame payload: the closed doc 152 input,
// exactly as the runner canonicalised it.
func DecodeEgressNotice(payload []byte) (broker.AppNotice, error) {
	n, err := broker.DecodeAppNotice(payload)
	if err != nil || !bytes.Equal(n.JSON(), payload) {
		return broker.AppNotice{}, ErrEgressProtocol
	}
	return n, nil
}

// EgressOutcome is how one call ended, for Portal's summary audit. RetryAfter
// is -1 when none was released.
type EgressOutcome struct {
	Class         byte
	Status        uint16
	ResponseBytes uint32
	RetryAfter    int32
}

// EncodeEgressOutcome encodes class u8, status u16, response bytes u32,
// retryAfter i32, big-endian.
func EncodeEgressOutcome(o EgressOutcome) ([]byte, error) {
	if o.Class < EgressOutcomeOK || o.Class > EgressOutcomeBusy || o.RetryAfter < -1 || o.RetryAfter > broker.MaxRetryAfterSeconds {
		return nil, ErrEgressProtocol
	}
	out := make([]byte, 11)
	out[0] = o.Class
	binary.BigEndian.PutUint16(out[1:], o.Status)
	binary.BigEndian.PutUint32(out[3:], o.ResponseBytes)
	binary.BigEndian.PutUint32(out[7:], uint32(o.RetryAfter))
	return out, nil
}

// DecodeEgressOutcome is the inverse.
func DecodeEgressOutcome(payload []byte) (EgressOutcome, error) {
	if len(payload) != 11 {
		return EgressOutcome{}, ErrEgressProtocol
	}
	o := EgressOutcome{Class: payload[0], Status: binary.BigEndian.Uint16(payload[1:]), ResponseBytes: binary.BigEndian.Uint32(payload[3:]), RetryAfter: int32(binary.BigEndian.Uint32(payload[7:]))}
	if _, err := EncodeEgressOutcome(o); err != nil {
		return EgressOutcome{}, err
	}
	return o, nil
}

// DecodeEgressRefusal reads a refusal code.
func DecodeEgressRefusal(payload []byte) (byte, error) {
	if len(payload) != 1 {
		return 0, ErrEgressProtocol
	}
	switch payload[0] {
	case EgressRefusalInvalid, EgressRefusalDenied, EgressRefusalUnavailable, EgressRefusalBusy, EgressRefusalConflict, EgressRefusalCapacity:
		return payload[0], nil
	}
	return 0, ErrEgressProtocol
}

// --- Open and acknowledgement --------------------------------------------

// EgressOpen is a parsed open frame.
type EgressOpen struct {
	Nonce      [32]byte
	Raw        []byte
	Signature  []byte
	Descriptor []byte
	// FrameSHA256 is the digest of the exact bytes received (magic included); the
	// acknowledgement binds it.
	FrameSHA256 [32]byte
}

// EncodeEgressOpen builds `PAEGRS01 | nonce[32] | u32 len | raw | sig[64] |
// u32 len | descriptor`. A fresh random nonce makes every open frame, and so its
// acknowledgement, unique.
func EncodeEgressOpen(raw, signature, descriptor []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return nil, ErrEgressProtocol
	}
	frame := make([]byte, 44, 44+len(raw)+64+4+len(descriptor))
	copy(frame, ServiceEgressOpenMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(frame[40:], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(descriptor)))
	frame = append(frame, size[:]...)
	return append(frame, descriptor...), nil
}

// ReadEgressOpen parses an open frame. Both lengths are checked before their
// allocation. When magicConsumed is true the first eight bytes were already
// read (and matched) by the dispatcher.
func ReadEgressOpen(r io.Reader, magicConsumed bool) (EgressOpen, error) {
	var open EgressOpen
	header := make([]byte, 44)
	copy(header, ServiceEgressOpenMagic)
	start := 0
	if magicConsumed {
		start = 8
	}
	if _, err := io.ReadFull(r, header[start:]); err != nil {
		return open, err
	}
	if string(header[:8]) != ServiceEgressOpenMagic {
		return open, ErrEgressProtocol
	}
	n := binary.BigEndian.Uint32(header[40:])
	if n == 0 || n > control.MaxCommandBytes {
		return open, ErrEgressProtocol
	}
	body := make([]byte, int(n)+64+4)
	if _, err := io.ReadFull(r, body[:int(n)+64]); err != nil {
		return open, err
	}
	if _, err := io.ReadFull(r, body[int(n)+64:]); err != nil {
		return open, err
	}
	count := binary.BigEndian.Uint32(body[int(n)+64:])
	if count == 0 || count > appplatform.MaxDescriptorBytes {
		return open, ErrEgressProtocol
	}
	descriptor := make([]byte, int(count))
	if _, err := io.ReadFull(r, descriptor); err != nil {
		return open, err
	}
	digest := sha256.New()
	digest.Write(header)
	digest.Write(body)
	digest.Write(descriptor)
	copy(open.FrameSHA256[:], digest.Sum(nil))
	copy(open.Nonce[:], header[8:40])
	open.Raw, open.Signature, open.Descriptor = body[:n:n], body[n:int(n)+64], descriptor
	return open, nil
}

// Acknowledgement statuses.
const (
	EgressAckOpen    byte = 1
	EgressAckRefused byte = 2
)

// EncodeEgressAck builds `PAEGRA01 | sha256(open frame) | status`.
func EncodeEgressAck(openSHA256 [32]byte, status byte) []byte {
	out := append([]byte(ServiceEgressAckMagic), openSHA256[:]...)
	return append(out, status)
}

// ParseEgressAck checks an acknowledgement against the digest of the open frame
// the caller actually sent, so an acknowledgement of another frame, or a replay
// of an older one, is refused. It returns the status.
func ParseEgressAck(ack []byte, openSHA256 [32]byte) (byte, error) {
	if len(ack) != 41 || string(ack[:8]) != ServiceEgressAckMagic || !bytes.Equal(ack[8:40], openSHA256[:]) || (ack[40] != EgressAckOpen && ack[40] != EgressAckRefused) {
		return 0, ErrEgressProtocol
	}
	return ack[40], nil
}
