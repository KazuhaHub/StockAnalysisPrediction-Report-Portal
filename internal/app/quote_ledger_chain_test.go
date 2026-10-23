package app

import (
	"reflect"
	"testing"
)

func TestLedgerChainFollowsOperatorOrder(t *testing.T) {
	grant := func(name string, cash, kind bool, iv quoteInterval) quoteSource {
		return quoteSource{name: name, cashDividends: cash, reportsKind: kind,
			caps: []quoteCapability{{intervals: []quoteInterval{iv}}}}
	}
	sources := []quoteSource{
		grant("plain", false, false, quoteIntervalSettled),
		grant("primary-b", true, true, quoteIntervalSettled),
		grant("cash", true, false, quoteIntervalSettled),
		grant("primary-a", true, true, quoteIntervalSettled),
		grant("shadow", true, true, quoteIntervalSettledShadow),
		grant("kind-only", false, true, quoteIntervalSettled),
		{name: "undeclared", cashDividends: true, reportsKind: true},
	}
	c := &quoteCache{sources: sources}
	for _, tc := range []struct {
		name, primary string
		order, want   []string
	}{
		{"cash first, stable within class", "primary-a", []string{"plain", "cash", "primary-a", "primary-b", "shadow", "undeclared"}, []string{"cash", "primary-a", "primary-b", "plain"}},
		{"reordered primary", "primary-b", []string{"primary-b", "plain", "primary-a"}, []string{"primary-b", "primary-a", "plain"}},
		{"primary disabled", "", []string{"plain", "cash", "shadow", "undeclared"}, []string{"cash", "plain"}},
		{"kind evidence alone is not primary", "", []string{"kind-only"}, []string{"kind-only"}},
		{"empty is disabled, not defaulted", "", nil, nil},
		{"unknown and duplicate names", "primary-a", []string{"missing", "primary-a", "primary-a"}, []string{"primary-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := c.ledgerChain(quoteConfig{Order: tc.order}, quoteAsk(t, "sh"))
			var names []string
			for _, src := range chain {
				names = append(names, src.name)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("chain=%v, want %v", names, tc.want)
			}
			primary, ok := ledgerPrimary(chain)
			if ok != (tc.primary != "") || primary.name != tc.primary {
				t.Fatalf("primary=%s present=%v, want %s", primary.name, ok, tc.primary)
			}
		})
	}
	// Selection must not reorder the caller's registry or mutate grant declarations.
	if c.sources[0].name != "plain" || len(c.sources[0].caps) != 1 {
		t.Fatal("chain selection mutated the source registry")
	}
}

func TestLedgerChainRetainsMarketAndCodeBoundaries(t *testing.T) {
	c := &quoteCache{sources: []quoteSource{{name: "narrow", cashDividends: true, reportsKind: true,
		caps:  []quoteCapability{{markets: func(m *quoteMarket) bool { return m.id == "sh" }, intervals: []quoteInterval{quoteIntervalSettled}}},
		knows: func(target quoteTarget) bool { return target.Code == "601899" },
	}}}
	cfg := quoteConfig{Order: []string{"narrow"}}
	if chain := c.ledgerChain(cfg, quoteAsk(t, "sh")); len(chain) != 1 {
		t.Fatal("positive control lost authorized source")
	}
	otherCode, err := quoteTargetFor("sh", "600519")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []quoteTarget{quoteAsk(t, "sz"), otherCode, {}} {
		if chain := c.ledgerChain(cfg, target); len(chain) != 0 {
			t.Fatalf("unauthorized target acquired source: %+v", target)
		}
	}
}
