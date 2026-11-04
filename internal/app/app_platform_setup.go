package app

import "database/sql"

const platformDigestFeedBinding = "digest-feed"

// Setup requirements are deliberately closed. They bind an application's
// reviewed business configuration to existing installation-scoped state, so
// uninstall and credential lifecycle handling stay identical to other bindings.
func platformRequiredSetupBindings(appID string) []string {
	switch appID {
	case "scheduled-digest":
		return []string{platformDigestFeedBinding}
	default:
		return nil
	}
}

func platformMissingSetupBindings(e migExec, installation, appID string, policyRevision int64) ([]string, error) {
	var missing []string
	for _, name := range platformRequiredSetupBindings(appID) {
		var count int
		err := e.queryRow(`SELECT COUNT(*)
			FROM app_platform_secret_bindings
			WHERE installation_id=? AND name=? AND state='active'
			  AND policy_revision=?`, installation, name, policyRevision).Scan(&count)
		if err != nil {
			return nil, err
		}
		if count != 1 {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

func platformReadyPhaseForInstallation(e migExec, installation string, policyRevision int64) (string, error) {
	var appID string
	if err := e.queryRow(`SELECT app_id FROM app_platform_installations
		WHERE id=?`, installation).Scan(&appID); err != nil {
		return "", err
	}
	missing, err := platformMissingSetupBindings(
		e, installation, appID, policyRevision)
	if err != nil {
		return "", err
	}
	if len(missing) > 0 {
		return "setup_required", nil
	}
	return "ready", nil
}

// Recompute the effective runnable phase after a reviewed configuration
// change. Disabled, removed and recovery states keep their stronger meaning.
func platformApplySetupPhase(e migExec, installation string, now int64) error {
	var appID, desired, phase string
	var policyRevision int64
	err := e.queryRow(`SELECT app_id,desired_state,phase,policy_revision
		FROM app_platform_installations WHERE id=?`, installation).Scan(
		&appID, &desired, &phase, &policyRevision)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if desired != "enabled" || (phase != "ready" && phase != "setup_required") ||
		len(platformRequiredSetupBindings(appID)) == 0 {
		return nil
	}
	next := "ready"
	missing, err := platformMissingSetupBindings(
		e, installation, appID, policyRevision)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		next = "setup_required"
	}
	if next == phase {
		return nil
	}
	changed, err := e.exec(`UPDATE app_platform_installations
		SET phase=?,revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase=?`,
		next, now, installation, phase)
	return platformObjectCAS(changed, err)
}
