package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxPlatformObjectBytes        = 1 << 20
	maxPlatformObjectRequestBytes = 8 << 20
)

var (
	errPlatformObjectInvalid     = errors.New("application object request is invalid")
	errPlatformObjectConflict    = errors.New("application object revision conflicts")
	errPlatformObjectStorageFull = errors.New("application object storage is full")
)

var (
	platformObjectCollectionPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	platformObjectRequestPattern    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	platformObjectMediaTypePattern  = regexp.MustCompile(`^[a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+-]+$`)
)

type platformObjectBinding struct {
	Generation     string `json:"generation"`
	PolicyRevision string `json:"policyRevision"`
	ReleaseSHA256  string `json:"releaseSha256"`
}

type platformObjectRequest struct {
	binding          platformObjectBinding
	action           string
	collection       string
	cursor           string
	limit            int
	id               string
	requestID        string
	expectedRevision int64
	label            string
	mediaType        string
	content          []byte
}

type platformObjectWireRequest struct {
	Binding          platformObjectBinding `json:"binding"`
	Action           string                `json:"action"`
	Collection       string                `json:"collection,omitempty"`
	Cursor           string                `json:"cursor,omitempty"`
	Limit            int                   `json:"limit,omitempty"`
	ID               string                `json:"id,omitempty"`
	RequestID        string                `json:"requestId,omitempty"`
	ExpectedRevision int64                 `json:"expectedRevision,omitempty"`
	Label            string                `json:"label,omitempty"`
	MediaType        string                `json:"mediaType,omitempty"`
	ContentBase64    string                `json:"contentBase64,omitempty"`
}

func platformJSONWithoutDuplicateKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if !platformJSONValueUnique(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func platformJSONValueUnique(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return false
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !platformJSONValueUnique(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !platformJSONValueUnique(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

func platformObjectTopLevel(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxPlatformObjectRequestBytes || !utf8.Valid(raw) || !platformJSONWithoutDuplicateKeys(raw) {
		return nil, errPlatformObjectInvalid
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&fields) != nil || fields == nil {
		return nil, errPlatformObjectInvalid
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return nil, errPlatformObjectInvalid
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errPlatformObjectInvalid
		}
	}
	return fields, nil
}

func platformObjectKeysMatch(fields map[string]json.RawMessage, required, optional []string) bool {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func parsePlatformObjectBinding(fields map[string]json.RawMessage) (platformObjectBinding, error) {
	var zero platformObjectBinding
	raw, ok := fields["binding"]
	if !ok {
		return zero, errPlatformObjectInvalid
	}
	var bindingFields map[string]json.RawMessage
	if json.Unmarshal(raw, &bindingFields) != nil || bindingFields == nil || !platformObjectKeysMatch(bindingFields, []string{"generation", "policyRevision", "releaseSha256"}, nil) {
		return zero, errPlatformObjectInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&zero) != nil || !platformPositiveCounter(zero.Generation) || !platformPositiveCounter(zero.PolicyRevision) || !platformValidSHA(zero.ReleaseSHA256) {
		return platformObjectBinding{}, errPlatformObjectInvalid
	}
	return zero, nil
}

func parsePlatformObjectRequest(raw []byte) (platformObjectRequest, error) {
	var zero platformObjectRequest
	fields, err := platformObjectTopLevel(raw)
	if err != nil {
		return zero, err
	}
	binding, err := parsePlatformObjectBinding(fields)
	if err != nil {
		return zero, err
	}
	var action string
	if json.Unmarshal(fields["action"], &action) != nil {
		return zero, errPlatformObjectInvalid
	}
	var required, optional []string
	switch action {
	case "list":
		required = []string{"binding", "action", "collection"}
		optional = []string{"cursor", "limit"}
	case "get":
		required = []string{"binding", "action", "id"}
	case "create":
		required = []string{"binding", "action", "requestId", "collection", "label", "mediaType", "contentBase64"}
	case "update":
		required = []string{"binding", "action", "id", "expectedRevision", "label", "mediaType", "contentBase64"}
	case "delete":
		required = []string{"binding", "action", "id", "expectedRevision"}
	default:
		return zero, errPlatformObjectInvalid
	}
	if !platformObjectKeysMatch(fields, required, optional) {
		return zero, errPlatformObjectInvalid
	}
	var wire platformObjectWireRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil {
		return zero, errPlatformObjectInvalid
	}
	request := platformObjectRequest{
		binding:          binding,
		action:           action,
		collection:       wire.Collection,
		cursor:           wire.Cursor,
		limit:            wire.Limit,
		id:               wire.ID,
		requestID:        wire.RequestID,
		expectedRevision: wire.ExpectedRevision,
		label:            wire.Label,
		mediaType:        wire.MediaType,
	}
	switch action {
	case "list":
		if !platformValidObjectCollection(request.collection) || (request.cursor != "" && !platformValidObjectCursor(request.cursor)) || request.limit < 0 || request.limit > 50 {
			return zero, errPlatformObjectInvalid
		}
		if request.limit == 0 {
			request.limit = 20
		}
	case "get":
		if !platformValidUUID(request.id) {
			return zero, errPlatformObjectInvalid
		}
	case "create":
		if !platformObjectRequestPattern.MatchString(request.requestID) || !platformValidObjectCollection(request.collection) || !platformValidObjectLabel(request.label) || !platformObjectMediaTypePattern.MatchString(request.mediaType) {
			return zero, errPlatformObjectInvalid
		}
		request.content, err = parsePlatformObjectContent(wire.ContentBase64)
		if err != nil {
			return zero, err
		}
	case "update":
		if !platformValidUUID(request.id) || request.expectedRevision < 1 || !platformValidObjectLabel(request.label) || !platformObjectMediaTypePattern.MatchString(request.mediaType) {
			return zero, errPlatformObjectInvalid
		}
		request.content, err = parsePlatformObjectContent(wire.ContentBase64)
		if err != nil {
			return zero, err
		}
	case "delete":
		if !platformValidUUID(request.id) || request.expectedRevision < 1 {
			return zero, errPlatformObjectInvalid
		}
	}
	return request, nil
}

func platformValidObjectCollection(value string) bool {
	return platformObjectCollectionPattern.MatchString(value)
}

func platformValidObjectLabel(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func platformValidObjectCursor(value string) bool {
	millis, id, ok := strings.Cut(value, ":")
	if !ok || !platformValidUUID(id) {
		return false
	}
	counter, err := strconv.ParseInt(millis, 10, 64)
	return err == nil && counter > 0
}

func parsePlatformObjectContent(value string) ([]byte, error) {
	if len(value) > base64.StdEncoding.EncodedLen(maxPlatformObjectBytes) {
		return nil, errPlatformObjectInvalid
	}
	content, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(content) > maxPlatformObjectBytes || base64.StdEncoding.EncodeToString(content) != value {
		return nil, errPlatformObjectInvalid
	}
	return content, nil
}

type platformObjectOwner struct {
	Username          string
	AccountCreated    string
	AccountGeneration string
}

func samePlatformObjectOwner(a, b platformObjectOwner) bool {
	return a.Username != "" && a.Username == b.Username && a.AccountCreated != "" && a.AccountCreated == b.AccountCreated && a.AccountGeneration != "" && a.AccountGeneration == b.AccountGeneration
}

func checkPlatformObjectRevision(current, expected int64) error {
	if current < 1 || expected < 1 {
		return errPlatformObjectInvalid
	}
	if current != expected {
		return errPlatformObjectConflict
	}
	return nil
}

type platformObjectUsage struct {
	Bytes, Objects       int64
	MaxBytes, MaxObjects int64
}

type platformObjectQuota struct {
	global, namespace, account platformObjectUsage
}

func admitPlatformObjectQuota(quota platformObjectQuota, addingBytes, addingObjects, removingBytes, removingObjects int64) error {
	if addingBytes < 0 || removingBytes < 0 || (addingObjects != 0 && addingObjects != 1) || (removingObjects != 0 && removingObjects != 1) {
		return errPlatformObjectInvalid
	}
	for _, usage := range []platformObjectUsage{quota.global, quota.namespace, quota.account} {
		if usage.Bytes < 0 || usage.Objects < 0 || usage.MaxBytes < 0 || usage.MaxObjects < 0 || usage.Bytes > usage.MaxBytes || usage.Objects > usage.MaxObjects || removingBytes > usage.Bytes || removingObjects > usage.Objects {
			return errPlatformObjectInvalid
		}
		if addingBytes > math.MaxInt64-(usage.Bytes-removingBytes) || addingObjects > math.MaxInt64-(usage.Objects-removingObjects) {
			return errPlatformObjectStorageFull
		}
		bytes := usage.Bytes - removingBytes + addingBytes
		objects := usage.Objects - removingObjects + addingObjects
		if bytes > usage.MaxBytes || objects > usage.MaxObjects {
			return errPlatformObjectStorageFull
		}
	}
	return nil
}
