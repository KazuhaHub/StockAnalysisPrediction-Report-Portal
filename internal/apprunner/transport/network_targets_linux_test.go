//go:build linux

package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func targetsEndpoint(t *testing.T, networkEnabled bool, networks []appplatform.PrivateNetwork) string {
	t.Helper()
	_, _, _, a, _ := networkSessionFixture(t)
	path := filepath.Join(privateDir(t), "control.sock")
	server, err := Listen(path, uint32(os.Geteuid()), &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	if networkEnabled {
		provider, err := newNetworkProvider(networkEnrollmentFixture{a}, 2, 2, WithPrivateNetworks(networks))
		if err != nil {
			t.Fatal(err)
		}
		server.network = provider
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	return path
}

func TestNetworkTargetsQueryRoundTrip(t *testing.T) {
	networks := targetsNetworks()
	path := targetsEndpoint(t, true, networks)
	got, err := queryNetworkTargets(context.Background(), path, privateProfile(uint32(os.Geteuid())))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := NewNetworkTargets(networks)
	if got.Format != want.Format || got.PrivateNetworksSHA256 != want.PrivateNetworksSHA256 || len(got.PrivateNetworks) != 2 || got.PrivateNetworks[0].Prefix != "10.20.0.0/24" || got.PolicyFormats[1] != appplatform.HTTPPolicyV2Format {
		t.Fatalf("%+v", got)
	}
	// The runner with no allowlist advertises that no private target can be granted.
	path = targetsEndpoint(t, true, nil)
	got, err = queryNetworkTargets(context.Background(), path, privateProfile(uint32(os.Geteuid())))
	emptySHA, _ := appplatform.PrivateNetworksSHA256(nil)
	if err != nil || len(got.PrivateNetworks) != 0 || got.PrivateNetworksSHA256 != emptySHA {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNetworkTargetsQueryOnARunnerThatPredatesIt(t *testing.T) {
	// A runner without a network provider does not answer, like one that predates
	// the query: it closes the connection and the query reports no support.
	path := targetsEndpoint(t, false, nil)
	if _, err := queryNetworkTargets(context.Background(), path, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrNetworkTargetsUnsupported) {
		t.Fatalf("narrow runner: %v", err)
	}
	// A listener that reads the magic and closes with the nonce unread resets the
	// connection instead of ending it cleanly; that is unsupported too.
	reset := filepath.Join(privateDir(t), "old.sock")
	l, err := net.Listen("unix", reset)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(reset, 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var magic [8]byte
			_, _ = io.ReadFull(c, magic[:])
			c.Close()
		}
	}()
	if _, err := queryNetworkTargets(context.Background(), reset, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrNetworkTargetsUnsupported) {
		t.Fatalf("old runner: %v", err)
	}
}

func TestNetworkTargetsQueryRefusesWrongReplies(t *testing.T) {
	good, _ := NewNetworkTargets(targetsNetworks())
	body, _ := EncodeNetworkTargets(good)
	cases := map[string]func(nonce, reply []byte) []byte{
		// The control case: the fake endpoint is reachable, so the refusals below
		// are about the reply and not about the socket.
		"good": func(nonce, _ []byte) []byte { return reply(t, NetworkTargetsReplyMagic, nonce, body) },
		"wrong magic": func(nonce, _ []byte) []byte {
			return reply(t, "PRNX0001", nonce, body)
		},
		"wrong nonce": func(nonce, _ []byte) []byte {
			other := append([]byte(nil), nonce...)
			other[0] ^= 1
			return reply(t, NetworkTargetsReplyMagic, other, body)
		},
		"empty body":    func(nonce, _ []byte) []byte { return reply(t, NetworkTargetsReplyMagic, nonce, nil) },
		"garbage body":  func(nonce, _ []byte) []byte { return reply(t, NetworkTargetsReplyMagic, nonce, []byte("{}")) },
		"trailing byte": func(nonce, _ []byte) []byte { return append(reply(t, NetworkTargetsReplyMagic, nonce, body), 'x') },
		"oversize": func(nonce, _ []byte) []byte {
			big := make([]byte, 4097)
			return reply(t, NetworkTargetsReplyMagic, nonce, big)
		},
		"short body": func(nonce, _ []byte) []byte {
			r := reply(t, NetworkTargetsReplyMagic, nonce, body)
			return r[:len(r)-5]
		},
	}
	for name, build := range cases {
		path := filepath.Join(privateDir(t), "fake.sock")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		go func() {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			request := make([]byte, 40)
			if _, err := io.ReadFull(c, request); err != nil {
				return
			}
			_, _ = c.Write(build(request[8:], nil))
		}()
		_, err = queryNetworkTargets(context.Background(), path, privateProfile(uint32(os.Geteuid())))
		if name == "good" && err != nil {
			t.Errorf("control reply refused: %v", err)
		} else if name != "good" && !errors.Is(err, ErrNetworkTargetsUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
		l.Close()
	}
}

func reply(t *testing.T, magic string, nonce, body []byte) []byte {
	t.Helper()
	out := make([]byte, 44)
	copy(out, magic)
	copy(out[8:40], nonce)
	binary.BigEndian.PutUint32(out[40:], uint32(len(body)))
	return append(out, body...)
}
