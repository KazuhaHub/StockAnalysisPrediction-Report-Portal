package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxResponseBytes = 1 << 20

var safeVersion = regexp.MustCompile(`^[0-9A-Za-z.+_~-]{1,64}$`)

func apiMinor(value string) (int, bool) {
	major, minor, ok := strings.Cut(value, ".")
	if !ok || major != "1" || len(minor) < 1 || len(minor) > 3 || (len(minor) > 1 && minor[0] == '0') {
		return 0, false
	}
	for _, c := range minor {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(minor)
	return n, err == nil
}

func readJSON(ctx context.Context, client *http.Client, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://app-runner-docker.invalid"+path, nil)
	if err != nil {
		return ErrUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ctx.Err()
		}
		if errors.Is(err, ErrUnsafeSocket) {
			return ErrUnsafeSocket
		}
		if errors.Is(err, ErrAccessDenied) {
			return ErrAccessDenied
		}
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	if len(raw) > maxResponseBytes || !validProjection(raw, out) {
		return ErrInvalidResponse
	}
	// Unknown daemon fields are deliberately ignored, never copied into reports.
	// No response value selects a URL, operation, path or implementation.
	if err := json.Unmarshal(raw, out); err != nil {
		return ErrInvalidResponse
	}
	return nil
}

// Docker has many non-projected fields and may add more. Accept bounded unknown
// fields, while refusing duplicate keys and case aliases of projected root fields.
func validProjection(raw []byte, out any) bool {
	if !utf8.Valid(raw) {
		return false
	}
	typ := reflect.TypeOf(out)
	if typ == nil || typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct {
		return false
	}
	typ = typ.Elem()
	known := []string{}
	for i := range typ.NumField() {
		field := typ.Field(i)
		name := field.Name
		if tag, ok := field.Tag.Lookup("json"); ok {
			name, _, _ = strings.Cut(tag, ",")
		}
		known = append(known, name)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	var object func(int, bool) bool
	var value func(int) bool
	object = func(depth int, root bool) bool {
		if depth > 32 {
			return false
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || len(key) > 4096 || seen[key] || len(seen) >= 2048 {
				return false
			}
			if root {
				for _, canonical := range known {
					if strings.EqualFold(key, canonical) && key != canonical {
						return false
					}
				}
			}
			seen[key] = true
			if !value(depth + 1) {
				return false
			}
		}
		last, err := decoder.Token()
		return err == nil && last == json.Delim('}')
	}
	value = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch token {
		case json.Delim('{'):
			return object(depth, false)
		case json.Delim('['):
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
			last, err := decoder.Token()
			return err == nil && last == json.Delim(']')
		}
		_, delimiter := token.(json.Delim)
		return !delimiter
	}
	if !object(1, true) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func inspectHTTP(ctx context.Context, client *http.Client, arch string) (Report, error) {
	r := initialReport()
	var v versionResponse
	if err := readJSON(ctx, client, "/version", &v); err != nil {
		return r, err
	}
	if !safeVersion.MatchString(v.Version) {
		return r, ErrInvalidResponse
	}
	r.EngineVersion = v.Version
	maximum, maxOK := apiMinor(v.APIVersion)
	minimum, minOK := apiMinor(v.MinAPIVersion)
	if !maxOK || !minOK || minimum > maximum {
		return r, ErrInvalidResponse
	}
	if minimum > 47 || maximum < 47 {
		r.Findings = append(r.Findings, Finding{"docker_api_incompatible", "unsupported"})
		return r, nil
	}
	var info infoResponse
	if err := readJSON(ctx, client, "/v"+APIVersion+"/info", &info); err != nil {
		return r, err
	}
	if info.ServerVersion != v.Version || info.NCPU < 1 || info.NCPU > 1<<20 || info.MemTotal < 16<<20 || info.MemTotal > 1<<60 || len(info.SecurityOptions) > 32 {
		return r, ErrInvalidResponse
	}
	return assess(v, info, arch), nil
}
