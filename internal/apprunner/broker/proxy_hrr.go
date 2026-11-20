package broker

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"sync/atomic"
)

// The second ClientHello after a HelloRetryRequest (doc 184 section 5.5, step 5). A TLS 1.3 server may answer the
// first ClientHello with a HelloRetryRequest, and the client then sends a second ClientHello on the same
// connection. The first was judged before it was forwarded; so is the second, or the check could be dodged by
// sending a harmless hello first. The broker does not read server records beyond the first: the first record
// tells it whether this is a retry, and until that is known, and until the second hello has been judged when it
// is, client records are read whole and only well-formed ones are forwarded.

// helloRetryRequestRandom is the fixed value in the random field of a HelloRetryRequest (RFC 8446, 4.1.3).
var helloRetryRequestRandom = []byte{
	0xCF, 0x21, 0xAD, 0x74, 0xE5, 0x9A, 0x61, 0x11, 0xBE, 0x1D, 0x8C, 0x02, 0x1E, 0x65, 0xB8, 0x91,
	0xC2, 0xA2, 0x11, 0x16, 0x7A, 0xBB, 0x8C, 0x5E, 0x07, 0x9E, 0x09, 0xE2, 0xC8, 0xA8, 0x33, 0x9C,
}

// maxProxyRecordBody is the largest TLS record body: 2^14 plaintext plus the 256 bytes of expansion.
const maxProxyRecordBody = 16384 + 256

// hrrGate is what the two directions of a tunnel tell each other.
type hrrGate struct {
	decided atomic.Bool // the server's first record has been seen
	retry   atomic.Bool // it was a HelloRetryRequest
	second  atomic.Bool // and the client's second ClientHello has been judged
}

// settled says no client record needs judging any more.
func (g *hrrGate) settled() bool {
	return g.decided.Load() && (!g.retry.Load() || g.second.Load())
}

// isHelloRetryRequest says whether a handshake message is a ServerHello with the HelloRetryRequest random.
func isHelloRetryRequest(handshake []byte) bool {
	return len(handshake) >= 38 && handshake[0] == 2 && bytes.Equal(handshake[6:38], helloRetryRequestRandom)
}

// tlsRecordHeader reports whether five bytes look like a TLS record header and returns the body length.
func tlsRecordHeader(h []byte) (kind byte, length int, ok bool) {
	length = int(binary.BigEndian.Uint16(h[3:]))
	ok = (h[0] >= 0x14 && h[0] <= 0x17) && h[1] == 3 && h[2] <= 4 && length >= 1 && length <= maxProxyRecordBody
	return h[0], length, ok
}

// serverFirstRecord reads the server's first record, decides whether it is a HelloRetryRequest and passes the
// bytes on. Anything that does not look like a TLS record is passed on as it is: the server speaks to its own
// client, and only a client's bytes are ever judged.
func (t *proxyTunnel) serverFirstRecord(src io.Reader, pass func([]byte) bool) error {
	var header [5]byte
	n, err := io.ReadFull(src, header[:])
	if err != nil {
		t.gate.decided.Store(true)
		if n > 0 && !pass(header[:n]) {
			return nil
		}
		return err
	}
	kind, length, ok := tlsRecordHeader(header[:])
	if !ok || kind != 0x16 {
		t.gate.decided.Store(true)
		pass(header[:])
		return nil
	}
	body := make([]byte, length)
	got, err := io.ReadFull(src, body)
	if err == nil && isHelloRetryRequest(body) {
		t.gate.retry.Store(true)
	}
	t.gate.decided.Store(true)
	if !pass(header[:]) {
		return nil
	}
	if got > 0 {
		pass(body[:got])
	}
	return err
}

// clientRecords forwards the client's records one whole record at a time while a second ClientHello may still
// come, and returns when none can. A hello after a retry must be a single record whose server name is the CONNECT
// host; before it only a ChangeCipherSpec may follow the retry. It returns false, nil after closing the tunnel
// for a record it refuses, and the read error for the caller to treat like any other end of the client's side.
func (t *proxyTunnel) clientRecords(src io.Reader, pass func([]byte) bool) (bool, error) {
	for !t.gate.settled() {
		var header [5]byte
		if n, err := io.ReadFull(src, header[:]); err != nil {
			if n > 0 && !pass(header[:n]) {
				return false, nil
			}
			return true, err
		}
		kind, length, ok := tlsRecordHeader(header[:])
		if !ok {
			t.fail(ProxyCloseSNIMismatch)
			return false, nil
		}
		record := make([]byte, 5+length)
		copy(record, header[:])
		if _, err := io.ReadFull(src, record[5:]); err != nil {
			return true, err
		}
		if t.gate.retry.Load() && !t.gate.second.Load() {
			switch kind {
			case 0x14:
			case 0x16:
				name, err := parseClientHelloSNI(record)
				if err != nil || strings.ToLower(name) != t.host {
					t.fail(ProxyCloseSNIMismatch)
					return false, nil
				}
				t.gate.second.Store(true)
			default:
				t.fail(ProxyCloseSNIMismatch)
				return false, nil
			}
		}
		if !pass(record) {
			return false, nil
		}
	}
	return true, nil
}
