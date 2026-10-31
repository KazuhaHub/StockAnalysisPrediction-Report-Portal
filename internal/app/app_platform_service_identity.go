package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformServiceIdentityMaxLifetime = 5 * time.Minute

var errAppPlatformServiceUnavailable = errors.New("application service identity unavailable")

type platformServiceFacts struct {
	appID, publisherID, generation, policyRevision, release, epoch, runtimeID string
}

func serviceResourceCapability(resource string) bool {
	switch resource {
	case "reports.read", "storage.files", "network.http", "notifications.publish", "events.subscribe", "tasks.schedule", "files.export":
		return true
	default:
		return false
	}
}

func loadPlatformServiceFacts(e migExec, installation string, resources []string, lock bool) (platformServiceFacts, error) {
	var facts platformServiceFacts
	var generation, policyRevision int64
	var runtime sql.NullString
	lockSQL := ""
	if lock && e.s.driver == "postgres" {
		lockSQL = " FOR UPDATE"
	}
	if err := e.queryRow(`SELECT app_id,publisher_id,generation,policy_revision,controller_epoch,runtime_id FROM app_platform_installations WHERE id=? AND desired_state='enabled' AND phase='ready'`+lockSQL, installation).
		Scan(&facts.appID, &facts.publisherID, &generation, &policyRevision, &facts.epoch, &runtime); err != nil {
		return facts, errAppPlatformServiceUnavailable
	}
	if !platformValidUUID(installation) || !platformDirectoryAppID.MatchString(facts.appID) || !platformDirectoryAppID.MatchString(facts.publisherID) || !platformValidUUID(facts.epoch) || !runtime.Valid || generation < 1 || policyRevision < 1 {
		return facts, errAppPlatformServiceUnavailable
	}
	facts.generation, facts.policyRevision, facts.runtimeID = platformCounterString(generation), platformCounterString(policyRevision), runtime.String
	if !platformValidUUID(facts.runtimeID) {
		return facts, errAppPlatformServiceUnavailable
	}
	var currentEpoch, paused string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')`).Scan(&currentEpoch, &paused); err != nil || currentEpoch != facts.epoch || paused != "0" {
		return facts, errAppPlatformServiceUnavailable
	}
	if err := e.queryRow(`SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND generation=? AND policy_revision=? AND state='active'`, installation, generation, policyRevision).Scan(&facts.release); err != nil || !platformValidSHA(facts.release) {
		return facts, errAppPlatformServiceUnavailable
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, facts.release).Scan(&descriptor); err != nil {
		return facts, errAppPlatformServiceUnavailable
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.ID != facts.appID || parsed.Manifest.Publisher != facts.publisherID || parsed.Manifest.Backend == nil {
		return facts, errAppPlatformServiceUnavailable
	}
	var planRaw, planDigest string
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256 FROM app_platform_capability_grants g JOIN app_platform_review_plans p ON p.id=g.review_plan_id WHERE g.installation_id=? AND g.capability='background.run' AND g.granted=1 AND g.policy_revision=? AND p.consumed_ms IS NOT NULL AND p.action='install'`, installation, policyRevision).
		Scan(&planRaw, &planDigest); err != nil || platformDigest([]byte(planRaw)) != planDigest {
		return facts, errAppPlatformServiceUnavailable
	}
	var plan platformInstallProjection
	if json.Unmarshal([]byte(planRaw), &plan) != nil || !platformManagedFormat(plan.Format) || plan.Runtime == nil || plan.AppID != facts.appID || plan.PublisherID != facts.publisherID || plan.ReleaseSHA256 != facts.release || plan.ControllerEpoch != facts.epoch || plan.Runtime.RuntimeID != facts.runtimeID {
		return facts, errAppPlatformServiceUnavailable
	}
	expected, err := platformInstallCapabilitySelection(parsed.Manifest, plan.Runtime)
	if err != nil {
		return facts, errAppPlatformServiceUnavailable
	}
	expectedGranted := map[string]bool{}
	for _, capability := range expected {
		expectedGranted[capability.capability] = capability.granted
	}
	if !expectedGranted["background.run"] {
		return facts, errAppPlatformServiceUnavailable
	}
	rows, err := e.query(`SELECT capability,granted,policy_revision,review_plan_id FROM app_platform_capability_grants WHERE installation_id=? ORDER BY capability`, installation)
	if err != nil {
		return facts, errAppPlatformServiceUnavailable
	}
	actual := map[string]bool{}
	for rows.Next() {
		var capability string
		var granted int
		var revision int64
		var review string
		if err := rows.Scan(&capability, &granted, &revision, &review); err != nil {
			rows.Close()
			return facts, errAppPlatformServiceUnavailable
		}
		if revision != policyRevision || !platformValidUUID(review) || (granted != 0 && granted != 1) {
			rows.Close()
			return facts, errAppPlatformServiceUnavailable
		}
		actual[capability] = granted == 1
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !actual["background.run"] {
		return facts, errAppPlatformServiceUnavailable
	}
	for _, resource := range resources {
		if !serviceResourceCapability(resource) || !expectedGranted[resource] || !actual[resource] {
			return facts, errAppPlatformServiceUnavailable
		}
	}
	return facts, nil
}

func platformCounterString(value int64) string {
	return strconv.FormatInt(value, 10)
}

func (s *Server) platformServiceSeed(e migExec, installation string) ([]byte, error) {
	var sealed string
	if err := e.queryRow(`SELECT sealed_identity_seed FROM app_platform_installations WHERE id=?`, installation).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, sealPrefix) {
		return nil, errAppPlatformServiceUnavailable
	}
	dek, err := s.readPlatformDEK(e)
	if err != nil {
		return nil, errAppPlatformServiceUnavailable
	}
	defer clear(dek)
	seed, err := aesOpen(dek, strings.TrimPrefix(sealed, sealPrefix), aad("app-platform", installation))
	if err != nil || len(seed) != 32 {
		clear(seed)
		return nil, errAppPlatformServiceUnavailable
	}
	return seed, nil
}

func normalizePlatformServiceResources(resources []string) ([]string, error) {
	if len(resources) < 1 || len(resources) > 16 {
		return nil, errAppPlatformServiceUnavailable
	}
	out := append([]string(nil), resources...)
	sort.Strings(out)
	for i, resource := range out {
		if !serviceResourceCapability(resource) || i > 0 && out[i-1] == resource {
			return nil, errAppPlatformServiceUnavailable
		}
	}
	return out, nil
}

func (s *Server) issuePlatformServiceIdentity(ctx context.Context, installation, leaseID string, resources []string, lifetime time.Duration) ([]byte, appplatform.ServiceIdentityGrant, error) {
	var zero appplatform.ServiceIdentityGrant
	resources, err := normalizePlatformServiceResources(resources)
	if err != nil || !platformValidUUID(installation) || !platformValidUUID(leaseID) {
		return nil, zero, errAppPlatformServiceUnavailable
	}
	if lifetime < 30*time.Second || lifetime > platformServiceIdentityMaxLifetime {
		return nil, zero, errAppPlatformServiceUnavailable
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return nil, zero, err
	}
	facts, err := loadPlatformServiceFacts(e, installation, resources, true)
	if err != nil {
		return nil, zero, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil || now > math.MaxInt64-lifetime.Milliseconds() {
		return nil, zero, errAppPlatformServiceUnavailable
	}
	seed, err := s.platformServiceSeed(e, installation)
	if err != nil {
		return nil, zero, err
	}
	defer clear(seed)
	grant := appplatform.ServiceIdentityGrant{
		Format: appplatform.ServiceIdentityFormat, LeaseID: leaseID, AppID: facts.appID,
		InstallationID: installation, ControllerEpoch: facts.epoch, Generation: facts.generation,
		PolicyRevision: facts.policyRevision, ReleaseSHA256: facts.release, Resources: resources,
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+lifetime.Milliseconds(), 10),
	}
	raw, err := appplatform.SignServiceIdentity(seed, grant, now)
	if err != nil {
		return nil, zero, errAppPlatformServiceUnavailable
	}
	grant.Mac = ""
	if err := tx.Commit(); err != nil {
		return nil, zero, err
	}
	return raw, grant, nil
}

func (s *Server) verifyPlatformServiceIdentity(ctx context.Context, raw []byte, requiredResource string) (appplatform.ServiceIdentityGrant, error) {
	var zero appplatform.ServiceIdentityGrant
	if len(raw) == 0 || len(raw) > 8192 || !serviceResourceCapability(requiredResource) {
		return zero, errAppPlatformServiceUnavailable
	}
	tx, err := s.st.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	var installation string
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return zero, errAppPlatformServiceUnavailable
	}
	if json.Unmarshal(probe["installation_id"], &installation) != nil || !platformValidUUID(installation) {
		return zero, errAppPlatformServiceUnavailable
	}
	current, err := loadPlatformServiceFacts(e, installation, nil, false)
	if err != nil {
		return zero, errAppPlatformServiceUnavailable
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, errAppPlatformServiceUnavailable
	}
	seed, err := s.platformServiceSeed(e, installation)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	expected := appplatform.ServiceIdentityGrant{Format: appplatform.ServiceIdentityFormat, AppID: current.appID, InstallationID: installation, ControllerEpoch: current.epoch, Generation: current.generation, PolicyRevision: current.policyRevision, ReleaseSHA256: current.release}
	grant, err := appplatform.VerifyServiceIdentity(raw, seed, expected, requiredResource, now)
	if err != nil {
		return zero, errAppPlatformServiceUnavailable
	}
	current, err = loadPlatformServiceFacts(e, installation, grant.Resources, false)
	if err != nil {
		return zero, errAppPlatformServiceUnavailable
	}
	if grant.AppID != current.appID || grant.ControllerEpoch != current.epoch || grant.Generation != current.generation || grant.PolicyRevision != current.policyRevision || grant.ReleaseSHA256 != current.release {
		return zero, errAppPlatformServiceUnavailable
	}
	return grant, nil
}
