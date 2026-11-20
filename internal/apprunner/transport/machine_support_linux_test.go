//go:build linux

package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestMachineSupportQueryRoundTripAndEncoding(t *testing.T) {
	path := targetsEndpoint(t, false, nil)
	got, err := queryMachineSupport(context.Background(), path, privateProfile(uint32(os.Geteuid())))
	if err != nil || !got.Supports() || got.Format != MachineSupportFormat || len(got.AssertionPrefixes) != 1 || got.AssertionPrefixes[0] != "mch." {
		t.Fatalf("%+v %v", got, err)
	}
	body, err := EncodeMachineSupport(NewMachineSupport())
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"empty": nil, "braces": []byte("{}"), "unknown key": []byte(`{"format":"` + MachineSupportFormat + `","assertion_prefixes":["mch."],"x":1}`),
		"other prefix": []byte(`{"format":"` + MachineSupportFormat + `","assertion_prefixes":["pub."]}`), "other format": []byte(`{"format":"x","assertion_prefixes":["mch."]}`),
		"padded": append(append([]byte(nil), body...), ' '),
	} {
		if _, err := ParseMachineSupport(raw); !errors.Is(err, ErrMachineSupportUnsupported) {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := ParseMachineSupport(body); err != nil {
		t.Fatal(err)
	}
}

// A runner that predates the query reads the magic and closes; the query repeats once and then
// concludes the runner does not support machine assertions.
func TestMachineSupportQueryOnARunnerThatPredatesIt(t *testing.T) {
	old := machineSupportRetryDelay
	machineSupportRetryDelay = time.Millisecond
	t.Cleanup(func() { machineSupportRetryDelay = old })
	path := filepath.Join(privateDir(t), "old.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			attempts.Add(1)
			var magic [8]byte
			_, _ = io.ReadFull(c, magic[:])
			c.Close()
		}
	}()
	if _, err := queryMachineSupport(context.Background(), path, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrMachineSupportUnsupported) {
		t.Fatalf("old runner: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestMachineSupportQueryRefusesWrongReplies(t *testing.T) {
	body, _ := EncodeMachineSupport(NewMachineSupport())
	cases := map[string]func(nonce []byte) []byte{
		"good":        func(nonce []byte) []byte { return reply(t, MachineSupportReplyMagic, nonce, body) },
		"wrong magic": func(nonce []byte) []byte { return reply(t, MachineSupportReplyMagic[:7]+"X", nonce, body) },
		"wrong nonce": func(nonce []byte) []byte {
			other := append([]byte(nil), nonce...)
			other[0] ^= 1
			return reply(t, MachineSupportReplyMagic, other, body)
		},
		"network targets magic": func(nonce []byte) []byte { return reply(t, NetworkTargetsReplyMagic, nonce, body) },
		"garbage":               func(nonce []byte) []byte { return reply(t, MachineSupportReplyMagic, nonce, []byte("{}")) },
		"trailing":              func(nonce []byte) []byte { return append(reply(t, MachineSupportReplyMagic, nonce, body), 'x') },
		"oversize":              func(nonce []byte) []byte { return reply(t, MachineSupportReplyMagic, nonce, make([]byte, 513)) },
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
			_, _ = c.Write(build(request[8:]))
		}()
		_, err = queryMachineSupport(context.Background(), path, privateProfile(uint32(os.Geteuid())))
		if name == "good" && err != nil {
			t.Errorf("control reply refused: %v", err)
		} else if name != "good" && !errors.Is(err, ErrMachineSupportUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
		l.Close()
	}
}
