package app

import (
	"encoding/json"
	"slices"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformFrontendReadinessFormat = "portal.frontend-readiness/1-dev.1"

type platformFrontendReadiness struct {
	Installation   string `json:"installation_id"`
	ReleaseSHA     string `json:"release_sha256"`
	Generation     string `json:"generation"`
	Policy         string `json:"policy_revision"`
	Format         string `json:"format"`
	Epoch          string `json:"controller_epoch"`
	ReviewSHA      string `json:"review_sha256"`
	DeclaredSchema int    `json:"declared_data_schema"`
	FrontendSHA    string `json:"frontend_sha256"`
	DocumentSHA    string `json:"document_sha256"`
	AudienceSHA    string `json:"audience_sha256"`
}

func loadPlatformFrontendCandidate(e migExec, lease platformOperationLease, now int64) (platformStaticCandidate, error) {
	var zero platformStaticCandidate
	count, err := platformInstallResourceCount(e, lease.installationID)
	if err != nil {
		return zero, err
	}
	if count != 0 {
		return zero, errAppPlatformStageEvidence
	}
	var descriptor, archive []byte
	var review string
	if err = e.queryRow(`SELECT r.descriptor,f.content,p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 JOIN app_platform_frontend_artifacts f ON f.sha256=r.frontend_sha256 WHERE o.id=?`, lease.id).Scan(&descriptor, &archive, &review); err != nil {
		return zero, err
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(review), &p) != nil || p.Format != platformFrontendInstallFormat {
		return zero, errAppPlatformStageEvidence
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	quota, err := platformInstallProfileQuota(p.Format, parsed.Manifest)
	if err != nil || p.DataQuotaBytes != quota {
		return zero, err
	}
	document, err := appplatform.BuildFrontendDocument(parsed.Manifest, archive)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err = validatePlatformInstallAudience(e, p.Audience, now); err != nil {
		return zero, err
	}
	return platformStaticCandidate{frontendOnly: true, reviewSHA: platformDigest([]byte(review)), declaredSchema: parsed.Manifest.Data.Schema, releaseSHA: parsed.SHA256, frontendSHA: document.SourceSHA256, documentSHA: document.DocumentSHA256, audience: p.Audience}, nil
}

// No namespace, effect, quota, writer or runner receipt is claimed by this format.
func platformFrontendEvidence(c platformStaticCandidate, epoch, installation, audienceSHA string) []byte {
	raw, _ := json.Marshal(platformFrontendReadiness{Installation: installation, ReleaseSHA: c.releaseSHA, Generation: "1", Policy: "1", Format: platformFrontendReadinessFormat, Epoch: epoch, ReviewSHA: c.reviewSHA, DeclaredSchema: c.declaredSchema, FrontendSHA: c.frontendSHA, DocumentSHA: c.documentSHA, AudienceSHA: audienceSHA})
	return raw
}

func verifyPlatformFrontendActivation(e migExec, installation, release, epoch string, observed int64, raw string, manifest appplatform.Manifest) error {
	fail := errAppPlatformLaunchUnavailable
	var proof platformFrontendReadiness
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &proof) != nil {
		return fail
	}
	canonical, _ := json.Marshal(proof)
	if string(canonical) != raw || proof.Installation != installation || proof.ReleaseSHA != release || proof.Generation != "1" || proof.Policy != "1" || proof.Format != platformFrontendReadinessFormat || proof.Epoch != epoch || proof.DeclaredSchema != manifest.Data.Schema || proof.FrontendSHA != manifest.Frontend.SHA256 || !platformValidSHA(proof.DocumentSHA) || !platformValidSHA(proof.ReviewSHA) || !platformValidSHA(proof.AudienceSHA) {
		return fail
	}
	if _, err := platformInstallProfileQuota(platformFrontendInstallFormat, manifest); err != nil {
		return fail
	}
	var unsafe, count int
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?)+(SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id WHERE o.installation_id=?)`, installation, installation, installation).Scan(&unsafe); err != nil {
		return err
	}
	if unsafe != 0 {
		return fail
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND action='install' AND managed=0 AND source_generation=0 AND target_generation=1 AND stage='complete' AND completed_ms=? AND controller_epoch=? AND lease_owner IS NULL AND lease_until_ms IS NULL`, installation, observed, epoch).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fail
	}
	var plan, digest, request, planID, target string
	var consumed, completed int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.request_sha256,p.id,p.target_release_sha256,p.consumed_ms,o.completed_ms FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=? AND o.action='install' AND o.managed=0 AND o.source_generation=0 AND o.target_generation=1 AND o.stage='complete' AND o.completed_ms=? AND o.controller_epoch=? AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL`, installation, observed, epoch).Scan(&plan, &digest, &request, &planID, &target, &consumed, &completed); err != nil {
		return fail
	}
	var p platformInstallProjection
	if len(plan) > 65536 || json.Unmarshal([]byte(plan), &p) != nil {
		return fail
	}
	quota, err := platformInstallProfileQuota(platformFrontendInstallFormat, manifest)
	if err != nil {
		return fail
	}
	encoded, _ := json.Marshal(p)
	audience, _ := json.Marshal(p.Audience)
	frame, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	declined := append([]string{}, manifest.Capabilities.Optional...)
	slices.Sort(declined)
	expectedDeclined, _ := json.Marshal(declined)
	actualDeclined, _ := json.Marshal(p.DeclinedCapabilities)
	if string(encoded) != plan || platformDigest([]byte(plan)) != digest || digest != proof.ReviewSHA || platformDigest(frame) != request || target != release || p.ReleaseSHA256 != release || p.Format != platformFrontendInstallFormat || p.DataQuotaBytes != quota || p.ControllerEpoch != epoch || p.PlatformAPI != platformDevelopmentAPI || p.AppID != manifest.ID || p.PublisherID != manifest.Publisher || consumed < 0 || completed < consumed || platformDigest(audience) != proof.AudienceSHA || string(expectedDeclined) != string(actualDeclined) {
		return fail
	}
	var archive []byte
	if err := e.queryRow(`SELECT content FROM app_platform_frontend_artifacts WHERE sha256=?`, manifest.Frontend.SHA256).Scan(&archive); err != nil {
		return err
	}
	document, err := appplatform.BuildFrontendDocument(manifest, archive)
	if err != nil || document.DocumentSHA256 != proof.DocumentSHA || document.SourceSHA256 != proof.FrontendSHA {
		return fail
	}
	return nil
}
