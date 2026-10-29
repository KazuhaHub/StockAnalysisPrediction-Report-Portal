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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const networkRequestMagic = "PRNQ0001"
const networkCheckMagic = "PRNC0001"
const networkProofMagic = "PRNP0001"
const networkReplyMagic = "PRNR0001"
const networkSessionTimeout = 20 * time.Second

type NetworkApproval func(context.Context, control.NetworkHTTPRequestCommand, string) ([]byte, []byte, error)
type networkEnrollmentStore interface {
	ControllerDeployment(context.Context) (journal.Deployment, error)
}
type networkExecutor interface {
	Do(context.Context, []byte, []byte, []byte, control.Authority) (broker.HTTPResponse, error)
}
type networkProvider struct {
	store    networkEnrollmentStore
	pool     networkExecutor
	current  broker.CurrentNetworkAuthority
	mu       sync.Mutex
	sessions map[string]*networkControlSession
}
type networkControlSession struct {
	conn       net.Conn
	ctx        context.Context
	requestSHA string
	sequence   byte
	mu         sync.Mutex
}

func newNetworkProvider(store networkEnrollmentStore, active, entries int) (*networkProvider, error) {
	if store == nil {
		return nil, broker.ErrDenied
	}
	p := &networkProvider{store: store, sessions: make(map[string]*networkControlSession)}
	current, err := broker.NewConfirmedNetworkAuthority(p.enrollment, p.confirm)
	if err != nil {
		return nil, err
	}
	p.current = current
	pool, err := broker.NewNetworkPool(active, entries, current)
	if err != nil {
		return nil, err
	}
	p.pool = pool
	return p, nil
}
func (p *networkProvider) enrollment(ctx context.Context) (control.Authority, error) {
	d, err := p.store.ControllerDeployment(ctx)
	if err != nil {
		return control.Authority{}, broker.ErrDenied
	}
	return control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: bytes.Clone(d.PublicKey), Fingerprint: d.Fingerprint}, nil
}
func (p *networkProvider) confirm(ctx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, broker.ErrDenied
	}
	digest := sha256.Sum256(raw)
	p.mu.Lock()
	session := p.sessions[c.RequestID]
	p.mu.Unlock()
	if session == nil || session.requestSHA != hex.EncodeToString(digest[:]) {
		return nil, nil, broker.ErrDenied
	}
	challenge, err := hex.DecodeString(nonce)
	if err != nil || len(challenge) != 32 {
		return nil, nil, broker.ErrDenied
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.sequence >= 4 || ctx.Err() != nil || session.ctx.Err() != nil {
		return nil, nil, broker.ErrDenied
	}
	session.sequence++
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, nil, broker.ErrDenied
	}
	if session.conn.SetDeadline(deadline) != nil {
		return nil, nil, broker.ErrDenied
	}
	stop := context.AfterFunc(ctx, func() { session.conn.Close() })
	defer stop()
	defer func() {
		if end, ok := session.ctx.Deadline(); ok {
			session.conn.SetDeadline(end)
		}
	}()
	message := append([]byte(networkCheckMagic), session.sequence)
	message = append(message, challenge...)
	if _, err := io.Copy(session.conn, bytes.NewReader(message)); err != nil {
		return nil, nil, broker.ErrDenied
	}
	var header [12]byte
	if _, err := io.ReadFull(session.conn, header[:]); err != nil || string(header[:8]) != networkProofMagic {
		return nil, nil, broker.ErrDenied
	}
	length := binary.BigEndian.Uint32(header[8:])
	if length == 0 || length > control.MaxNetworkHTTPCheckBytes {
		return nil, nil, broker.ErrDenied
	}
	proof := make([]byte, int(length))
	signature := make([]byte, 64)
	if _, err := io.ReadFull(session.conn, proof); err != nil {
		return nil, nil, broker.ErrDenied
	}
	if _, err := io.ReadFull(session.conn, signature); err != nil {
		return nil, nil, broker.ErrDenied
	}
	return proof, signature, nil
}

