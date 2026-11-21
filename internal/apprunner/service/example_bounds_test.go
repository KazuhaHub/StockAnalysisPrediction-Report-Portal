package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
)

// LoadConfig accepts network_entries up to 4096, but the broker's pools refuse more than their maximum when the
// runner starts, so a configuration copied from an example with a larger value loads and then never becomes
// ready. The shipped examples (and what host-setup writes) must stay inside what a real start accepts.
func TestShippedExamplesStayInsideTheBrokerPoolBounds(t *testing.T) {
	for _, name := range []string{"service.example.json", "service.proxy-pins.example.json"} {
		raw, err := os.ReadFile("../../../deploy/app-runner/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var cfg Config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.NetworkActive < 1 || cfg.NetworkActive > broker.MaxNetworkPoolActive || cfg.NetworkEntries < 1 || cfg.NetworkEntries > broker.MaxNetworkPoolEntries {
			t.Errorf("%s: network_active %d and network_entries %d must be within %d and %d", name, cfg.NetworkActive, cfg.NetworkEntries, broker.MaxNetworkPoolActive, broker.MaxNetworkPoolEntries)
		}
	}
}
