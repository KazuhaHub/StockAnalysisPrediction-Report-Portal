package appplatform

import "errors"

var ErrDataLifecycle = errors.New("application data lifecycle incompatible")

func DataUpgradeCompatible(currentSchema int, target DataCompatibility) bool {
	if currentSchema < minDescriptorDataSchema || currentSchema > maxDescriptorDataSchema || target.Schema < currentSchema || target.Schema < minDescriptorDataSchema || target.Schema > maxDescriptorDataSchema || target.WritesOnStartup || len(target.ReadableSchemas) < 1 || len(target.ReadableSchemas) > maxDescriptorReadableSchemas {
		return false
	}
	seen := map[int]bool{}
	for _, schema := range target.ReadableSchemas {
		if schema < minDescriptorDataSchema || schema > maxDescriptorDataSchema || seen[schema] {
			return false
		}
		seen[schema] = true
	}
	return seen[currentSchema]
}

func DataRollbackCompatible(migratedSchema int, previous DataCompatibility) bool {
	if migratedSchema < minDescriptorDataSchema || migratedSchema > maxDescriptorDataSchema || previous.Schema < minDescriptorDataSchema || previous.Schema > maxDescriptorDataSchema || previous.WritesOnStartup || len(previous.ReadableSchemas) < 1 || len(previous.ReadableSchemas) > maxDescriptorReadableSchemas {
		return false
	}
	seen := map[int]bool{}
	for _, schema := range previous.ReadableSchemas {
		if schema < minDescriptorDataSchema || schema > maxDescriptorDataSchema || seen[schema] {
			return false
		}
		seen[schema] = true
	}
	return seen[migratedSchema]
}

func RequireDataUpgrade(currentSchema int, target DataCompatibility) error {
	if !DataUpgradeCompatible(currentSchema, target) {
		return ErrDataLifecycle
	}
	return nil
}

func RequireDataRollback(migratedSchema int, previous DataCompatibility) error {
	if !DataRollbackCompatible(migratedSchema, previous) {
		return ErrDataLifecycle
	}
	return nil
}

// PlanDataUpgrade selects the declared migration step that upgrades the
// installed schema to the target. No step is returned when the target already
// reads the installed schema; an unreadable schema without a step is refused.
// The returned step never aliases the descriptor.
func PlanDataUpgrade(currentSchema int, target DataCompatibility) (*DataMigration, error) {
	if currentSchema >= minDescriptorDataSchema && currentSchema <= maxDescriptorDataSchema && target.Schema > currentSchema && target.Schema <= maxDescriptorDataSchema && !target.WritesOnStartup {
		for _, step := range target.Migrations {
			if step.From == currentSchema && step.To == target.Schema {
				chosen := step
				chosen.Entrypoint = append([]string(nil), step.Entrypoint...)
				if step.WallClockSeconds != nil {
					seconds := *step.WallClockSeconds
					chosen.WallClockSeconds = &seconds
				}
				return &chosen, nil
			}
		}
	}
	if err := RequireDataUpgrade(currentSchema, target); err != nil {
		return nil, err
	}
	return nil, nil
}
