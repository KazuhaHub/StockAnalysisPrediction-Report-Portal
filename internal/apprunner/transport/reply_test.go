package transport

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReplyFrameIsCanonicalAndBounded(t *testing.T) {
	correlation := bytes.Repeat([]byte{7}, 32)
	payload := []byte("canonical reply")
	reply, err := buildReplyFrame("PRXY0001", correlation, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply) != 44+len(payload) || string(reply[:8]) != "PRXY0001" ||
		!bytes.Equal(reply[8:40], correlation) || binary.BigEndian.Uint32(reply[40:44]) != uint32(len(payload)) ||
		!bytes.Equal(reply[44:], payload) {
		t.Fatal("reply frame does not match its declared metadata")
	}
	for name, test := range map[string]struct {
		magic       string
		correlation []byte
		payload     []byte
	}{
		"magic":       {magic: "short", correlation: correlation, payload: payload},
		"correlation": {magic: "PRXY0001", correlation: correlation[:31], payload: payload},
		"empty":       {magic: "PRXY0001", correlation: correlation},
		"oversized":   {magic: "PRXY0001", correlation: correlation, payload: make([]byte, maxReplyPayloadBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if reply, err := buildReplyFrame(test.magic, test.correlation, test.payload); err == nil || reply != nil {
				t.Fatal("invalid reply metadata accepted")
			}
		})
	}
}
