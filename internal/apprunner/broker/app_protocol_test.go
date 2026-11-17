package broker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const validBrokerRequest = `{"requestId":"7d5c4d2e-1111-4222-8333-444455556666","method":"POST","url":"https://api.example.com/v1/check?q=%E4%B8%AD","accept":"application/json","contentType":"application/octet-stream","bodyBase64":"AP+ACg=="}`

func TestBrokerProtocolClosedRequest(t *testing.T) {
	got, err := DecodeAppHTTPRequest([]byte(validBrokerRequest))
	if err != nil || got.RequestID != "7d5c4d2e-1111-4222-8333-444455556666" || got.Method != "POST" || got.URL != "https://api.example.com/v1/check?q=%E4%B8%AD" || got.Accept != "application/json" || got.ContentType != "application/octet-stream" || !bytes.Equal(got.Body, []byte{0, 255, 128, 10}) {
		t.Fatalf("%+v %v", got, err)
	}
	empty := strings.Replace(validBrokerRequest, `"bodyBase64":"AP+ACg=="`, `"bodyBase64":""`, 1)
	if got, err := DecodeAppHTTPRequest([]byte(empty)); err != nil || len(got.Body) != 0 {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte{7}, appplatform.MaxHTTPRequestBytes)
	maxed := strings.Replace(validBrokerRequest, `"AP+ACg=="`, `"`+base64.StdEncoding.EncodeToString(big)+`"`, 1)
	if len(maxed) > MaxAppRequestBytes {
		t.Fatalf("a maximum body no longer fits the 128 KiB cap: %d", len(maxed))
	}
	if got, err := DecodeAppHTTPRequest([]byte(maxed)); err != nil || len(got.Body) != len(big) {
		t.Fatal("maximum body refused", err)
	}
	bad := []string{
		``, `null`, `[]`, `{}`, validBrokerRequest + `{}`, validBrokerRequest + ` x`,
		strings.Replace(validBrokerRequest, `"method":"POST"`, `"method":"POST","method":"GET"`, 1),
		strings.Replace(validBrokerRequest, `"method":"POST"`, `"Method":"POST"`, 1),
		strings.Replace(validBrokerRequest, `"method":"POST"`, `"method":"CONNECT"`, 1),
		strings.Replace(validBrokerRequest, `"method":"POST"`, `"method":null`, 1),
		strings.Replace(validBrokerRequest, `"method":"POST"`, `"method":1`, 1),
		strings.Replace(validBrokerRequest, `"accept":"application/json"`, `"accept":"bad\r\nvalue"`, 1),
		strings.Replace(validBrokerRequest, `"contentType":"application/octet-stream"`, `"contentType":"é"`, 1),
		strings.Replace(validBrokerRequest, `"url":"https://api.example.com/v1/check?q=%E4%B8%AD"`, `"url":""`, 1),
		strings.Replace(validBrokerRequest, `"url":"https://api.example.com/v1/check?q=%E4%B8%AD"`, `"url":"`+strings.Repeat("a", appplatform.MaxHTTPTargetBytes+1)+`"`, 1),
		strings.Replace(validBrokerRequest, `"requestId":"7d5c4d2e-1111-4222-8333-444455556666"`, `"requestId":""`, 1),
		strings.Replace(validBrokerRequest, `"requestId":"7d5c4d2e-1111-4222-8333-444455556666"`, `"requestId":"`+strings.Repeat("a", 65)+`"`, 1),
		strings.Replace(validBrokerRequest, `"requestId":"7d5c4d2e-1111-4222-8333-444455556666"`, `"requestId":"a b"`, 1),
		strings.Replace(validBrokerRequest, `"url":`, `"headers":{"Authorization":"x"},"url":`, 1),
		strings.Replace(validBrokerRequest, `"url":`, `"credential":"c","url":`, 1),
		strings.Replace(validBrokerRequest, `"url":`, `"binding":{},"url":`, 1),
		strings.Replace(validBrokerRequest, `"bodyBase64":"AP+ACg=="`, `"bodyBase64":"AP+ACg"`, 1),
		strings.Replace(validBrokerRequest, `"bodyBase64":"AP+ACg=="`, `"bodyBase64":"AP-_Cg=="`, 1),
		strings.Replace(validBrokerRequest, `"bodyBase64":"AP+ACg=="`, `"bodyBase64":"AP+ACh=="`, 1),
		strings.Replace(validBrokerRequest, `"bodyBase64":"AP+ACg=="`, `"bodyBase64":"`+base64.StdEncoding.EncodeToString(append(bytes.Clone(big), 1))+`"`, 1),
		validBrokerRequest[:20] + "\xff" + validBrokerRequest[21:],
		strings.Repeat(" ", MaxAppRequestBytes) + validBrokerRequest,
	}
	for i, raw := range bad {
		got, err := DecodeAppHTTPRequest([]byte(raw))
		if !errors.Is(err, ErrAppInvalid) || got.Method != "" || len(got.Body) != 0 {
			t.Fatalf("case %d accepted or returned partial input: %v", i, err)
		}
	}
}

