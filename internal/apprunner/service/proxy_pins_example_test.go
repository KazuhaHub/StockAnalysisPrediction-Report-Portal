package service

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The documented proxy_pins example is the plain example plus exactly the proxy_pins block, so the two cannot
// drift, and its block is a valid allowlist for the name of doc 186. Fails closed: an example the runner would
// refuse is a test failure, not an operator's surprise at deploy time.
func TestProxyPinsExampleConfigIsTheExampleWithTheBlock(t *testing.T) {
	plain, err := os.ReadFile("../../../deploy/app-runner/service.example.json")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile("../../../deploy/app-runner/service.proxy-pins.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := decodeStrict(pinned, &cfg); err != nil {
		t.Fatalf("the proxy_pins example does not decode strictly: %v", err)
	}
	want := []appplatform.ProxyPin{{Host: "quotes.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}}
	if !reflect.DeepEqual(cfg.ProxyPins, want) || appplatform.ValidateProxyOperatorPins(cfg.ProxyPins) != nil {
		t.Fatalf("the example block is %+v", cfg.ProxyPins)
	}
	var a, b map[string]any
	if err := json.Unmarshal(plain, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pinned, &b); err != nil {
		t.Fatal(err)
	}
	if _, ok := a["proxy_pins"]; ok {
		t.Fatal("the plain example must not carry pins: a pin needs the host network namespace")
	}
	delete(b, "proxy_pins")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the proxy_pins example differs from service.example.json in more than the proxy_pins block")
	}
}
