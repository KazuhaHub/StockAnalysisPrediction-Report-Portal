package appplatform

import (
	"errors"
	"strings"
	"testing"
)

func migrationDescriptor(t *testing.T, mutate func(data map[string]any)) []byte {
	t.Helper()
	return mutateDescriptor(t, func(d map[string]any) {
		data := d["data"].(map[string]any)
		data["schema"] = 3
		data["readable_schemas"] = []int{2, 3}
		data["migrations"] = []any{map[string]any{"from": 2, "to": 3, "entrypoint": []string{"/bin/migrate", "--apply"}}}
		if mutate != nil {
			mutate(data)
		}
	})
}

func migrationStep(data map[string]any) map[string]any {
	return data["migrations"].([]any)[0].(map[string]any)
}

func TestDescriptorAcceptsSingleHopMigration(t *testing.T) {
	parsed, err := ParseDescriptor(migrationDescriptor(t, func(data map[string]any) {
		migrationStep(data)["wall_clock_seconds"] = 600
	}))
	if err != nil {
		t.Fatal(err)
	}
	steps := parsed.Manifest.Data.Migrations
	if len(steps) != 1 || steps[0].From != 2 || steps[0].To != 3 || len(steps[0].Entrypoint) != 2 || steps[0].WallClockSeconds == nil || *steps[0].WallClockSeconds != 600 {
		t.Fatalf("unexpected migration step: %+v", steps)
	}
	if _, err := ParseDescriptor(migrationDescriptor(t, nil)); err != nil {
		t.Fatal("wall clock must be optional", err)
	}
}

func TestDescriptorRefusesMigrationToNotSchema(t *testing.T) {
	refused(t, migrationDescriptor(t, func(data map[string]any) { migrationStep(data)["to"] = 2; migrationStep(data)["from"] = 1 }))
}

func TestDescriptorRefusesMigrationFromNotBelowTo(t *testing.T) {
	for _, from := range []int{3, 4, 0} {
		refused(t, migrationDescriptor(t, func(data map[string]any) { migrationStep(data)["from"] = from }))
	}
}

func TestDescriptorRefusesDuplicateFrom(t *testing.T) {
	refused(t, migrationDescriptor(t, func(data map[string]any) {
		step := migrationStep(data)
		data["migrations"] = []any{step, map[string]any{"from": 2, "to": 3, "entrypoint": []string{"/bin/other"}}}
	}))
}

func TestDescriptorRefusesStaticAppMigration(t *testing.T) {
	refused(t, mutateDescriptor(t, func(d map[string]any) {
		data := d["data"].(map[string]any)
		data["schema"], data["readable_schemas"] = 3, []int{2, 3}
		data["migrations"] = []any{map[string]any{"from": 2, "to": 3, "entrypoint": []string{"/bin/migrate"}}}
		delete(d, "backend")
		d["capabilities"] = map[string]any{"required": []string{"storage.files"}, "optional": []string{}}
	}))
}

func TestDescriptorRefusesWallClockAbove600(t *testing.T) {
	for _, seconds := range []int{601, 0, -1} {
		refused(t, migrationDescriptor(t, func(data map[string]any) { migrationStep(data)["wall_clock_seconds"] = seconds }))
	}
}

func TestDescriptorRefusesRelativeEntrypoint(t *testing.T) {
	for _, entry := range [][]string{{"bin/migrate"}, {}, {"/bin/migrate", strings.Repeat("a", 257)}, {"/bin/m\x01"}, make([]string, 17)} {
		if len(entry) == 17 {
			for i := range entry {
				entry[i] = "/x"
			}
		}
		refused(t, migrationDescriptor(t, func(data map[string]any) { migrationStep(data)["entrypoint"] = entry }))
	}
}

func TestDescriptorRefusesMoreThanEightMigrations(t *testing.T) {
	refused(t, migrationDescriptor(t, func(data map[string]any) {
		steps := []any{}
		for from := 1; from <= 9; from++ {
			steps = append(steps, map[string]any{"from": from, "to": 10, "entrypoint": []string{"/m"}})
		}
		data["schema"], data["readable_schemas"], data["migrations"] = 10, []int{10}, steps
	}))
}

func refused(t *testing.T, raw []byte) {
	t.Helper()
	if _, err := ParseDescriptor(raw); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestPlanDataUpgradeSelectsStep(t *testing.T) {
	target := DataCompatibility{Schema: 3, ReadableSchemas: []int{3}, Migrations: []DataMigration{
		{From: 1, To: 3, Entrypoint: []string{"/m1"}}, {From: 2, To: 3, Entrypoint: []string{"/m2"}},
	}}
	step, err := PlanDataUpgrade(2, target)
	if err != nil || step == nil || step.From != 2 || step.Entrypoint[0] != "/m2" {
		t.Fatalf("unexpected plan: %+v %v", step, err)
	}
	step.Entrypoint[0] = "/changed"
	if target.Migrations[1].Entrypoint[0] != "/m2" {
		t.Fatal("plan aliases the descriptor")
	}
}

func TestPlanDataUpgradeSameSchemaNoStep(t *testing.T) {
	target := DataCompatibility{Schema: 3, ReadableSchemas: []int{2, 3}, Migrations: []DataMigration{{From: 1, To: 3, Entrypoint: []string{"/m"}}}}
	for _, current := range []int{3, 2} {
		step, err := PlanDataUpgrade(current, target)
		if err != nil || step != nil {
			t.Fatalf("current %d: unexpected step %+v %v", current, step, err)
		}
	}
}

func TestPlanDataUpgradeRefusesUnreadableWithoutStep(t *testing.T) {
	target := DataCompatibility{Schema: 3, ReadableSchemas: []int{3}, Migrations: []DataMigration{{From: 1, To: 3, Entrypoint: []string{"/m"}}}}
	for _, current := range []int{2, 4, 0} {
		if step, err := PlanDataUpgrade(current, target); !errors.Is(err, ErrDataLifecycle) || step != nil {
			t.Fatalf("current %d: expected refusal, got %+v %v", current, step, err)
		}
	}
}