// serve is entered only after the Linux endpoint verifies the kernel peer. The
// common stream codec is separately tested over in-memory duplex connections.
func (p *networkProvider) serve(parent context.Context, conn net.Conn, magicConsumed bool) {
	ctx, cancel := context.WithTimeout(parent, networkSessionTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	header := make([]byte, 44)
	if magicConsumed {
		copy(header, networkRequestMagic)
		if _, err := io.ReadFull(conn, header[8:]); err != nil {
			return
		}
	} else if _, err := io.ReadFull(conn, header); err != nil || string(header[:8]) != networkRequestMagic {
		return
	}
	length := binary.BigEndian.Uint32(header[40:])
	if length == 0 || length > control.MaxCommandBytes {
		return
	}
	raw := make([]byte, int(length))
	signature := make([]byte, 64)
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
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return
	}
	policy, err := appplatform.ParseHTTPPolicy(verified.Command().PolicyRaw)
	if err != nil {
		return
	}
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return
	}
	bodySize := binary.BigEndian.Uint32(size[:])
	if bodySize > 65536 {
		return
	}
	body := make([]byte, int(bodySize))
	defer clear(body)
	if _, err := io.ReadFull(conn, body); err != nil || control.ValidateNetworkHTTPBody(verified, body) != nil {
		return
	}
	hasher := sha256.New()
	hasher.Write(header)
	hasher.Write(raw)
	hasher.Write(signature)
	hasher.Write(size[:])
	hasher.Write(body)
	frameSHA := hasher.Sum(nil)
	requestSHA := sha256.Sum256(raw)
	c := verified.Command()
	session := &networkControlSession{conn: conn, ctx: ctx, requestSHA: hex.EncodeToString(requestSHA[:])}
	p.mu.Lock()
	if len(p.sessions) >= 8 || p.sessions[c.RequestID] != nil {
		p.mu.Unlock()
		return
	}
	p.sessions[c.RequestID] = session
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.sessions[c.RequestID] == session {
			delete(p.sessions, c.RequestID)
		}
		p.mu.Unlock()
	}()
	response, err := p.pool.Do(ctx, raw, signature, body, a)
	if ctx.Err() != nil {
		return
	}
	code := byte(1)
	if err != nil {
		response = broker.HTTPResponse{}
		switch {
		case errors.Is(err, broker.ErrDenied):
			code = 2
		case errors.Is(err, broker.ErrUnavailable):
			code = 3
		default:
			code = 4
		}
	}
	if code == 1 && (session.sequence != 4 || response.Status < 200 || response.Status >= 300 && response.Status < 400 || response.Status > 599 || !appplatform.ValidHTTPPolicyHeaderValue(response.ContentType) || int64(len(response.Body)) > policy.Policy.ResponseBytes) {
		return
	}
	result := make([]byte, 49)
	copy(result, networkReplyMagic)
	copy(result[8:40], frameSHA)
	result[40] = code
	binary.BigEndian.PutUint16(result[41:43], uint16(response.Status))
	binary.BigEndian.PutUint16(result[43:45], uint16(len(response.ContentType)))
	binary.BigEndian.PutUint32(result[45:49], uint32(len(response.Body)))
	result = append(result, response.ContentType...)
	result = append(result, response.Body...)
	defer clear(result)
	io.Copy(conn, bytes.NewReader(result))
}

