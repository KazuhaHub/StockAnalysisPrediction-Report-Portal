package appplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

var ErrBackendReadiness = errors.New("backend readiness response unavailable")

// BackendReadiness is the provisional credential-free GET /health/ready body.
// The platform independently binds this response to the actual candidate.
type BackendReadiness struct {
	Protocol   string `json:"protocol"`
	Ready      bool   `json:"ready"`
	DataFormat string `json:"data_format"`
}

func ParseBackendReadiness(raw []byte) (BackendReadiness, error) {
	var result BackendReadiness
	if len(raw) == 0 || len(raw) > 4096 {
		return result, ErrBackendReadiness
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return result, ErrBackendReadiness
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return BackendReadiness{}, ErrBackendReadiness
		}
		seen[name] = true
		switch name {
		case "protocol":
			err = d.Decode(&result.Protocol)
		case "ready":
			value, e := d.Token()
			if e != nil {
				return BackendReadiness{}, ErrBackendReadiness
			}
			result.Ready, ok = value.(bool)
			if !ok {
				return BackendReadiness{}, ErrBackendReadiness
			}
		case "data_format":
			err = d.Decode(&result.DataFormat)
		default:
			return BackendReadiness{}, ErrBackendReadiness
		}
		if err != nil {
			return BackendReadiness{}, ErrBackendReadiness
		}
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') || len(seen) != 3 {
		return BackendReadiness{}, ErrBackendReadiness
	}
	if _, err := d.Token(); err != io.EOF {
		return BackendReadiness{}, ErrBackendReadiness
	}
	n, err := strconv.ParseInt(result.DataFormat, 10, 64)
	if err != nil || n < 1 || n > 1000000 || strconv.FormatInt(n, 10) != result.DataFormat || result.Protocol != "portal.backend/1" {
		return BackendReadiness{}, ErrBackendReadiness
	}
	return result, nil
}
