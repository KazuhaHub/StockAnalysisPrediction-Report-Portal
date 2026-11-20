package broker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// This file is the application-facing wire of the background broker,
// `portal.broker/1`: strict decoders for what an application sends and
// canonical encoders for what it receives. It decides no authority. Portal and
// the runner executor re-derive every grant; nothing here is ever sent upstream
// before they have approved it.

// AppProtocol names the application-facing broker protocol.
const AppProtocol = "portal.broker/1"

// MaxAppRequestBytes bounds one request body on the broker socket. It equals the
// foreground gateway's limit: a 64 KiB body is 87,384 base64 bytes, and the rest
// is room for the URL and the two header values, JSON-escaped.
const MaxAppRequestBytes = 128 << 10

// MaxAppNoticeBytes bounds one notification body (doc 152 input).
const MaxAppNoticeBytes = 16384

const maxAppRequestIDBytes = 64

// ErrAppInvalid is any malformed, open-schema or out-of-bounds application input.
var ErrAppInvalid = errors.New("broker application request invalid")

// Application-visible error codes.
const (
	AppErrInvalid     = "invalid"
	AppErrDenied      = "denied"
	AppErrUnavailable = "unavailable"
	AppErrBusy        = "busy"
	AppErrUncertain   = "uncertain"
	AppErrConflict    = "conflict"
	AppErrCapacity    = "capacity"
)

// AppHTTPRequest is a decoded POST /v1/network/http body. RequestID is only for
// the application's own correlation; it is never signed, logged as authority or
// sent upstream. Portal mints the identifier it signs.
type AppHTTPRequest struct {
	RequestID, Method, URL, Accept, ContentType string
	Body                                        []byte
	// Credential is the name of a reviewed credential binding, or "". It is only
	// a name: Portal decides whether the binding may be used and places the value.
	Credential string
	// Signing asks for a signed call (doc 177), mutually exclusive with Credential. It is zero when
	// absent. Like Credential it names a binding only; Portal computes the signature.
	Signing appplatform.SigningRequest
}

// DecodeAppHTTPRequest accepts exactly the six fields requestId, method, url,
// accept, contentType and bodyBase64, and optionally a seventh, credential (a
// binding name), each a JSON string, with no duplicate,
// unknown, differently-cased or null member. Value rules match the foreground
// gateway (shared with it through package appplatform). On error nothing partial
// is returned.
func DecodeAppHTTPRequest(raw []byte) (AppHTTPRequest, error) {
	var zero, out AppHTTPRequest
	fields, err := decodeAppObjectWith(raw, MaxAppRequestBytes, []string{"signing"}, "requestId", "method", "url", "accept", "contentType", "bodyBase64", "credential", "signing")
	_, named := fields["credential"]
	rawSigning, signed := fields["signing"]
	if err != nil || len(fields) != 6+boolToInt(named)+boolToInt(signed) || named && signed {
		return zero, ErrAppInvalid
	}
	if signed {
		out.Signing, err = appplatform.DecodeSigningRequest([]byte(rawSigning))
		if err != nil {
			return zero, ErrAppInvalid
		}
	}
	if named {
		out.Credential = fields["credential"]
		if !appplatform.ValidCredentialBindingName(out.Credential) {
			return zero, ErrAppInvalid
		}
	}
	out.RequestID, out.Method, out.URL, out.Accept, out.ContentType = fields["requestId"], fields["method"], fields["url"], fields["accept"], fields["contentType"]
	if !validAppRequestID(out.RequestID) || !appplatform.ValidHTTPRequestMethod(out.Method) || len(out.URL) == 0 || len(out.URL) > appplatform.MaxHTTPTargetBytes || !appplatform.ValidHTTPPolicyHeaderValue(out.Accept) || !appplatform.ValidHTTPPolicyHeaderValue(out.ContentType) {
		return zero, ErrAppInvalid
	}
	out.Body, err = appplatform.DecodeHTTPRequestBody(fields["bodyBase64"])
	if err != nil {
		return zero, ErrAppInvalid
	}
	return out, nil
}

