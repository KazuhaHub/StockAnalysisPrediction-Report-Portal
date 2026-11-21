package service

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
)

// LoadConfig used to accept network_active up to 16 and network_entries up to 4096, but the broker's pools refuse
// more than their maxima when the runner starts, so such a configuration loaded and never became ready. It is
// refused at load, at the pools' own bounds.
func TestConfigRefusesPoolSizesTheBrokerWouldRefuse(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cases := map[string]struct {
		mutate func(*Config)
		ok     bool
	}{
		"entries at the maximum":   {func(c *Config) { c.NetworkEntries = broker.MaxNetworkPoolEntries }, true},
		"active at the maximum":    {func(c *Config) { c.NetworkActive = broker.MaxNetworkPoolActive }, true},
		"entries over the maximum": {func(c *Config) { c.NetworkEntries = broker.MaxNetworkPoolEntries + 1 }, false},
		"active over the maximum":  {func(c *Config) { c.NetworkActive = broker.MaxNetworkPoolActive + 1 }, false},
		"the old entries limit":    {func(c *Config) { c.NetworkEntries = 4096 }, false},
		"the old active limit":     {func(c *Config) { c.NetworkActive = 16 }, false},
	}
	for name, c := range cases {
		cfg := configFixture(dir)
		c.mutate(&cfg)
		writePrivate(t, path, cfg)
		_, err := LoadConfig(path)
		if c.ok && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if !c.ok && !errors.Is(err, ErrConfig) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}
