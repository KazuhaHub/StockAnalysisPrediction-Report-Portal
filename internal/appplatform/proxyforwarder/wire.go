// Package proxyforwarder is the reference for the application side of the reviewed
// forward proxy (doc 184): the PRXY0001 stream client and a minimal HTTP CONNECT
// forwarder that listens inside the container and relays each tunnel over the
// runner's broker socket. The Apps repository mirrors this package; it holds no
// authority, every decision is the broker's.
package proxyforwarder

import (
	"encoding/binary"
	"errors"
	"io"
	"strconv"
)

const (
	// OpenMagic starts a tunnel on the broker socket: forwarder -> broker. It is
	// followed by a big-endian u16 length and "host:port" of that many bytes.
	OpenMagic = "PRXY0001"
	// ReplyMagic starts the broker's answer: broker -> forwarder. It is followed by
	// a status byte and a code byte. After a StatusOpen answer both sides carry raw
	// bytes until either closes.
	ReplyMagic = "PRXR0001"
	// MaxOpenBytes bounds the open payload: a host of at most 253 bytes, a colon and
	// a port of at most five digits.
	MaxOpenBytes = 253 + 1 + 5
)

// Reply status bytes.
const (
	StatusOpen    byte = 1
	StatusRefused byte = 2
)

// Refusal is a broker refusal code (doc 184 section 8). The zero value is no
// refusal.
type Refusal string

const (
	RefusalNone             Refusal = ""
	RefusalNotGranted       Refusal = "not_granted"
	RefusalHostNotReviewed  Refusal = "host_not_reviewed"
	RefusalPortNotPermitted Refusal = "port_not_permitted"
	RefusalAddressDenied    Refusal = "address_denied"
	RefusalResolveFailed    Refusal = "resolve_failed"
	RefusalConnectFailed    Refusal = "connect_failed"
	RefusalBusy             Refusal = "busy"
	RefusalBudgetSpent      Refusal = "budget_spent"
	RefusalUnavailable      Refusal = "unavailable"
)

// refusalCodes is the wire byte of each refusal. A byte outside the table is an
// unavailable answer, never an open one.
var refusalCodes = map[Refusal]byte{
	RefusalNotGranted: 1, RefusalHostNotReviewed: 2, RefusalPortNotPermitted: 3, RefusalAddressDenied: 4,
	RefusalResolveFailed: 5, RefusalConnectFailed: 6, RefusalBusy: 7, RefusalBudgetSpent: 8, RefusalUnavailable: 9,
}

// ErrProtocol is any malformed open or reply.
var ErrProtocol = errors.New("proxy stream protocol violation")

// EncodeOpen builds the open frame of a tunnel to host:port.
func EncodeOpen(host string, port int) ([]byte, error) {
	target := host + ":" + strconv.Itoa(port)
	if host == "" || len(host) > 253 || port < 1 || port > 65535 || len(target) > MaxOpenBytes {
		return nil, ErrProtocol
	}
	frame := make([]byte, 0, len(OpenMagic)+2+len(target))
	frame = append(frame, OpenMagic...)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(target)))
	return append(frame, target...), nil
}

// ReadOpen reads the open frame that follows an already consumed OpenMagic, or the
// whole frame when magic is true. The length is checked before the payload is read.
func ReadOpen(r io.Reader, magic bool) (host string, port int, err error) {
	if magic {
		var m [len(OpenMagic)]byte
		if _, err := io.ReadFull(r, m[:]); err != nil {
			return "", 0, err
		}
		if string(m[:]) != OpenMagic {
			return "", 0, ErrProtocol
		}
	}
	var n [2]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return "", 0, err
	}
	length := int(binary.BigEndian.Uint16(n[:]))
	if length < 3 || length > MaxOpenBytes {
		return "", 0, ErrProtocol
	}
	target := make([]byte, length)
	if _, err := io.ReadFull(r, target); err != nil {
		return "", 0, err
	}
	return splitTarget(string(target))
}

// splitTarget splits "host:port" at the last colon. The host may not be empty and
// the port is decimal digits without sign or leading zero. Nothing about the host
// is judged here: that is the broker's host check.
func splitTarget(target string) (string, int, error) {
	for i := len(target) - 1; i >= 0; i-- {
		if target[i] != ':' {
			continue
		}
		host, digits := target[:i], target[i+1:]
		port, err := strconv.Atoi(digits)
		if host == "" || len(host) > 253 || err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != digits {
			return "", 0, ErrProtocol
		}
		return host, port, nil
	}
	return "", 0, ErrProtocol
}

// EncodeReply builds the broker's answer: an open one for RefusalNone, a refusal
// otherwise.
func EncodeReply(refusal Refusal) ([]byte, error) {
	frame := append([]byte(nil), ReplyMagic...)
	if refusal == RefusalNone {
		return append(frame, StatusOpen, 0), nil
	}
	code, ok := refusalCodes[refusal]
	if !ok {
		return nil, ErrProtocol
	}
	return append(frame, StatusRefused, code), nil
}

// ReadReply reads the broker's answer. It returns RefusalNone for an open tunnel.
func ReadReply(r io.Reader) (Refusal, error) {
	var frame [len(ReplyMagic) + 2]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil {
		return RefusalNone, err
	}
	if string(frame[:len(ReplyMagic)]) != ReplyMagic {
		return RefusalNone, ErrProtocol
	}
	status, code := frame[len(ReplyMagic)], frame[len(ReplyMagic)+1]
	switch {
	case status == StatusOpen && code == 0:
		return RefusalNone, nil
	case status == StatusRefused:
		for refusal, b := range refusalCodes {
			if b == code {
				return refusal, nil
			}
		}
		return RefusalUnavailable, nil
	}
	return RefusalNone, ErrProtocol
}
