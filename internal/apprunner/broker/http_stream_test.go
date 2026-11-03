package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func streamPolicyFixture() HTTPStreamPolicy {
	return HTTPStreamPolicy{
		Origin:       "https://stream.example.com",
		Paths:        []string{"/v1/stream"},
		Methods:      []string{"GET", "POST"},
		RequestBytes: 1024,
		StreamBytes:  256 << 10,
		Duration:     3 * time.Second,
		IdleTimeout:  750 * time.Millisecond,
		Concurrency:  1,
		MediaTypes:   []string{"application/x-ndjson", "text/event-stream"},
	}
}

func streamFixture(t *testing.T, p HTTPStreamPolicy, auth func(context.Context) error, handler http.HandlerFunc) (*HTTPStream, *httptest.Server, *atomic.Int32) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	executor, err := NewHTTPStream(p, auth)
	if err != nil {
		t.Fatal(err)
	}
	var lookup atomic.Int32
	executor.lookup = func(context.Context, string) ([]netip.Addr, error) {
		lookup.Add(1)
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	executor.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "8.8.8.8:443" {
			t.Errorf("unbound dial %q", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	executor.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "stream.example.com", RootCAs: roots}
	return executor, server, &lookup
}

func TestHTTPStreamChunksRealResponseWithCurrentChecks(t *testing.T) {
	var remote atomic.Int32
	executor, _, lookup := streamFixture(t, streamPolicyFixture(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		remote.Add(1)
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher := w.(http.Flusher)
		for i := 0; i < 4; i++ {
			_, _ = fmt.Fprintf(w, `{"part":%d}`+"\n", i)
			flusher.Flush()
			time.Sleep(time.Millisecond)
		}
	})
	var checks atomic.Int32
	var started bool
	var chunks [][]byte
	result, err := executor.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://stream.example.com/v1/stream", Accept: "application/x-ndjson"}, func(context.Context) error {
		checks.Add(1)
		return nil
	}, HTTPStreamCallbacks{
		Start: func(status int, contentType string) error {
			started = status == http.StatusOK && contentType == "application/x-ndjson"
			return nil
		},
		Chunk: func(data []byte) error {
			if len(data) > appplatform.MaxHTTPStreamFrameBytes {
				t.Fatal("oversized frame")
			}
			chunks = append(chunks, bytes.Clone(data))
			return nil
		},
	})
	if err != nil || !started || result.Status != http.StatusOK || result.Bytes == 0 || result.Frames < 1 || remote.Load() != 1 || lookup.Load() != 1 {
		t.Fatalf("result=%+v err=%v started=%v remote=%d lookup=%d", result, err, started, remote.Load(), lookup.Load())
	}
	if int(checks.Load()) < 3+result.Frames {
		t.Fatalf("checks=%d frames=%d", checks.Load(), result.Frames)
	}
	if got := strings.Join(func() []string {
		values := make([]string, len(chunks))
		for i := range chunks {
			values[i] = string(chunks[i])
		}
		return values
	}(), ""); got != "{\"part\":0}\n{\"part\":1}\n{\"part\":2}\n{\"part\":3}\n" {
		t.Fatalf("body=%q", got)
	}
}

func TestHTTPStreamRevocationAfterFramesNeverReleasesMoreData(t *testing.T) {
	executor, _, _ := streamFixture(t, streamPolicyFixture(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 8; i++ {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
		}
	})
	var emitted bytes.Buffer
	var checks atomic.Int32
	result, err := executor.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://stream.example.com/v1/stream", Accept: "text/event-stream"}, func(context.Context) error {
		if checks.Add(1) == 5 {
			return errors.New("revoked")
		}
		return nil
	}, HTTPStreamCallbacks{
		Start: func(int, string) error { return nil },
		Chunk: func(data []byte) error {
			emitted.Write(data)
			return nil
		},
	})
	if !errors.Is(err, ErrUncertain) || result.Status != 0 || emitted.Len() == 0 || strings.Contains(emitted.String(), "data: 7") {
		t.Fatalf("result=%+v err=%v emitted=%q", result, err, emitted.String())
	}
}

func TestHTTPStreamRefusesUpgradeEncodingRedirectAndSlowSource(t *testing.T) {
	for _, mode := range []string{"upgrade", "encoding", "redirect", "idle"} {
		t.Run(mode, func(t *testing.T) {
			executor, _, _ := streamFixture(t, streamPolicyFixture(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "upgrade":
					w.Header().Set("Connection", "Upgrade")
					w.Header().Set("Upgrade", "websocket")
					w.Header().Set("Content-Type", "text/event-stream")
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
					w.Header().Set("Content-Type", "text/event-stream")
				case "redirect":
					http.Redirect(w, r, "https://stream.example.com/v1/stream", http.StatusTemporaryRedirect)
					return
				case "idle":
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					time.Sleep(time.Second)
				}
			})
			_, err := executor.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://stream.example.com/v1/stream", Accept: "text/event-stream"}, nil, HTTPStreamCallbacks{
				Start: func(int, string) error { return nil },
			})
			if !errors.Is(err, ErrUncertain) {
				t.Fatalf("accepted %s: %v", mode, err)
			}
		})
	}
}

func TestHTTPStreamCredentialEchoAcrossFramesNeverLeaks(t *testing.T) {
	secret := []byte("Bearer disposable-stream-token")
	executor, _, _ := streamFixture(t, streamPolicyFixture(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher := w.(http.Flusher)
		for _, part := range [][]byte{[]byte("ok\nBearer disposable-"), []byte("stream-token\nnext\n")} {
			_, _ = w.Write(part)
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
		}
	})
	var emitted bytes.Buffer
	_, err := executor.Do(context.Background(), HTTPRequest{
		Method: "POST", URL: "https://stream.example.com/v1/stream", Accept: "application/x-ndjson",
		HeaderName: "Authorization", HeaderValue: secret,
	}, nil, HTTPStreamCallbacks{
		Start: func(int, string) error { return nil },
		Chunk: func(data []byte) error {
			emitted.Write(data)
			return nil
		},
	})
	if !errors.Is(err, ErrUncertain) || bytes.Contains(emitted.Bytes(), []byte("Bearer disposable-stream-token")) || bytes.Contains(emitted.Bytes(), []byte("disposable-stream-token")) {
		t.Fatalf("err=%v emitted=%q", err, emitted.String())
	}
}

func TestHTTPStreamCredentialGuardDeliversTheFinalBytes(t *testing.T) {
	executor, _, _ := streamFixture(t, streamPolicyFixture(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("complete\n"))
	})
	var emitted bytes.Buffer
	result, err := executor.Do(context.Background(), HTTPRequest{
		Method: "POST", URL: "https://stream.example.com/v1/stream", Accept: "application/x-ndjson",
		HeaderName: "Authorization", HeaderValue: []byte("Bearer disposable-stream-token"),
	}, nil, HTTPStreamCallbacks{
		Start: func(int, string) error { return nil },
		Chunk: func(data []byte) error {
			emitted.Write(data)
			return nil
		},
	})
	if err != nil || emitted.String() != "complete\n" || result.Bytes != 9 {
		t.Fatalf("result=%+v err=%v emitted=%q", result, err, emitted.String())
	}
}
