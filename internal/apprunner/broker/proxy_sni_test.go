package broker

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// helloOptions shapes a synthetic ClientHello record.
type helloOptions struct {
	sni       string   // empty omits the server_name extension
	sniType   byte     // 0 (host_name) when zero value is left; set 1 to make a non host_name entry
	extras    [][]byte // raw extensions (type, length, data) appended after the standard ones
	noExts    bool     // omit the extensions block entirely
	duplicate bool     // repeat the server_name extension
	mutate    func(handshakeBody []byte) []byte
}

func ext(typ uint16, data []byte) []byte {
	out := binary.BigEndian.AppendUint16(nil, typ)
	out = binary.BigEndian.AppendUint16(out, uint16(len(data)))
	return append(out, data...)
}

func sniExtension(name string, nameType byte) []byte {
	entry := append([]byte{nameType}, binary.BigEndian.AppendUint16(nil, uint16(len(name)))...)
	entry = append(entry, name...)
	list := binary.BigEndian.AppendUint16(nil, uint16(len(entry)))
	return ext(0, append(list, entry...))
}

// greaseECH is the shape Chrome and curl_cffi send with no real configuration: an outer
// encrypted_client_hello extension (type 0xfe0d) holding random bytes.
func greaseECH() []byte {
	payload := append([]byte{0}, binary.BigEndian.AppendUint16(nil, 1)...) // outer, kdf id
	payload = binary.BigEndian.AppendUint16(payload, 1)                    // aead id
	payload = append(payload, 0x42)                                        // config id
	payload = binary.BigEndian.AppendUint16(payload, 32)
	payload = append(payload, bytes.Repeat([]byte{0xa5}, 32)...)
	payload = binary.BigEndian.AppendUint16(payload, 144)
	payload = append(payload, bytes.Repeat([]byte{0x5a}, 144)...)
	return ext(0xfe0d, payload)
}

func buildHello(o helloOptions) []byte {
	body := []byte{3, 3}
	body = append(body, bytes.Repeat([]byte{7}, 32)...)
	body = append(body, 32)
	body = append(body, bytes.Repeat([]byte{9}, 32)...)
	// GREASE cipher suite first, as a browser does.
	body = append(body, 0, 6, 0x0a, 0x0a, 0x13, 0x01, 0x13, 0x02)
	body = append(body, 1, 0)
	var exts []byte
	if o.sni != "" {
		exts = append(exts, sniExtension(o.sni, o.sniType)...)
		if o.duplicate {
			exts = append(exts, sniExtension(o.sni, o.sniType)...)
		}
	}
	exts = append(exts, ext(0x0a0a, nil)...)                                                                              // GREASE extension
	exts = append(exts, ext(43, []byte{4, 0x0a, 0x0a, 3, 4})...)                                                          // supported_versions
	exts = append(exts, ext(51, append([]byte{0x04, 0xc0, 0x11, 0xec, 0x04, 0xc0}, bytes.Repeat([]byte{3}, 1216)...))...) // a large hybrid key share
	for _, extra := range o.extras {
		exts = append(exts, extra...)
	}
	if !o.noExts {
		body = binary.BigEndian.AppendUint16(body, uint16(len(exts)))
		body = append(body, exts...)
	}
	if o.mutate != nil {
		body = o.mutate(body)
	}
	handshake := []byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	handshake = append(handshake, body...)
	record := []byte{0x16, 3, 1}
	record = binary.BigEndian.AppendUint16(record, uint16(len(handshake)))
	return append(record, handshake...)
}

