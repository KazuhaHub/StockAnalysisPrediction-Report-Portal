package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const networkStreamRequestMagic = "PRSS0001"
const networkStreamCheckMagic = "PRSC0001"
const networkStreamStartMagic = "PRST0001"
const networkStreamChunkMagic = "PRSK0001"
const networkStreamEndMagic = "PRSE0001"
const networkStreamSessionTimeout = 35 * time.Second

type networkStreamExecution interface {
	Do(
		context.Context,
		[]byte,
		[]byte,
		[]byte,
		control.Authority,
		func(context.Context) error,
		broker.HTTPStreamCallbacks,
	) (broker.HTTPStreamResult, error)
}

func exchangeNetworkStreamSession(
	parent context.Context,
	conn net.Conn,
	raw, signature, body []byte,
	a control.Authority,
	approve NetworkApproval,
	callbacks broker.HTTPStreamCallbacks,
) (broker.HTTPStreamResult, error) {
	var zero broker.HTTPStreamResult
	if parent == nil || conn == nil || approve == nil || callbacks.Start == nil || parent.Err() != nil {
		return zero, broker.ErrDenied
	}
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(body) > appplatform.MaxHTTPRequestBytes || len(a.PublicKey) != 32 {
		return zero, broker.ErrDenied
	}
	raw, signature, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(body)
	a.PublicKey = bytes.Clone(a.PublicKey)
	defer clear(body)
	verified, err := control.VerifyNetworkHTTPStreamRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPStreamBody(verified, body) != nil {
		return zero, broker.ErrDenied
	}
	command := verified.Command()
	defer clear(command.CredentialValue)
	policy, err := appplatform.ParseHTTPStreamPolicy(command.PolicyRaw)
	if err != nil {
		return zero, broker.ErrDenied
	}
	ctx, cancel := context.WithTimeout(parent, networkStreamSessionTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	frame := make([]byte, 44)
	copy(frame, networkStreamRequestMagic)
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
	checks := 0
	seen := make(map[[32]byte]bool)
	started := false
	var status int
	var contentType string
	var total int64
	frames := 0
	digest := sha256.New()
	for {
		var magic [8]byte
		if _, err := io.ReadFull(conn, magic[:]); err != nil {
			return zero, broker.ErrUncertain
		}
		switch string(magic[:]) {
		case networkStreamCheckMagic:
			var challenge [33]byte
			if _, err := io.ReadFull(conn, challenge[:]); err != nil ||
				checks >= appplatform.MaxHTTPStreamChecks ||
				int(challenge[0]) != checks+1 {
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
			proof, proofSignature, approveErr := approve(checkCtx, verified.Command(), hex.EncodeToString(challenge[1:]))
			canceled := checkCtx.Err() != nil
			checkCancel()
			if approveErr != nil ||
				canceled ||
				control.VerifyNetworkHTTPStreamCheck(verified.Command(), hex.EncodeToString(challenge[1:]), proof, proofSignature, a, time.Now().UnixMilli()) != nil {
				return zero, broker.ErrUncertain
			}
			reply := make([]byte, 12)
			copy(reply, networkProofMagic)
			binary.BigEndian.PutUint32(reply[8:], uint32(len(proof)))
			reply = append(reply, proof...)
			reply = append(reply, proofSignature...)
			if _, err := io.Copy(conn, bytes.NewReader(reply)); err != nil {
				return zero, broker.ErrUncertain
			}
		case networkStreamStartMagic:
			if started {
				return zero, broker.ErrUncertain
			}
			var header [4]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				return zero, broker.ErrUncertain
			}
			remoteStatus := int(binary.BigEndian.Uint16(header[:2]))
			kindSize := int(binary.BigEndian.Uint16(header[2:]))
			if remoteStatus < 200 || remoteStatus >= 300 || kindSize < 1 || kindSize > 256 {
				return zero, broker.ErrUncertain
			}
			kind := make([]byte, kindSize)
			if _, err := io.ReadFull(conn, kind); err != nil {
				return zero, broker.ErrUncertain
			}
			if _, ok := appplatform.ValidateHTTPStreamContentType(policy.Policy, string(kind)); !ok {
				return zero, broker.ErrUncertain
			}
			if callbacks.Start(remoteStatus, string(kind)) != nil {
				return zero, broker.ErrUncertain
			}
			started, status, contentType = true, remoteStatus, string(kind)
		case networkStreamChunkMagic:
			if !started || frames >= appplatform.MaxHTTPStreamChecks {
				return zero, broker.ErrUncertain
			}
			var chunkHeader [5]byte
			if _, err := io.ReadFull(conn, chunkHeader[:]); err != nil || int(chunkHeader[0]) != frames+1 {
				return zero, broker.ErrUncertain
			}
			n := int64(binary.BigEndian.Uint32(chunkHeader[1:]))
			if n < 1 || n > appplatform.MaxHTTPStreamFrameBytes || total+n > policy.Policy.StreamBytes {
				return zero, broker.ErrUncertain
			}
			chunk := make([]byte, int(n))
			if _, err := io.ReadFull(conn, chunk); err != nil {
				return zero, broker.ErrUncertain
			}
			if callbacks.Chunk != nil && callbacks.Chunk(bytes.Clone(chunk)) != nil {
				clear(chunk)
				return zero, broker.ErrUncertain
			}
			_, _ = digest.Write(chunk)
			clear(chunk)
			total += n
			frames++
		case networkStreamEndMagic:
			var end [77]byte
			if _, err := io.ReadFull(conn, end[:]); err != nil || !bytes.Equal(end[:32], frameSHA[:]) {
				return zero, broker.ErrUncertain
			}
			code := end[32]
			remoteBytes := int64(binary.BigEndian.Uint64(end[33:41]))
			remoteFrames := int(binary.BigEndian.Uint32(end[41:45]))
			remoteDigest := end[45:77]
			if code == 1 {
				if !started || checks < 3 || remoteBytes != total || remoteFrames != frames || !bytes.Equal(remoteDigest, digest.Sum(nil)) || ctx.Err() != nil {
					return zero, broker.ErrUncertain
				}
				if _, err := control.VerifyNetworkHTTPStreamRequest(raw, signature, a, time.Now().UnixMilli()); err != nil {
					return zero, broker.ErrUncertain
				}
				if err := networkStreamEOF(conn); err != nil {
					return zero, broker.ErrUncertain
				}
				return broker.HTTPStreamResult{Status: status, ContentType: contentType, Bytes: total, Frames: frames}, nil
			}
			if remoteBytes != 0 || remoteFrames != 0 || !bytes.Equal(remoteDigest, make([]byte, 32)) {
				return zero, broker.ErrUncertain
			}
			if err := networkStreamEOF(conn); err != nil {
				return zero, broker.ErrUncertain
			}
			if started {
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
		default:
			return zero, broker.ErrUncertain
		}
	}
}

// serveStream runs only after the Linux endpoint verifies the exact peer.
func (p *networkProvider) serveStream(parent context.Context, conn net.Conn, magicConsumed bool) {
	ctx, cancel := context.WithTimeout(parent, networkStreamSessionTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	header := make([]byte, 44)
	if magicConsumed {
		copy(header, networkStreamRequestMagic)
		if _, err := io.ReadFull(conn, header[8:]); err != nil {
			return
		}
	} else if _, err := io.ReadFull(conn, header); err != nil || string(header[:8]) != networkStreamRequestMagic {
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
	verified, err := control.VerifyNetworkHTTPStreamRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return
	}
	command := verified.Command()
	defer clear(command.CredentialValue)
	policy, err := appplatform.ParseHTTPStreamPolicy(command.PolicyRaw)
	if err != nil {
		return
	}
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return
	}
	bodySize := binary.BigEndian.Uint32(size[:])
	if bodySize > appplatform.MaxHTTPRequestBytes {
		return
	}
	body := make([]byte, int(bodySize))
	defer clear(body)
	if _, err := io.ReadFull(conn, body); err != nil || control.ValidateNetworkHTTPStreamBody(verified, body) != nil {
		return
	}
	hasher := sha256.New()
	_, _ = hasher.Write(header)
	_, _ = hasher.Write(raw)
	_, _ = hasher.Write(signature)
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write(body)
	frameSHA := hasher.Sum(nil)
	requestSHA := sha256.Sum256(raw)
	session := &networkControlSession{
		conn:       conn,
		ctx:        ctx,
		requestSHA: hex.EncodeToString(requestSHA[:]),
		checkMagic: networkStreamCheckMagic,
		maxChecks:  appplatform.MaxHTTPStreamChecks,
	}
	p.mu.Lock()
	if len(p.streamSessions) >= 8 || p.streamSessions[command.RequestID] != nil || p.sessions[command.RequestID] != nil {
		p.mu.Unlock()
		return
	}
	p.streamSessions[command.RequestID] = session
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.streamSessions[command.RequestID] == session {
			delete(p.streamSessions, command.RequestID)
		}
		p.mu.Unlock()
	}()
	started := false
	var total int64
	frames := 0
	streamDigest := sha256.New()
	result, runErr := p.stream.Do(ctx, raw, signature, body, a, nil, broker.HTTPStreamCallbacks{
		Start: func(status int, contentType string) error {
			if started || status < 200 || status >= 300 || len(contentType) > 256 {
				return broker.ErrUncertain
			}
			if _, ok := appplatform.ValidateHTTPStreamContentType(policy.Policy, contentType); !ok {
				return broker.ErrUncertain
			}
			frame := make([]byte, 12)
			copy(frame, networkStreamStartMagic)
			binary.BigEndian.PutUint16(frame[8:10], uint16(status))
			binary.BigEndian.PutUint16(frame[10:12], uint16(len(contentType)))
			frame = append(frame, contentType...)
			if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
				return broker.ErrUncertain
			}
			started = true
			return nil
		},
		Chunk: func(data []byte) error {
			if !started || len(data) < 1 || len(data) > appplatform.MaxHTTPStreamFrameBytes || total+int64(len(data)) > policy.Policy.StreamBytes || frames >= appplatform.MaxHTTPStreamChecks {
				return broker.ErrUncertain
			}
			frame := make([]byte, 13)
			copy(frame, networkStreamChunkMagic)
			frame[8] = byte(frames + 1)
			binary.BigEndian.PutUint32(frame[9:13], uint32(len(data)))
			frame = append(frame, data...)
			if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
				return broker.ErrUncertain
			}
			_, _ = streamDigest.Write(data)
			total += int64(len(data))
			frames++
			return nil
		},
	})
	end := make([]byte, 85)
	copy(end, networkStreamEndMagic)
	copy(end[8:40], frameSHA)
	code := byte(1)
	if runErr != nil {
		result = broker.HTTPStreamResult{}
		switch {
		case started:
			code = 4
		case errors.Is(runErr, broker.ErrDenied):
			code = 2
		case errors.Is(runErr, broker.ErrUnavailable):
			code = 3
		default:
			code = 4
		}
	}
	if code == 1 {
		if !started || result.Bytes != total || result.Frames != frames {
			code = 4
		} else {
			binary.BigEndian.PutUint64(end[41:49], uint64(total))
			binary.BigEndian.PutUint32(end[49:53], uint32(frames))
			copy(end[53:85], streamDigest.Sum(nil))
		}
	}
	end[40] = code
	_, _ = io.Copy(conn, bytes.NewReader(end))
}