func TestBrokerProtocolNoticeIsClosedDoc152Input(t *testing.T) {
	const ok = `{"occurrenceId":"probe-1","title":"Monitor down","message":"api failed","severity":"error"}`
	n, err := DecodeAppNotice([]byte(ok))
	if err != nil || n.OccurrenceID != "probe-1" || n.Title != "Monitor down" || n.Message != "api failed" || n.Severity != "error" {
		t.Fatalf("%+v %v", n, err)
	}
	if string(n.JSON()) != ok {
		t.Fatalf("canonical form changed: %s", n.JSON())
	}
	for i, raw := range []string{
		``, `{}`, `null`, ok + `{}`,
		strings.Replace(ok, `"severity":"error"`, `"severity":"error","extra":"x"`, 1),
		strings.Replace(ok, `"severity":"error"`, `"severity":"error","binding":{}`, 1),
		strings.Replace(ok, `"title":"Monitor down",`, ``, 1),
		strings.Replace(ok, `"title":"Monitor down"`, `"title":"a","title":"b"`, 1),
		strings.Replace(ok, `"message":"api failed"`, `"message":null`, 1),
		strings.Replace(ok, `"message":"api failed"`, `"message":"`+strings.Repeat("x", MaxAppNoticeBytes)+`"`, 1),
	} {
		if _, err := DecodeAppNotice([]byte(raw)); !errors.Is(err, ErrAppInvalid) {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
}

func TestBrokerProtocolErrorBodies(t *testing.T) {
	want := map[string]int{AppErrInvalid: 400, AppErrDenied: 403, AppErrUnavailable: 503, AppErrBusy: 429, AppErrUncertain: 502, AppErrConflict: 409, AppErrCapacity: 429}
	for code, status := range want {
		if AppErrorStatus(code) != status {
			t.Fatalf("%s: %d", code, AppErrorStatus(code))
		}
		if got := string(AppErrorBody(code)); got != fmt.Sprintf(`{"error":%q,"retryable":false}`, code) {
			t.Fatalf("%s: %s", code, got)
		}
		// Retry-After is set for busy only.
		if (AppErrorRetryAfter(code) != "") != (code == AppErrBusy) {
			t.Fatalf("%s: retry-after %q", code, AppErrorRetryAfter(code))
		}
	}
	if AppErrorRetryAfter(AppErrBusy) != "1" {
		t.Fatal("busy must advise one second")
	}
	// An unknown code is never invented; it collapses to the least informative error.
	if AppErrorStatus("secret-diagnostic") != 502 || string(AppErrorBody("secret-diagnostic")) != `{"error":"uncertain","retryable":false}` {
		t.Fatal("unknown error code leaked")
	}
	for err, code := range map[error]string{
		ErrDenied:                          AppErrDenied,
		ErrUnavailable:                     AppErrUnavailable,
		ErrBusy:                            AppErrBusy,
		ErrUncertain:                       AppErrUncertain,
		fmt.Errorf("wrapped: %w", ErrBusy): AppErrBusy,
		fmt.Errorf("w: %w", ErrDenied):     AppErrDenied,
		errors.New("anything else"):        AppErrUncertain,
		nil:                                AppErrUncertain,
	} {
		if got := AppErrorFor(err); got != code {
			t.Fatalf("%v: %s want %s", err, got, code)
		}
	}
}

func TestBrokerProtocolSuccessShape(t *testing.T) {
	var out struct {
		Status            int     `json:"status"`
		ContentType       string  `json:"contentType"`
		BodyBase64        string  `json:"bodyBase64"`
		RetryAfterSeconds *int    `json:"retryAfterSeconds"`
		Extra             *string `json:"extra"`
	}
	raw := AppHTTPSuccessBody(HTTPResponse{Status: 429, ContentType: "application/json", Body: []byte{0, 255}, RetryAfterSeconds: 30})
	if string(raw) != `{"status":429,"contentType":"application/json","bodyBase64":"AP8=","retryAfterSeconds":30}` {
		t.Fatalf("%s", raw)
	}
	raw = AppHTTPSuccessBody(HTTPResponse{Status: 200, Body: nil, RetryAfterSeconds: -1})
	if string(raw) != `{"status":200,"contentType":"","bodyBase64":"","retryAfterSeconds":null}` {
		t.Fatalf("%s", raw)
	}
	if json.Unmarshal(raw, &out) != nil || out.RetryAfterSeconds != nil {
		t.Fatal("retryAfterSeconds must be null when absent")
	}
	if got := string(AppNoticeSuccessBody("01HX", true)); got != `{"eventId":"01HX","created":true}` {
		t.Fatal(got)
	}
	if got := string(AppNoticeSuccessBody("01HX", false)); got != `{"eventId":"01HX","created":false}` {
		t.Fatal(got)
	}
	if got := string(AppStatusBody(true, []string{"network.http", "notifications.publish"})); got != `{"egress":"available","resources":["network.http","notifications.publish"]}` {
		t.Fatal(got)
	}
	if got := string(AppStatusBody(false, nil)); got != `{"egress":"unavailable","resources":[]}` {
		t.Fatal(got)
	}
}
