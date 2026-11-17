package appplatform

import (
	"errors"
	"testing"
)

func TestDataLifecycleRequiresExplicitUpgradeAndRollbackReadability(t *testing.T) {
	previous := DataCompatibility{Schema: 1, ReadableSchemas: []int{1}}
	target := DataCompatibility{Schema: 2, ReadableSchemas: []int{1, 2}}
	if err := RequireDataUpgrade(1, target); err != nil {
		t.Fatal("explicit migration target refused", err)
	}
	if err := RequireDataRollback(2, previous); !errors.Is(err, ErrDataLifecycle) {
		t.Fatal("old release regained unreadable migrated data")
	}
	if err := RequireDataRollback(1, previous); err != nil {
		t.Fatal("compatible pre-migration rollback refused", err)
	}
	if err := RequireDataUpgrade(2, target); err != nil {
		t.Fatal("same-schema upgrade refused", err)
	}
	if err := RequireDataUpgrade(3, target); !errors.Is(err, ErrDataLifecycle) {
		t.Fatal("unknown newer data schema accepted")
	}
}

func TestDataLifecycleRefusesUnsafeOrMalformedDeclarations(t *testing.T) {
	for _, data := range []DataCompatibility{
		{Schema: 2, ReadableSchemas: nil},
		{Schema: 2, ReadableSchemas: []int{2}},
		{Schema: 2, ReadableSchemas: []int{1, 1}},
		{Schema: 2, ReadableSchemas: []int{0, 1}},
		{Schema: 2, ReadableSchemas: []int{1}, WritesOnStartup: true},
	} {
		if DataUpgradeCompatible(1, data) {
			t.Fatal("unsafe data declaration accepted", data)
		}
	}
	for _, schema := range []int{0, maxDescriptorDataSchema + 1} {
		if DataUpgradeCompatible(schema, DataCompatibility{Schema: maxDescriptorDataSchema, ReadableSchemas: []int{schema}}) {
			t.Fatal("invalid current schema accepted", schema)
		}
	}
}