func TestClientHelloSNIAcceptsBrowserShapes(t *testing.T) {
	for name, o := range map[string]helloOptions{
		"plain":             {sni: "qt.gtimg.cn"},
		"grease ech":        {sni: "qt.gtimg.cn", extras: [][]byte{greaseECH()}},
		"ech before sni":    {sni: "qt.gtimg.cn", extras: [][]byte{ext(0xfe0d, bytes.Repeat([]byte{1}, 200)), ext(0xff01, []byte{0})}},
		"upper case":        {sni: "QT.GTIMG.CN"},
		"padding extension": {sni: "qt.gtimg.cn", extras: [][]byte{ext(21, make([]byte, 100))}},
	} {
		got, err := parseClientHelloSNI(buildHello(o))
		if err != nil || !strings.EqualFold(got, o.sni) {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

func TestClientHelloSNIRefusals(t *testing.T) {
	good := buildHello(helloOptions{sni: "qt.gtimg.cn"})
	for name, record := range map[string][]byte{
		"empty":            nil,
		"short":            good[:4],
		"http":             []byte("GET / HTTP/1.1\r\nHost: qt.gtimg.cn\r\n\r\n"),
		"not a handshake":  append([]byte{0x17}, good[1:]...),
		"old version":      append([]byte{0x16, 2, 0}, good[3:]...),
		"truncated":        good[:len(good)-1],
		"trailing byte":    append(append([]byte(nil), good...), 0),
		"no extensions":    buildHello(helloOptions{sni: "qt.gtimg.cn", noExts: true}),
		"no sni":           buildHello(helloOptions{}),
		"duplicate sni":    buildHello(helloOptions{sni: "qt.gtimg.cn", duplicate: true}),
		"not a host name":  buildHello(helloOptions{sni: "qt.gtimg.cn", sniType: 1}),
		"empty name":       buildHello(helloOptions{extras: [][]byte{ext(0, []byte{0, 3, 0, 0, 0})}}),
		"duplicate type":   buildHello(helloOptions{sni: "qt.gtimg.cn", extras: [][]byte{ext(43, []byte{2, 3, 4})}}),
		"trailing dot":     buildHello(helloOptions{sni: "qt.gtimg.cn."}),
		"two entries":      buildHello(helloOptions{extras: [][]byte{ext(0, append(binary.BigEndian.AppendUint16(nil, 12), []byte{0, 0, 1, 'a', 0, 0, 1, 'b'}...))}}),
		"name with space":  buildHello(helloOptions{sni: "qt gtimg.cn"}),
		"name with slash":  buildHello(helloOptions{sni: "qt.gtimg.cn/x"}),
		"extension length": buildHello(helloOptions{sni: "qt.gtimg.cn", mutate: func(b []byte) []byte { return append(b, 0) }}),
	} {
		if got, err := parseClientHelloSNI(record); err == nil {
			t.Errorf("%s: accepted as %q", name, got)
		}
	}
	// A handshake message split across records cannot be judged and is refused.
	split := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(split[3:], uint16(len(good)-5-10))
	if _, err := parseClientHelloSNI(split[:len(split)-10]); err == nil {
		t.Error("a fragmented ClientHello was accepted")
	}
	// An oversize record is refused before anything is allocated for it.
	oversize := []byte{0x16, 3, 1, 0x40, 0x01}
	if _, err := parseClientHelloSNI(oversize); err == nil {
		t.Error("an oversize record was accepted")
	}
}

func TestReadFirstRecordBoundsAndTimes(t *testing.T) {
	good := buildHello(helloOptions{sni: "qt.gtimg.cn"})
	record, err := readFirstRecord(bytes.NewReader(good))
	if err != nil || !bytes.Equal(record, good) {
		t.Fatalf("read: %v", err)
	}
	for name, input := range map[string][]byte{
		"empty":    nil,
		"http":     []byte("GET / HTTP/1.1\r\n\r\n"),
		"oversize": {0x16, 3, 1, 0x40, 0x01},
		"short":    good[:20],
	} {
		if _, err := readFirstRecord(bytes.NewReader(input)); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	// Nothing is read past the declared length.
	reader := bytes.NewReader(append(append([]byte(nil), good...), []byte("more")...))
	if _, err := readFirstRecord(reader); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(reader)
	if string(rest) != "more" {
		t.Fatalf("read past the record: %q", rest)
	}
}

// A real Go TLS client hello must parse: it is the happy path of every Go app.
func TestClientHelloSNIParsesAGoClientHello(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: "push2.eastmoney.com", MinVersion: tls.VersionTLS12})
		_ = client.SetDeadline(time.Now().Add(time.Second))
		_ = c.Handshake()
		client.Close()
	}()
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	record, err := readFirstRecord(server)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseClientHelloSNI(record)
	if err != nil || got != "push2.eastmoney.com" {
		t.Fatalf("%q %v", got, err)
	}
}
