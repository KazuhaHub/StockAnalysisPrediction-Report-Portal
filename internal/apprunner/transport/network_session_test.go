package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type networkEnrollmentFixture struct{ a control.Authority }

func (e networkEnrollmentFixture) ControllerDeployment(context.Context) (journal.Deployment, error) {
	return journal.Deployment{ControllerEnrollment: journal.ControllerEnrollment{DeploymentID: e.a.DeploymentID, ControllerEpoch: e.a.ControllerEpoch, ControlRevision: e.a.ControlRevision, PublicKey: e.a.PublicKey, Fingerprint: e.a.Fingerprint}}, nil
}

type networkExecutionFixture struct {
	current broker.CurrentNetworkAuthority
	effects atomic.Int32
}

func (e *networkExecutionFixture) Do(ctx context.Context, raw, sig, body []byte, a control.Authority) (broker.HTTPResponse, error) {
	v, err := control.VerifyNetworkHTTPRequest(raw, sig, a, time.Now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(v, body) != nil {
		return broker.HTTPResponse{}, broker.ErrDenied
	}
	for i := 0; i < 4; i++ {
		if _, err := e.current(ctx, v.Command()); err != nil {
			if i == 3 {
				return broker.HTTPResponse{}, broker.ErrUncertain
			}
			return broker.HTTPResponse{}, broker.ErrDenied
		}
		if i == 2 {
			e.effects.Add(1)
		}
	}
	return broker.HTTPResponse{Status: 201, ContentType: "application/json", Body: []byte(`{"ok":true}`)}, nil
}
func networkSessionFixture(t *testing.T) ([]byte, []byte, []byte, control.Authority, ed25519.PrivateKey) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(public)
	a := control.Authority{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(hash[:])}
	policy, err := appplatform.CanonicalHTTPPolicy(appplatform.HTTPPolicy{Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, RequestBytes: 64, ResponseBytes: 64, Deadline: time.Second, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := appplatform.ParseHTTPPolicy(policy)
	body := []byte(`{}`)
	digest := sha256.Sum256(body)
	now := time.Now()
	c := control.NetworkHTTPRequestCommand{Format: control.NetworkHTTPRequestFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: "40000000-0000-4000-8000-000000000001", ReviewPlanID: "50000000-0000-4000-8000-000000000001", Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: parsed.SHA256, Method: "POST", URL: "https://api.example.com/v1", BodyBytes: "2", BodySHA256: hex.EncodeToString(digest[:]), IssuedMS: strconv.FormatInt(now.UnixMilli(), 10), ExpiresMS: strconv.FormatInt(now.Add(30*time.Second).UnixMilli(), 10)}
	raw, sig, err := control.SignNetworkHTTPRequest(c, key, a, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, body, a, key
}

func TestNetworkSessionInteractiveProofsAndBoundResponse(t *testing.T) {
	raw, sig, body, a, key := networkSessionFixture(t)
	provider, err := newNetworkProvider(networkEnrollmentFixture{a}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	execution := &networkExecutionFixture{current: provider.current}
	provider.pool = execution
	client, server := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); provider.serve(ctx, server, false) }()
	defer client.Close()
	var approvals atomic.Int32
	got, err := exchangeNetworkSession(ctx, client, raw, sig, body, a, func(ctx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
		approvals.Add(1)
		return control.SignNetworkHTTPCheck(c, nonce, key, a, time.Now().UnixMilli())
	})
	if err != nil || got.Status != 201 || string(got.Body) != `{"ok":true}` || approvals.Load() != 4 || execution.effects.Load() != 1 {
		t.Fatalf("%+v %v checks=%d effects=%d", got, err, approvals.Load(), execution.effects.Load())
	}
	<-done
	if len(provider.sessions) != 0 {
		t.Fatal("session leaked")
	}
}

func TestNetworkSessionRevocationBeforeDispatchAndAfterIO(t *testing.T) {
	for _, at := range []int32{1, 3, 4} {
		t.Run(strconv.FormatInt(int64(at), 10), func(t *testing.T) {
			raw, sig, body, a, key := networkSessionFixture(t)
			provider, _ := newNetworkProvider(networkEnrollmentFixture{a}, 2, 2)
			execution := &networkExecutionFixture{current: provider.current}
			provider.pool = execution
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); defer server.Close(); provider.serve(ctx, server, false) }()
			var approvals atomic.Int32
			got, err := exchangeNetworkSession(ctx, client, raw, sig, body, a, func(ctx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
				if approvals.Add(1) == at {
					return nil, nil, errors.New("private SQL denial")
				}
				return control.SignNetworkHTTPCheck(c, nonce, key, a, time.Now().UnixMilli())
			})
			client.Close()
			<-done
			if !errors.Is(err, broker.ErrUncertain) || got.Status != 0 || len(got.Body) != 0 || len(provider.sessions) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
			want := int32(0)
			if at == 4 {
				want = 1
			}
			if execution.effects.Load() != want {
				t.Fatal("wrong dispatch count", execution.effects.Load())
			}
		})
	}
}

