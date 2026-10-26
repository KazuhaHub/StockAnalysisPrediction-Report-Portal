package app

import (
	"context"
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The profile is confirmed as part of canonical review bytes. No-data means no
// application persistence provider, not a fictitious successful quota/receipt.
func platformInstallProfileQuota(format string, manifest appplatform.Manifest) (int64, error) {
	switch format {
	case platformNativeInstallFormat:
		return 256 << 20, nil
	case platformFrontendInstallFormat:
		if manifest.Backend != nil || len(manifest.Capabilities.Required) != 0 || manifest.Data.WritesOnStartup {
			return 0, errAppPlatformInstallUnavailable
		}
		return 0, nil
	default:
		return 0, errAppPlatformPlanChanged
	}
}
func platformSetInstallProjectionFormat(e migExec, p *platformInstallProjection, format string) error {
	var raw []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, p.ReleaseSHA256).Scan(&raw); err != nil {
		return err
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		return errAppPlatformPlanChanged
	}
	quota, err := platformInstallProfileQuota(format, parsed.Manifest)
	if err != nil {
		return err
	}
	p.Format, p.DataQuotaBytes = format, quota
	return nil
}
func platformParseInstallProfile(raw string) (string, error) {
	var p platformInstallProjection
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &p) != nil || (p.Format != platformNativeInstallFormat && p.Format != platformFrontendInstallFormat) {
		return "", errAppPlatformStageEvidence
	}
	return p.Format, nil
}
func (s *Store) platformInstallProfile(ctx context.Context, operation string) (string, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`), operation).Scan(&raw); err != nil {
		return "", err
	}
	return platformParseInstallProfile(raw)
}

func platformInstallProfileInTransaction(e migExec, operation string) (string, error) {
	var raw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, operation).Scan(&raw); err != nil {
		return "", err
	}
	return platformParseInstallProfile(raw)
}
