package broker

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Every copy of the credential value the pool makes for a call is wiped when the
// call ends: the clones it hands to the authority callback and the signed bytes
// it cloned, on the foreground path and the background one alike.
func TestNetworkPoolWipesItsCopiesOfTheCredential(t *testing.T) {
	const value = "Bearer pool-wipe-secret-value"
	for name, run := range map[string]func(*NetworkPool, context.Context, []byte, []byte, []byte, control.Authority) (HTTPResponse, error){
		"foreground": (*NetworkPool).Do,
		"background": (*NetworkPool).DoBackground,
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("ok"))
			})
			raw, sig, a := signedNetworkCredentialFixture(t, h, []byte(`{}`), value)
			var mu sync.Mutex
			var seen [][]byte
			pool, err := NewNetworkPool(2, 2, func(_ context.Context, c control.NetworkHTTPRequestCommand) (control.Authority, error) {
				mu.Lock()
				seen = append(seen, c.CredentialValue)
				mu.Unlock()
				return a, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			pool.create = func(HTTPPolicy) (*HTTP, error) { return h, nil }
			if _, err := run(pool, context.Background(), raw, sig, []byte(`{}`), a); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 {
				t.Fatal("the authority callback never ran")
			}
			for i, got := range seen {
				if len(got) == 0 {
					t.Fatalf("callback %d saw no credential", i)
				}
				if !bytes.Equal(got, make([]byte, len(got))) {
					t.Fatalf("callback %d copy of the credential survived the call", i)
				}
			}
		})
	}
}
