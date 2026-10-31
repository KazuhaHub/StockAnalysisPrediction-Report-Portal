package app

import (
	"encoding/json"
	"slices"
	"strconv"

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
func platformFrontendEvidence(c platformStaticCandidate, epoch, installation, audienceSHA string, generation, policy int64) []byte {
	raw, _ := json.Marshal(platformFrontendReadiness{Installation: installation, ReleaseSHA: c.releaseSHA, Generation: strconv.FormatInt(generation, 10), Policy: strconv.FormatInt(policy, 10), Format: platformFrontendReadinessFormat, Epoch: epoch, ReviewSHA: c.reviewSHA, DeclaredSchema: c.declaredSchema, FrontendSHA: c.frontendSHA, DocumentSHA: c.documentSHA, AudienceSHA: audienceSHA})
	return raw
}

func platformInstallationAudienceSHA(e migExec, installation string) (string, error) {
	rows, err := e.query(`SELECT principal_type,COALESCE(username,''),COALESCE(account_created,''),COALESCE(account_generation,0),COALESCE(group_id,0),COALESCE(group_created,'') FROM app_platform_access_grants WHERE installation_id=? ORDER BY principal_type,username,group_id LIMIT ?`, installation, maxPlatformInstallAudience+1)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var audience []platformInstallAudience
	for rows.Next() {
		var kind, username, accountCreated, groupCreated string
		var accountGeneration, groupID int64
		if err := rows.Scan(&kind, &username, &accountCreated, &accountGeneration, &groupID, &groupCreated); err != nil {
			return "", err
		}
		switch kind {
		case "user":
			if username == "" || accountCreated == "" || accountGeneration < 0 || groupID != 0 || groupCreated != "" {
				return "", errAppPlatformLaunchUnavailable
			}
			audience = append(audience, platformInstallAudience{Type: kind, Username: username, AccountCreated: accountCreated, AccountGeneration: strconv.FormatInt(accountGeneration, 10)})
		case "ou":
			if username != "" || accountCreated != "" || accountGeneration != 0 || groupID < 1 || groupCreated == "" {
				return "", errAppPlatformLaunchUnavailable
			}
			audience = append(audience, platformInstallAudience{Type: kind, GroupID: strconv.FormatInt(groupID, 10), GroupCreated: groupCreated})
		default:
			return "", errAppPlatformLaunchUnavailable
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(audience) > maxPlatformInstallAudience {
		return "", errAppPlatformLaunchUnavailable
	}
	raw, _ := json.Marshal(audience)
	return platformDigest(raw), nil
}

func verifyPlatformFrontendActivation(e migExec, installation, release, epoch string, generation, policy, observed int64, raw string, manifest appplatform.Manifest) error {
	fail := errAppPlatformLaunchUnavailable
	var proof platformFrontendReadiness
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &proof) != nil {
		return fail
	}
	generationText, policyText := strconv.FormatInt(generation, 10), strconv.FormatInt(policy, 10)
	canonical, _ := json.Marshal(proof)
	if string(canonical) != raw || generation < 1 || policy < 1 || (generation == 1 && policy != 1) || proof.Installation != installation || proof.ReleaseSHA != release || proof.Generation != generationText || proof.Policy != policyText || proof.Format != platformFrontendReadinessFormat || proof.Epoch != epoch || proof.DeclaredSchema != manifest.Data.Schema || proof.FrontendSHA != manifest.Frontend.SHA256 || !platformValidSHA(proof.DocumentSHA) || !platformValidSHA(proof.ReviewSHA) || !platformValidSHA(proof.AudienceSHA) {
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
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND managed=0 AND stage='complete' AND completed_ms=? AND controller_epoch=? AND lease_owner IS NULL AND lease_until_ms IS NULL AND ((action='install' AND source_generation=0 AND target_generation=?) OR (action='update' AND source_generation=? AND target_generation=?))`, installation, observed, epoch, generation, generation-1, generation).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fail
	}
	var plan, digest, request, planID, action, source, target string
	var consumed, completed int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.request_sha256,p.id,o.action,COALESCE(p.source_release_sha256,''),p.target_release_sha256,p.consumed_ms,o.completed_ms FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=? AND o.managed=0 AND o.stage='complete' AND o.completed_ms=? AND o.controller_epoch=? AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND p.action=o.action AND ((o.action='install' AND o.source_generation=0 AND o.target_generation=?) OR (o.action='update' AND o.source_generation=? AND o.target_generation=?))`, installation, observed, epoch, generation, generation-1, generation).Scan(&plan, &digest, &request, &planID, &action, &source, &target, &consumed, &completed); err != nil {
		return fail
	}
	if len(plan) > 65536 || !platformValidSHA(digest) || digest != proof.ReviewSHA || target != release || consumed < 0 || completed < consumed {
		return fail
	}
	quota, err := platformInstallProfileQuota(platformFrontendInstallFormat, manifest)
	if err != nil {
		return fail
	}
	frame, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	declined := append([]string{}, manifest.Capabilities.Optional...)
	slices.Sort(declined)
	expectedDeclined, _ := json.Marshal(declined)
	if platformDigest([]byte(plan)) != digest || platformDigest(frame) != request {
		return fail
	}
	var encoded []byte
	var actualDeclined []byte
	switch action {
	case "install":
		if generation != 1 || policy != 1 {
			return fail
		}
		var p platformInstallProjection
		if json.Unmarshal([]byte(plan), &p) != nil {
			return fail
		}
		encoded, _ = json.Marshal(p)
		actualDeclined, _ = json.Marshal(p.DeclinedCapabilities)
		if p.Format != platformFrontendInstallFormat || p.ReleaseSHA256 != release || p.DataQuotaBytes != quota || p.ControllerEpoch != epoch || p.PlatformAPI != platformDevelopmentAPI || p.AppID != manifest.ID || p.PublisherID != manifest.Publisher {
			return fail
		}
	case "update":
		if generation < 2 || source == "" {
			return fail
		}
		var p platformUpdateProjection
		if json.Unmarshal([]byte(plan), &p) != nil || p.Format != platformUpdateReviewFormat {
			return fail
		}
		encoded, _ = json.Marshal(p)
		actualDeclined, _ = json.Marshal(p.Target.DeclinedCapabilities)
		if p.InstallationID != installation || p.InstallationRevision < 1 || p.PolicyRevision < 1 || p.TargetPolicyRevision != policy || p.SourceReleaseSHA256 != source || p.SourceGeneration != generation-1 || p.TargetGeneration != generation || p.CurrentDataFormat != int64(manifest.Data.Schema) || p.Target.Format != platformFrontendInstallFormat || p.Target.ReleaseSHA256 != release || p.Target.DataQuotaBytes != quota || p.Target.ControllerEpoch != epoch || p.Target.PlatformAPI != platformDevelopmentAPI || p.AppID != manifest.ID || p.PublisherID != manifest.Publisher || p.Target.AppID != manifest.ID || p.Target.PublisherID != manifest.Publisher || !platformValidSHA(p.GrantsSHA256) {
			return fail
		}
	default:
		return fail
	}
	if string(encoded) != plan || string(actualDeclined) != string(expectedDeclined) {
		return fail
	}
	audienceSHA, err := platformInstallationAudienceSHA(e, installation)
	if err != nil || audienceSHA != proof.AudienceSHA {
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