// exchangeNetworkSession assumes its caller has already authenticated the exact
// OS endpoint. Any lost/malformed reply after dispatch remains uncertain.
func exchangeNetworkSession(parent context.Context, conn net.Conn, raw, signature, body []byte, a control.Authority, approve NetworkApproval) (broker.HTTPResponse, error) {
	var zero broker.HTTPResponse
	if parent == nil || approve == nil || parent.Err() != nil {
		return zero, broker.ErrDenied
	}
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(body) > 65536 || len(a.PublicKey) != 32 {
		return zero, broker.ErrDenied
	}
	raw, signature, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(body)
	a.PublicKey = bytes.Clone(a.PublicKey)
	defer clear(body)
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(verified, body) != nil {
		return zero, broker.ErrDenied
	}
	policy, err := appplatform.ParseHTTPPolicy(verified.Command().PolicyRaw)
	if err != nil {
		return zero, broker.ErrDenied
	}
	ctx, cancel := context.WithTimeout(parent, networkSessionTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	frame := make([]byte, 44)
	copy(frame, networkRequestMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, broker.ErrDenied
	}
	binary.BigEndian.PutUint32(frame[40:], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(body)))
	frame = append(frame, size[:]...)
	frame = append(frame, body...)
	defer clear(frame)
	frameSHA := sha256.Sum256(frame)
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return zero, broker.ErrUncertain
	}
	checks := byte(0)
	seen := make(map[[32]byte]bool)
	for {
		var magic [8]byte
		if _, err := io.ReadFull(conn, magic[:]); err != nil {
			return zero, broker.ErrUncertain
		}
		switch string(magic[:]) {
		case networkCheckMagic:
			var challenge [33]byte
			if _, err := io.ReadFull(conn, challenge[:]); err != nil || checks >= 4 || challenge[0] != checks+1 {
				return zero, broker.ErrUncertain
			}
			checks++
			var nonce [32]byte
			copy(nonce[:], challenge[1:])
			if seen[nonce] {
				return zero, broker.ErrUncertain
			}
			seen[nonce] = true
			checkCtx, checkCancel := context.WithTimeout(ctx, time.Second)
			command := verified.Command()
			proof, sig, err := approve(checkCtx, command, hex.EncodeToString(challenge[1:]))
			canceled := checkCtx.Err() != nil
			checkCancel()
			if err != nil || canceled || control.VerifyNetworkHTTPCheck(verified.Command(), hex.EncodeToString(challenge[1:]), proof, sig, a, time.Now().UnixMilli()) != nil {
				return zero, broker.ErrUncertain
			}
			reply := make([]byte, 12)
			copy(reply, networkProofMagic)
			binary.BigEndian.PutUint32(reply[8:], uint32(len(proof)))
			reply = append(reply, proof...)
			reply = append(reply, sig...)
			if _, err := io.Copy(conn, bytes.NewReader(reply)); err != nil {
				return zero, broker.ErrUncertain
			}
		case networkReplyMagic:
			var header [41]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil || !bytes.Equal(header[:32], frameSHA[:]) {
				return zero, broker.ErrUncertain
			}
			code := header[32]
			status := binary.BigEndian.Uint16(header[33:35])
			kindSize := binary.BigEndian.Uint16(header[35:37])
			bodySize := binary.BigEndian.Uint32(header[37:41])
			if code < 1 || code > 4 || kindSize > 256 || int64(bodySize) > policy.Policy.ResponseBytes {
				return zero, broker.ErrUncertain
			}
			if code != 1 {
				if status != 0 || kindSize != 0 || bodySize != 0 {
					return zero, broker.ErrUncertain
				}
				if err := networkStreamEOF(conn); err != nil {
					return zero, broker.ErrUncertain
				}
				switch code {
				case 2:
					return zero, broker.ErrDenied
				case 3:
					return zero, broker.ErrUnavailable
				default:
					return zero, broker.ErrUncertain
				}
			}
			if checks != 4 || status < 200 || status >= 300 && status < 400 || status > 599 {
				return zero, broker.ErrUncertain
			}
			content := make([]byte, int(kindSize)+int(bodySize))
			defer clear(content)
			if _, err := io.ReadFull(conn, content); err != nil {
				return zero, broker.ErrUncertain
			}
			kind := string(content[:kindSize])
			if !appplatform.ValidHTTPPolicyHeaderValue(kind) || networkStreamEOF(conn) != nil || ctx.Err() != nil {
				return zero, broker.ErrUncertain
			}
			if _, err := control.VerifyNetworkHTTPRequest(raw, signature, a, time.Now().UnixMilli()); err != nil {
				return zero, broker.ErrUncertain
			}
			return broker.HTTPResponse{Status: int(status), ContentType: kind, Body: bytes.Clone(content[kindSize:])}, nil
		default:
			return zero, broker.ErrUncertain
		}
	}
}
func networkStreamEOF(r io.Reader) error {
	var extra [1]byte
	n, err := r.Read(extra[:])
	if n != 0 || err != io.EOF {
		return broker.ErrUncertain
	}
	return nil
}
