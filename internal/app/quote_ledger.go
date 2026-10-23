package app

import "sort"

// ledgerChain resolves the operator's already-loaded order without display defaults. Explicit
// grants are necessary even for otherwise unrestricted test sources. Stable partitioning moves
// cash-dividend evidence first while retaining the operator's priority within either class.
func (c *quoteCache) ledgerChain(cfg quoteConfig, target quoteTarget) []quoteSource {
	byName := make(map[string]quoteSource, len(c.sources))
	for _, src := range c.sources {
		byName[src.name] = src
	}
	seen := make(map[string]bool, len(cfg.Order))
	var chain []quoteSource
	for _, name := range cfg.Order {
		if seen[name] {
			continue
		}
		seen[name] = true
		src, ok := byName[name]
		if ok && src.servesExplicit(target, quoteIntervalSettled) {
			chain = append(chain, src)
		}
	}
	sort.SliceStable(chain, func(i, j int) bool {
		return chain[i].cashDividends && !chain[j].cashDividends
	})
	return chain
}

// A chain without both forms of evidence has no primary, even when it contains other sources.
// The caller must pause settlement rather than substitute the first display-capable vendor.
func ledgerPrimary(chain []quoteSource) (quoteSource, bool) {
	for _, src := range chain {
		if src.cashDividends && src.reportsKind {
			return src, true
		}
	}
	return quoteSource{}, false
}
