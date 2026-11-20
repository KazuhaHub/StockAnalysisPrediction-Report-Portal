package broker

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// A background call that carries a credential gets no response that echoes it:
// the result is uncertain and there is no body, in the recognised forms too.
func TestNetworkPoolBackgroundEchoOfTheCredentialIsWithheld(t *testing.T) {
	const token = "bg-echo-secret/value+1"
	value := "Bearer " + token
	for name, echo := range map[string]string{
		"bearer header":   value,
		"token":           token,
		"base64 token":    base64.StdEncoding.EncodeToString([]byte(token)),
		"base64 url":      base64.RawURLEncoding.EncodeToString([]byte("x" + token)),
		"url encoded":     url.QueryEscape(token),
		"json escaped":    `{"k":"bg-echo-secret\/value+1"}`,
		"header form b64": base64.StdEncoding.EncodeToString([]byte(value)),
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(echo))
			})
			raw, sig, a := signedNetworkCredentialFixture(t, h, []byte(`{}`), value)
			pool, err := NewNetworkPool(2, 2, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
			if err != nil {
				t.Fatal(err)
			}
			pool.create = func(HTTPPolicy) (*HTTP, error) { return h, nil }
			got, err := pool.DoBackground(context.Background(), raw, sig, []byte(`{}`), a)
			if name == "json escaped" {
				// The JSON form escapes "/" as \/ , which is not one of the
				// recognised transforms of the value: it is released.
				if err != nil {
					t.Fatalf("an unrecognised form was withheld: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrUncertain) || got.Status != 0 || len(got.Body) != 0 || got.ContentType != "" {
				t.Fatalf("echo released: %+v %v", got, err)
			}
		})
	}
}