// MaxAppPostgresRequestBytes bounds one PostgreSQL liveness request body.
const MaxAppPostgresRequestBytes = 1024

// AppPostgresRequest is a decoded POST /v1/postgres/liveness body (doc 178): the reviewed target and
// the binding to use, both only names. RequestID is only for the application's own correlation.
type AppPostgresRequest struct{ RequestID, Target, Credential string }

// DecodeAppPostgresRequest accepts exactly requestId, target and credential, each a JSON string,
// with no duplicate, unknown, differently-cased or null member.
func DecodeAppPostgresRequest(raw []byte) (AppPostgresRequest, error) {
	fields, err := decodeAppObject(raw, MaxAppPostgresRequestBytes, "requestId", "target", "credential")
	if err != nil || len(fields) != 3 {
		return AppPostgresRequest{}, ErrAppInvalid
	}
	out := AppPostgresRequest{RequestID: fields["requestId"], Target: fields["target"], Credential: fields["credential"]}
	if !validAppRequestID(out.RequestID) || !appplatform.ValidPostgresTargetID(out.Target) || !appplatform.ValidCredentialBindingName(out.Credential) {
		return AppPostgresRequest{}, ErrAppInvalid
	}
	return out, nil
}

// AppPostgresErrorFor maps a probe error to an application error code. Unlike AppErrorFor it has no
// uncertain: the check has no external effect, so whatever is not a denial or a spent budget is
// unavailable.
func AppPostgresErrorFor(err error) string {
	switch {
	case errors.Is(err, ErrDenied):
		return AppErrDenied
	case errors.Is(err, ErrBusy):
		return AppErrBusy
	default:
		return AppErrUnavailable
	}
}

// AppPostgresSuccessBody encodes a finished check: its outcome class and its latency.
func AppPostgresSuccessBody(outcome string, latencyMS int64) []byte {
	name, _ := json.Marshal(outcome)
	out := append([]byte(`{"outcome":`), name...)
	out = append(out, `,"latencyMs":`...)
	out = strconv.AppendInt(out, latencyMS, 10)
	return append(out, '}')
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func validAppRequestID(s string) bool {
	if len(s) < 1 || len(s) > maxAppRequestIDBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// AppNotice is the closed doc 152 notification input. Its values are validated
// by Portal's central notification rules, not here; this layer only guarantees
// the shape and size so that exactly these four strings cross to Portal.
type AppNotice struct{ OccurrenceID, Title, Message, Severity string }

// DecodeAppNotice accepts exactly occurrenceId, title, message and severity as
// JSON strings.
func DecodeAppNotice(raw []byte) (AppNotice, error) {
	fields, err := decodeAppObject(raw, MaxAppNoticeBytes, "occurrenceId", "title", "message", "severity")
	if err != nil || len(fields) != 4 {
		return AppNotice{}, ErrAppInvalid
	}
	return AppNotice{OccurrenceID: fields["occurrenceId"], Title: fields["title"], Message: fields["message"], Severity: fields["severity"]}, nil
}

// JSON is the canonical encoding that crosses to Portal in a notice frame. It is
// not HTML-escaped: escaping &, < and > would grow a valid input past the frame.
func (n AppNotice) JSON() []byte {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	_ = e.Encode(struct {
		OccurrenceID string `json:"occurrenceId"`
		Title        string `json:"title"`
		Message      string `json:"message"`
		Severity     string `json:"severity"`
	}(n))
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// decodeAppObject reads one JSON object whose members are all strings and all
// named in allowed, refusing duplicates (exact, case-sensitive), nulls,
// non-string values, trailing data, invalid UTF-8 and anything over limit bytes.
func decodeAppObject(raw []byte, limit int, allowed ...string) (map[string]string, error) {
	return decodeAppObjectWith(raw, limit, nil, allowed...)
}

// decodeAppObjectWith is decodeAppObject where the members named in objects are JSON objects, returned
// as their raw text for the caller to decode strictly.
func decodeAppObjectWith(raw []byte, limit int, objects []string, allowed ...string) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return nil, ErrAppInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, ErrAppInvalid
	}
	out := make(map[string]string, len(allowed))
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrAppInvalid
		}
		known := false
		for _, name := range allowed {
			known = known || key == name
		}
		if _, duplicate := out[key]; duplicate || !known {
			return nil, ErrAppInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrAppInvalid
		}
		var text string
		if len(value) > 0 && value[0] == '{' && slices.Contains(objects, key) {
			out[key] = string(value)
			continue
		}
		if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &text) != nil {
			return nil, ErrAppInvalid
		}
		out[key] = text
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrAppInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrAppInvalid
	}
	return out, nil
}

