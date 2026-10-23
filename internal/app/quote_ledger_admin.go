package app

import "sort"

type quoteLedgerHealth struct {
	Source      string `json:"source"`
	LastSuccess string `json:"lastSuccess"`
	LastError   string `json:"lastError"`
	LastErrorAt string `json:"lastErrorAt"`
	Failures    int    `json:"consecutiveFailures"`
}

// Configured declarations describe all markets, not a promise that any particular instrument is
// eligible. Runtime ledgerChain still resolves the actual market/code and applies knows.
func (c *quoteCache) configuredLedgerSources(cfg quoteConfig, iv quoteInterval) []quoteSource {
	c.init()
	byName := map[string]quoteSource{}
	for _, src := range c.sources {
		byName[src.name] = src
	}
	seen := map[string]bool{}
	var sources []quoteSource
	for _, name := range cfg.Order {
		if seen[name] {
			continue
		}
		seen[name] = true
		if src, ok := byName[name]; ok && len(src.marketIDsFor(iv)) > 0 {
			sources = append(sources, src)
		}
	}
	if iv == quoteIntervalSettled {
		sort.SliceStable(sources, func(i, j int) bool { return sources[i].cashDividends && !sources[j].cashDividends })
	}
	return sources
}

func (s *Server) quoteLedgerAdminState(cfg quoteConfig) map[string]any {
	chain := s.quotes.configuredLedgerSources(cfg, quoteIntervalSettled)
	names := make([]string, 0, len(chain))
	for _, src := range chain {
		names = append(names, src.name)
	}
	primary, _ := ledgerPrimary(chain)
	shadow := ""
	for _, src := range s.quotes.configuredLedgerSources(cfg, quoteIntervalSettledShadow) {
		if src.name != primary.name {
			shadow = src.name
			break
		}
	}
	health := []quoteLedgerHealth{}
	for _, h := range s.quotes.snapshotLedgerHealth() {
		health = append(health, quoteLedgerHealth{Source: h.Source, LastSuccess: quoteAdminTime(h.LastSuccess),
			LastError: h.LastError, LastErrorAt: quoteAdminTime(h.LastErrorAt), Failures: h.Failures})
	}
	return map[string]any{"ledgerChain": names, "ledgerPrimary": primary.name, "ledgerShadowPeer": shadow, "ledgerHealth": health}
}
