package broker

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

// The first-record check of a proxy tunnel (doc 184 section 5.5, step 5). The broker
// reads the first TLS record the application sends and judges the outer server name
// only. It decrypts nothing and keeps nothing: the record is forwarded unchanged once
// it passes. An encrypted_client_hello extension is never a reason to refuse on its
// own (Q3): Chrome and curl_cffi send a GREASE one with the real host in the outer
// name, and real ECH differs only in that the outer name is another, public one.

// maxProxyFirstRecord is the largest first record the broker reads: the TLS record
// plaintext limit.
const maxProxyFirstRecord = 16384

var (
	errProxyNotClientHello = errors.New("first record is not a single TLS ClientHello")
	errProxyNoServerName   = errors.New("ClientHello carries no usable server name")
)

// readFirstRecord reads exactly one TLS record from r: a five-byte header whose
// content type is handshake and whose length is within the limit, then that many
// bytes. It reads nothing beyond the record. The caller bounds the time with a
// deadline on the connection.
func readFirstRecord(r io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(header[3:]))
	if header[0] != 0x16 || header[1] != 3 || header[2] > 4 || length < 4 || length > maxProxyFirstRecord {
		return nil, errProxyNotClientHello
	}
	record := make([]byte, 5+length)
	copy(record, header[:])
	if _, err := io.ReadFull(r, record[5:]); err != nil {
		return nil, err
	}
	return record, nil
}

// parseClientHelloSNI returns the host_name of the server_name extension of a
// record that is exactly one complete ClientHello. A record that is anything else
// (a fragment of a larger handshake message, trailing bytes, an extension block
// that does not end where the message does), a duplicate extension type, a missing,
// repeated or non-host_name server name, or a name that is not a plain ASCII DNS
// name, is refused: such a hello cannot be judged, so nothing is forwarded.
func parseClientHelloSNI(record []byte) (string, error) {
	if len(record) < 5+4 || record[0] != 0x16 || record[1] != 3 || record[2] > 4 {
		return "", errProxyNotClientHello
	}
	length := int(binary.BigEndian.Uint16(record[3:]))
	if length > maxProxyFirstRecord || len(record) != 5+length {
		return "", errProxyNotClientHello
	}
	hello := record[5:]
	if hello[0] != 1 {
		return "", errProxyNotClientHello
	}
	body := hello[4:]
	if hlen := int(hello[1])<<16 | int(hello[2])<<8 | int(hello[3]); hlen != len(body) {
		return "", errProxyNotClientHello
	}
	cursor := reader{b: body}
	// legacy_version, random, legacy_session_id, cipher_suites, legacy_compression_methods.
	if !cursor.skip(2+32) || !cursor.skipVector(1) {
		return "", errProxyNotClientHello
	}
	suites, ok := cursor.vector(2)
	if !ok || len(suites) < 2 || len(suites)%2 != 0 || !cursor.skipVector(1) {
		return "", errProxyNotClientHello
	}
	block, ok := cursor.vector(2)
	if !ok || cursor.len() != 0 {
		return "", errProxyNotClientHello
	}
	seen := map[uint16]bool{}
	name := ""
	for len(block) > 0 {
		if len(block) < 4 {
			return "", errProxyNotClientHello
		}
		kind, size := binary.BigEndian.Uint16(block), int(binary.BigEndian.Uint16(block[2:]))
		if 4+size > len(block) || seen[kind] {
			return "", errProxyNotClientHello
		}
		seen[kind] = true
		if kind == 0 {
			parsed, err := parseServerNameExtension(block[4 : 4+size])
			if err != nil {
				return "", err
			}
			name = parsed
		}
		block = block[4+size:]
	}
	if name == "" {
		return "", errProxyNoServerName
	}
	return name, nil
}

// parseServerNameExtension reads the extension body: a list of exactly one entry,
// a host_name.
func parseServerNameExtension(data []byte) (string, error) {
	cursor := reader{b: data}
	list, ok := cursor.vector(2)
	if !ok || cursor.len() != 0 || len(list) < 3 || list[0] != 0 {
		return "", errProxyNoServerName
	}
	size := int(binary.BigEndian.Uint16(list[1:]))
	if size < 1 || 3+size != len(list) {
		return "", errProxyNoServerName
	}
	name := string(list[3:])
	if !plainDNSName(name) {
		return "", errProxyNoServerName
	}
	return name, nil
}

// plainDNSName admits ASCII letters, digits, hyphens and dots with no empty label
// and no trailing dot. It does not judge the name against any policy: the caller
// compares it with the CONNECT host.
func plainDNSName(name string) bool {
	if len(name) < 1 || len(name) > 253 || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// reader is a bounds-checked cursor: every method fails instead of reading past the
// end.
type reader struct{ b []byte }

func (r *reader) len() int { return len(r.b) }

func (r *reader) skip(n int) bool {
	if n < 0 || n > len(r.b) {
		return false
	}
	r.b = r.b[n:]
	return true
}

// vector reads a length-prefixed vector whose prefix is width bytes (1 or 2).
func (r *reader) vector(width int) ([]byte, bool) {
	if len(r.b) < width {
		return nil, false
	}
	size := int(r.b[0])
	if width == 2 {
		size = int(binary.BigEndian.Uint16(r.b))
	}
	if width+size > len(r.b) {
		return nil, false
	}
	out := r.b[width : width+size]
	r.b = r.b[width+size:]
	return out, true
}

func (r *reader) skipVector(width int) bool {
	_, ok := r.vector(width)
	return ok
}