// AppErrorStatus is the HTTP status of an application-visible error code. An
// unknown code is reported as uncertain, never invented or echoed.
func AppErrorStatus(code string) int {
	switch code {
	case AppErrInvalid:
		return 400
	case AppErrDenied:
		return 403
	case AppErrUnavailable:
		return 503
	case AppErrBusy, AppErrCapacity:
		return 429
	case AppErrConflict:
		return 409
	default:
		return 502
	}
}

// AppErrorRetryAfter is the Retry-After header value of a code, or "" for none.
// Only busy sets it: the budget it reports frees within a call's lifetime. The
// broker never retries; this is advice, and the body stays retryable:false.
func AppErrorRetryAfter(code string) string {
	if code == AppErrBusy {
		return "1"
	}
	return ""
}

// AppErrorBody is the closed error body. retryable is always false: the
// application owns retry policy.
func AppErrorBody(code string) []byte {
	switch code {
	case AppErrInvalid, AppErrDenied, AppErrUnavailable, AppErrBusy, AppErrUncertain, AppErrConflict, AppErrCapacity:
	default:
		code = AppErrUncertain
	}
	return []byte(`{"error":"` + code + `","retryable":false}`)
}

// AppErrorFor maps an executor outcome to an application error code. ErrBusy
// wraps ErrUnavailable, so it is tested first. Any other error, or none where a
// response was required, is reported as uncertain: the caller must not claim an
// effect did not happen when it cannot tell.
func AppErrorFor(err error) string {
	switch {
	case errors.Is(err, ErrDenied):
		return AppErrDenied
	case errors.Is(err, ErrBusy):
		return AppErrBusy
	case errors.Is(err, ErrUnavailable):
		return AppErrUnavailable
	default:
		return AppErrUncertain
	}
}

// AppHTTPSuccessBody encodes a released upstream response. retryAfterSeconds is
// null unless the executor released one (>= 0).
func AppHTTPSuccessBody(r HTTPResponse) []byte {
	contentType, _ := json.Marshal(r.ContentType)
	out := append([]byte(`{"status":`), strconv.Itoa(r.Status)...)
	out = append(out, `,"contentType":`...)
	out = append(out, contentType...)
	out = append(out, `,"bodyBase64":"`...)
	out = base64.StdEncoding.AppendEncode(out, r.Body)
	out = append(out, `","retryAfterSeconds":`...)
	if r.RetryAfterSeconds >= 0 {
		out = strconv.AppendInt(out, int64(r.RetryAfterSeconds), 10)
	} else {
		out = append(out, "null"...)
	}
	return append(out, '}')
}

// AppNoticeSuccessBody encodes the result of a published notification.
func AppNoticeSuccessBody(eventID string, created bool) []byte {
	id, _ := json.Marshal(eventID)
	out := append([]byte(`{"eventId":`), id...)
	out = append(out, `,"created":`...)
	out = strconv.AppendBool(out, created)
	return append(out, '}')
}

// AppStatusBody encodes GET /v1/status. resources lists what the current egress
// session may use; it is empty when egress is unavailable.
func AppStatusBody(available bool, resources []string) []byte {
	state := "unavailable"
	if available {
		state = "available"
	}
	if resources == nil {
		resources = []string{}
	}
	list, _ := json.Marshal(resources)
	return []byte(`{"egress":"` + state + `","resources":` + string(list) + `}`)
}
