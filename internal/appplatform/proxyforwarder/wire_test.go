package proxyforwarder

import (
	"bytes"
	"strings"
	"testing"
)

func TestOpenRoundTrip(t *testing.T) {
	frame, err := EncodeOpen("qt.gtimg.cn", 443)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := ReadOpen(bytes.NewReader(frame), true)
	if err != nil || host != "qt.gtimg.cn" || port != 443 {
		t.Fatalf("got %q %d %v", host, port, err)
	}
	// The same frame without its magic, as the broker reads it after sniffing.
	host, port, err = ReadOpen(bytes.NewReader(frame[len(OpenMagic):]), false)
	if err != nil || host != "qt.gtimg.cn" || port != 443 {
		t.Fatalf("got %q %d %v", host, port, err)
	}
}

func TestOpenBounds(t *testing.T) {
	long := strings.Repeat("a", 253)
	if _, err := EncodeOpen(long, 65535); err != nil {
		t.Fatalf("the longest legal open was refused: %v", err)
	}
	if _, err := EncodeOpen(long+"a", 443); err == nil {
		t.Fatal("an over-long host was encoded")
	}
	for _, bad := range []struct {
		host string
		port int
	}{{"", 443}, {"a.example.com", 0}, {"a.example.com", 65536}} {
		if _, err := EncodeOpen(bad.host, bad.port); err == nil {
			t.Fatalf("%+v was encoded", bad)
		}
	}
	// A declared length above the bound is refused before any payload is read.
	oversize := append([]byte(OpenMagic), 0x01, 0x04)
	if _, _, err := ReadOpen(bytes.NewReader(oversize), true); err != ErrProtocol {
		t.Fatalf("oversize open: %v", err)
	}
	for _, payload := range []string{"nocolon", ":443", "a.example.com:", "a.example.com:0443", "a.example.com:+443", "a.example.com:99999", "a.example.com:x"} {
		frame := append([]byte(OpenMagic), byte(len(payload)>>8), byte(len(payload)))
		frame = append(frame, payload...)
		if _, _, err := ReadOpen(bytes.NewReader(frame), true); err == nil {
			t.Errorf("%q was read", payload)
		}
	}
	if _, _, err := ReadOpen(strings.NewReader("PRXY0002\x00\x05a.b:1"), true); err != ErrProtocol {
		t.Fatalf("wrong magic: %v", err)
	}
}

func TestReplyRoundTrip(t *testing.T) {
	for _, refusal := range []Refusal{RefusalNone, RefusalNotGranted, RefusalHostNotReviewed, RefusalPortNotPermitted, RefusalAddressDenied, RefusalResolveFailed, RefusalConnectFailed, RefusalBusy, RefusalBudgetSpent, RefusalUnavailable} {
		frame, err := EncodeReply(refusal)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReadReply(bytes.NewReader(frame))
		if err != nil || got != refusal {
			t.Fatalf("%q: got %q %v", refusal, got, err)
		}
	}
	if _, err := EncodeReply("sni_mismatch"); err == nil {
		t.Fatal("a code outside the table was encoded")
	}
	// An unknown refusal byte is unavailable, never an open tunnel.
	got, err := ReadReply(strings.NewReader(ReplyMagic + "\x02\xfe"))
	if err != nil || got != RefusalUnavailable {
		t.Fatalf("unknown refusal byte: %q %v", got, err)
	}
	for _, bad := range []string{ReplyMagic + "\x01\x01", ReplyMagic + "\x03\x00", "PRXR0002\x01\x00"} {
		if _, err := ReadReply(strings.NewReader(bad)); err != ErrProtocol {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