func networkTestFrame(raw, sig, body []byte) []byte {
	frame := make([]byte, 44)
	copy(frame, networkRequestMagic)
	frame[8] = 1
	binary.BigEndian.PutUint32(frame[40:], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, sig...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(body)))
	frame = append(frame, size[:]...)
	return append(frame, body...)
}

func TestNetworkSessionMalformedRequestsNeverExecute(t *testing.T) {
	for _, mode := range []string{"magic", "oversized", "signature", "body", "body_size", "truncated", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			raw, sig, body, a, _ := networkSessionFixture(t)
			provider, _ := newNetworkProvider(networkEnrollmentFixture{a}, 2, 2)
			execution := &networkExecutionFixture{current: provider.current}
			provider.pool = execution
			frame := networkTestFrame(raw, sig, body)
			switch mode {
			case "magic":
				frame[0] ^= 1
			case "oversized":
				binary.BigEndian.PutUint32(frame[40:], 65537)
			case "signature":
				frame[44+len(raw)] ^= 1
			case "body":
				frame[len(frame)-1] ^= 1
			case "body_size":
				binary.BigEndian.PutUint32(frame[44+len(raw)+64:], 65537)
			case "truncated":
				frame = frame[:len(frame)-1]
			case "trailing":
				frame = append(frame, []byte("invalid proof bytes")...)
			}
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); defer server.Close(); provider.serve(ctx, server, false) }()
			wrote := make(chan struct{})
			go func() { defer close(wrote); io.Copy(client, bytes.NewReader(frame)) }()
			if mode == "truncated" {
				client.Close()
			} else {
				var result [64]byte
				client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
				client.Read(result[:])
				client.Close()
			}
			<-done
			<-wrote
			if execution.effects.Load() != 0 || len(provider.sessions) != 0 {
				t.Fatal("malformed request executed or leaked")
			}
		})
	}
}

func TestNetworkSessionRefusesForgedOrUnboundedReplies(t *testing.T) {
	for _, mode := range []string{"missing_check", "repeat_nonce", "request_hash", "unknown_code", "oversized_body", "redirect", "trailing", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			raw, sig, body, a, key := networkSessionFixture(t)
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				frame := make([]byte, len(networkTestFrame(raw, sig, body)))
				if _, err := io.ReadFull(server, frame); err != nil {
					return
				}
				digest := sha256.Sum256(frame)
				if mode != "missing_check" {
					for i := byte(1); i <= 4; i++ {
						nonce := make([]byte, 32)
						nonce[0] = i
						if mode == "repeat_nonce" {
							nonce[0] = 1
						}
						message := append([]byte(networkCheckMagic), i)
						message = append(message, nonce...)
						if _, err := io.Copy(server, bytes.NewReader(message)); err != nil {
							return
						}
						var header [12]byte
						if _, err := io.ReadFull(server, header[:]); err != nil {
							return
						}
						length := binary.BigEndian.Uint32(header[8:])
						if length > 2048 {
							return
						}
						if _, err := io.CopyN(io.Discard, server, int64(length)+64); err != nil {
							return
						}
					}
				}
				data := []byte(`{"ok":true}`)
				reply := make([]byte, 49)
				copy(reply, networkReplyMagic)
				copy(reply[8:40], digest[:])
				reply[40] = 1
				binary.BigEndian.PutUint16(reply[41:43], 201)
				binary.BigEndian.PutUint32(reply[45:49], uint32(len(data)))
				switch mode {
				case "request_hash":
					reply[8] ^= 1
				case "unknown_code":
					reply[40] = 5
				case "oversized_body":
					binary.BigEndian.PutUint32(reply[45:49], 65)
				case "redirect":
					binary.BigEndian.PutUint16(reply[41:43], 302)
				case "truncated":
					data = data[:len(data)-1]
				}
				reply = append(reply, data...)
				if mode == "trailing" {
					reply = append(reply, 1)
				}
				io.Copy(server, bytes.NewReader(reply))
			}()
			got, err := exchangeNetworkSession(ctx, client, raw, sig, body, a, func(ctx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
				return control.SignNetworkHTTPCheck(c, nonce, key, a, time.Now().UnixMilli())
			})
			client.Close()
			<-done
			if !errors.Is(err, broker.ErrUncertain) || got.Status != 0 || len(got.Body) != 0 {
				t.Fatalf("unsafe reply %+v %v", got, err)
			}
		})
	}
}

func TestNetworkObservationCannotRoundTripThroughJSON(t *testing.T) {
	original := NetworkObservation{response: broker.HTTPResponse{Status: 200, Body: []byte("private")}, authority: control.Authority{PublicKey: []byte("key")}, requestSHA: "bound"}
	if _, err := json.Marshal(original); err == nil {
		t.Fatal("observation serialized as authority")
	}
	target := original
	if json.Unmarshal([]byte(`{}`), &target) == nil || target.requestSHA != "" || target.response.Status != 0 {
		t.Fatal("observation reconstructed")
	}
	response := original.Response()
	response.Body[0] ^= 1
	authority := original.Controller()
	authority.PublicKey[0] ^= 1
	if string(original.Response().Body) != "private" || string(original.Controller().PublicKey) != "key" {
		t.Fatal("aliased observation")
	}
}
