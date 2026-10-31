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
