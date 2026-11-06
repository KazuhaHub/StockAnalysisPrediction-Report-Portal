package appplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

var ErrBackendHandshake = errors.New("backend handshake unavailable")
var backendHandshakeToken = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

func ValidBackendHandshakeToken(token string) bool {
	return len(token) > 0 && len(token) <= 4096 && backendHandshakeToken.MatchString(token)
}
func BackendHandshakeRequest(challenge string) ([]byte, error) {
	if !ValidBackendHandshakeToken(challenge) {
		return nil, ErrBackendHandshake
	}
	return json.Marshal(struct {
		Challenge string `json:"challenge"`
	}{challenge})
}
func ParseBackendHandshakeChallenge(raw []byte) (string, error) {
	return parseBackendHandshakeField(raw, "challenge")
}
func ParseBackendHandshakeAnswer(raw []byte) (string, error) {
	return parseBackendHandshakeField(raw, "answer")
}
func parseBackendHandshakeField(raw []byte, field string) (string, error) {
	if len(raw) == 0 || len(raw) > 8192 {
		return "", ErrBackendHandshake
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return "", ErrBackendHandshake
	}
	key, err := d.Token()
	if err != nil || key != field {
		return "", ErrBackendHandshake
	}
	value, err := d.Token()
	token, ok := value.(string)
	if err != nil || !ok || !ValidBackendHandshakeToken(token) {
		return "", ErrBackendHandshake
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return "", ErrBackendHandshake
	}
	if _, err := d.Token(); err != io.EOF {
		return "", ErrBackendHandshake
	}
	return token, nil
}
