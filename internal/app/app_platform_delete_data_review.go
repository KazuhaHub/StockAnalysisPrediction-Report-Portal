package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const platformDeleteDataReviewFormat = "portal.delete-data-review/1"

// This projection is intentionally separate from uninstall. A delete-data
// decision names one persisted namespace and cannot remove code, access or a
// plugin table.
type platformDeleteDataReviewProjection struct {
	Format               string                         `json:"format"`
	InstallationID       string                         `json:"installation_id"`
	AppID                string                         `json:"app_id"`
	PublisherID          string                         `json:"publisher_id"`
	Action               appplatform.Action             `json:"action"`
	Managed              bool                           `json:"managed"`
	Generation           int64                          `json:"generation"`
	TargetGeneration     int64                          `json:"target_generation"`
	InstallationRevision int64                          `json:"installation_revision"`
	PolicyRevision       int64                          `json:"policy_revision"`
	TrustRevision        int64                          `json:"trust_revision"`
	ControlFence         int64                          `json:"control_fence"`
	ControllerEpoch      string                         `json:"controller_epoch"`
	ActorAuthority       string                         `json:"actor_authority"`
	DesiredState         string                         `json:"desired_state"`
	Phase                string                         `json:"phase"`
	RuntimeID            string                         `json:"runtime_id"`
	RuntimeRevision      int64                          `json:"runtime_revision"`
	ActivationID         string                         `json:"activation_id"`
	ReleaseSHA256        string                         `json:"release_sha256"`
	RecoverySetsSHA256   string                         `json:"recovery_sets_sha256"`
	DataEffect           string                         `json:"data_effect"`
	Data                 platformDeleteDataReviewTarget `json:"data"`
}

type platformDeleteDataReviewTarget struct {
	ID                   string `json:"id"`
	OriginInstallationID string `json:"origin_installation_id"`
	OwnerInstallationID  string `json:"owner_installation_id"`
	State                string `json:"state"`
	Format               int64  `json:"format"`
	Revision             int64  `json:"revision"`
	WriterFence          int64  `json:"writer_fence"`
	QuotaBytes           int64  `json:"quota_bytes"`
	ObservedBytes        int64  `json:"observed_bytes"`
	SourceOperationID    string `json:"source_operation_id"`
	SourceEffectID       string `json:"source_effect_id"`
	SourceGeneration     int64  `json:"source_generation"`
	SourceFence          int64  `json:"source_fence"`
	SourcePayloadSHA256  string `json:"source_payload_sha256"`
	SourceReceiptSHA256  string `json:"source_receipt_sha256"`
	OwnershipSHA256      string `json:"ownership_sha256"`
	ProjectID            string `json:"project_id"`
	Device               string `json:"device"`
	NamespaceInode       string `json:"namespace_inode"`
	QuotaInodes          string `json:"quota_inodes"`
}

func platformDeleteDataRecoverySetsDigest(e migExec, namespace string) (string, error) {
	rows, err := e.query(`SELECT id,operation_id,state,release_sha256,data_format,writer_fence,checkpoint_id,manifest_sha256,bytes,verified_ms,deleted_ms
		FROM app_platform_recovery_sets WHERE data_namespace_id=? ORDER BY id`, namespace)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var id, operation, state, release, checkpoint string
		var manifest sql.NullString
		var verified, deleted sql.NullInt64
		var format, writer, archive int64
		if err := rows.Scan(&id, &operation, &state, &release, &format,
			&writer, &checkpoint, &manifest, &archive, &verified,
			&deleted); err != nil {
			return "", err
		}
		manifestText := "null"
		if manifest.Valid {
			manifestText = manifest.String
		}
		verifiedText, deletedText := "null", "null"
		if verified.Valid {
			verifiedText = strconv.FormatInt(verified.Int64, 10)
		}
		if deleted.Valid {
			deletedText = strconv.FormatInt(deleted.Int64, 10)
		}
		raw, err := json.Marshal([]string{
			id, operation, state, release,
			strconv.FormatInt(format, 10),
			strconv.FormatInt(writer, 10), checkpoint,
			manifestText, strconv.FormatInt(archive, 10),
			verifiedText, deletedText,
		})
		if err != nil {
			return "", err
		}
		digest.Write(raw)
		digest.Write([]byte{'\n'})
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func platformDeleteDataSourceFacts(
	e migExec, installation, namespace string,
	runtime platformRuntimeRegistration, now int64,
) (platformDeleteDataReviewTarget, error) {
	var result platformDeleteDataReviewTarget
	lastReason := "no applied preparation effect"
	rows, err := e.query(`SELECT o.id,o.target_generation,o.fence,
		f.effect_id,f.payload_sha256,f.receipt_json
		FROM app_platform_operation_effects f
		JOIN app_platform_operations o ON o.id=f.operation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='install' AND o.managed=1
		AND o.stage='complete' AND o.completed_ms IS NOT NULL
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
		AND o.error_code IS NULL AND o.controller_epoch=?
		AND f.stage='prepare_data' AND f.effect_number=1
		AND f.kind='prepare-data' AND f.state='applied'
		AND f.runtime_id=? AND f.controller_epoch=?
		ORDER BY o.completed_ms DESC,f.updated_ms DESC,f.effect_id
		LIMIT 64`, installation, runtime.controllerEpoch, runtime.id,
		runtime.controllerEpoch)
	if err != nil {
		return result, err
	}
	type candidate struct {
		operation, effect, digest, receipt string
		generation, fence                  int64
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.operation, &item.generation,
			&item.fence, &item.effect, &item.digest,
			&item.receipt); err != nil {
			rows.Close()
			return platformDeleteDataReviewTarget{}, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return platformDeleteDataReviewTarget{}, err
	}
	rows.Close()
	for _, item := range candidates {
		if item.generation < 1 || item.fence < 1 ||
			!platformValidUUID(item.operation) ||
			!platformValidUUID(item.effect) ||
			!platformValidSHA(item.digest) {
			return platformDeleteDataReviewTarget{}, errAppPlatformPlanChanged
		}
		receipt, err := journal.ParseDataPreparationReceipt(
			[]byte(item.receipt))
		if err != nil || receipt.Claim.EffectID != item.effect ||
			receipt.Claim.ResourceID != namespace ||
			receipt.InstallationID != installation ||
			receipt.DeploymentID != runtime.deploymentID ||
			receipt.ControllerEpoch != runtime.controllerEpoch ||
			receipt.PayloadSHA256 != item.digest {
			lastReason = "preparation identity"
			continue
		}
		source := platformManagedSource{
			operationID: item.operation, installationID: installation,
			epoch:      runtime.controllerEpoch,
			generation: uint64(item.generation), fence: item.fence,
		}
		command, err := platformDataCommandForSource(
			e, source, runtime, namespace, item.effect)
		if err != nil || command.PayloadSHA256 != item.digest {
			lastReason = "preparation command"
			continue
		}
		command.ControlRevision = receipt.ControlRevision
		if !receipt.MatchesCommand(command) {
			lastReason = "preparation receipt binding"
			continue
		}
		observed, observedErr := strconv.ParseInt(
			receipt.ObservedMS, 10, 64)
		project, projectErr := strconv.ParseUint(
			receipt.ProjectID, 10, 32)
		device, deviceErr := strconv.ParseUint(receipt.Device, 10, 64)
		inode, inodeErr := strconv.ParseUint(
			receipt.NamespaceInode, 10, 64)
		quotaBytes, bytesErr := strconv.ParseInt(
			receipt.QuotaBytes, 10, 64)
		quotaInodes, inodesErr := strconv.ParseInt(
			receipt.QuotaInodes, 10, 64)
		format, formatErr := strconv.ParseInt(receipt.DataFormat, 10, 64)
		if observedErr != nil || observed < 0 || observed > now ||
			projectErr != nil || project == 0 ||
			deviceErr != nil || device == 0 ||
			inodeErr != nil || inode == 0 ||
			bytesErr != nil || quotaBytes <= 0 ||
			quotaBytes%512 != 0 || inodesErr != nil || quotaInodes <= 0 ||
			formatErr != nil || format < 1 {
			lastReason = "preparation native tuple"
			continue
		}
		result = platformDeleteDataReviewTarget{
			ID: namespace, OriginInstallationID: installation,
			SourceOperationID: item.operation, SourceEffectID: item.effect,
			SourceGeneration: item.generation, SourceFence: item.fence,
			SourcePayloadSHA256: item.digest,
			SourceReceiptSHA256: platformDigest([]byte(item.receipt)),
			OwnershipSHA256:     receipt.Claim.OwnershipSHA256,
			ProjectID:           receipt.ProjectID,
			Device:              receipt.Device,
			NamespaceInode:      receipt.NamespaceInode,
			QuotaBytes:          quotaBytes,
			QuotaInodes:         receipt.QuotaInodes,
			Format:              format,
		}
		return result, nil
	}
	return platformDeleteDataReviewTarget{}, fmt.Errorf(
		"delete-data preparation source (%s): %w",
		lastReason, errAppPlatformPlanChanged)
}

func platformLoadDeleteDataReviewProjection(
	e migExec,
	installationID, namespaceID string,
	actor platformActorFacts,
	control platformActorControl,
	runtime platformRuntimeRegistration,
) (platformDeleteDataReviewProjection, error) {
	var p platformDeleteDataReviewProjection
	if !platformValidUUID(installationID) ||
		!platformValidUUID(namespaceID) {
		return p, errAppPlatformPlanChanged
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var runtimeID sql.NullString
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,
		controller_epoch,runtime_id,generation,revision,policy_revision,
		control_fence FROM app_platform_installations WHERE id=?`+lock,
		installationID).Scan(
		&p.AppID, &p.PublisherID, &p.DesiredState, &p.Phase,
		&p.ControllerEpoch, &runtimeID, &p.Generation,
		&p.InstallationRevision, &p.PolicyRevision,
		&p.ControlFence); err != nil {
		return p, errAppPlatformPlanChanged
	}
	if p.ControllerEpoch != control.epoch ||
		!runtimeID.Valid || runtimeID.String != runtime.id ||
		!((p.DesiredState == "disabled" &&
			(p.Phase == "disabled" || p.Phase == "recovery_required" ||
				p.Phase == "review_required")) ||
			(p.DesiredState == "removed" &&
				(p.Phase == "removed" || p.Phase == "recovery_required" ||
					p.Phase == "review_required"))) ||
		p.Generation < 1 || p.InstallationRevision < 1 ||
		p.InstallationRevision == math.MaxInt64 ||
		p.PolicyRevision < 1 || p.ControlFence < 1 {
		return p, fmt.Errorf(
			"delete-data installation tuple: %w", errAppPlatformPlanChanged)
	}
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations
		WHERE installation_id=? AND completed_ms IS NULL`,
		installationID).Scan(&pending); err != nil || pending != 0 {
		if err != nil {
			return p, err
		}
		return p, errAppPlatformOperationConflict
	}
	var active int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		installationID).Scan(&active); err != nil || active != 0 {
		if err != nil {
			return p, err
		}
		return p, errAppPlatformPlanChanged
	}
	var format int64
	if err := e.queryRow(`SELECT id,release_sha256,generation,data_format
		FROM app_platform_activations
		WHERE installation_id=? AND data_namespace_id=? AND state='retired'
		ORDER BY generation DESC LIMIT 1`+lock,
		installationID, namespaceID).Scan(
		&p.ActivationID, &p.ReleaseSHA256, &p.Generation,
		&format); err != nil {
		return p, errAppPlatformPlanChanged
	}
	if !platformValidUUID(p.ActivationID) ||
		!platformValidSHA(p.ReleaseSHA256) || p.Generation < 1 ||
		format < 1 {
		return p, fmt.Errorf(
			"delete-data source activation: %w", errAppPlatformPlanChanged)
	}
	var revision int64
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers
		WHERE id=?`, p.PublisherID).Scan(&revision); err != nil ||
		revision < 1 {
		return p, errAppPlatformPlanChanged
	}
	p.TrustRevision = revision
	var releaseApp, releasePublisher, version, keyID string
	var descriptor, signature []byte
	var managed int
	if err := e.queryRow(`SELECT app_id,publisher_id,version,
		descriptor,signature,verification_key_id,managed
		FROM app_platform_releases WHERE sha256=?`,
		p.ReleaseSHA256).Scan(
		&releaseApp, &releasePublisher, &version, &descriptor,
		&signature, &keyID, &managed); err != nil ||
		releaseApp != p.AppID || releasePublisher != p.PublisherID ||
		version == "" || keyID == "" || len(signature) == 0 ||
		managed != 1 {
		return p, errAppPlatformPlanChanged
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != p.ReleaseSHA256 ||
		parsed.Manifest.ID != p.AppID ||
		parsed.Manifest.Publisher != p.PublisherID ||
		parsed.Manifest.Backend == nil ||
		int64(parsed.Manifest.Data.Schema) != format {
		return p, fmt.Errorf(
			"delete-data stored release: %w", errAppPlatformPlanChanged)
	}
	var origin string
	var owner sql.NullString
	if err := e.queryRow(`SELECT origin_installation_id,
		owner_installation_id,state,data_format,revision,writer_fence,
		quota_bytes,observed_bytes
		FROM app_platform_data_namespaces WHERE id=?`+lock,
		namespaceID).Scan(
		&origin, &owner, &p.Data.State, &p.Data.Format,
		&p.Data.Revision, &p.Data.WriterFence, &p.Data.QuotaBytes,
		&p.Data.ObservedBytes); err != nil {
		return p, errAppPlatformPlanChanged
	}
	if origin != installationID ||
		(owner.Valid && owner.String != installationID) ||
		(p.Data.State == "active" && !owner.Valid) ||
		(p.Data.State != "active" && p.Data.State != "retained" &&
			p.Data.State != "review_required") ||
		p.Data.Format != format || p.Data.Revision < 1 ||
		p.Data.Revision == math.MaxInt64 || p.Data.WriterFence < 1 ||
		p.Data.WriterFence == math.MaxInt64 || p.Data.QuotaBytes <= 0 ||
		p.Data.ObservedBytes < 0 {
		return p, fmt.Errorf(
			"delete-data namespace tuple: %w", errAppPlatformPlanChanged)
	}
	if runtime.revision < 1 ||
		runtime.controllerEpoch != control.epoch {
		return p, errAppPlatformPlanChanged
	}
	source, err := platformDeleteDataSourceFacts(
		e, installationID, namespaceID, runtime, control.nowMS)
	if err != nil {
		return p, err
	}
	if source.Format > p.Data.Format ||
		source.QuotaBytes != p.Data.QuotaBytes ||
		source.ProjectID == "" || source.Device == "" ||
		source.NamespaceInode == "" || source.QuotaInodes == "" ||
		source.OwnershipSHA256 == "" {
		return p, fmt.Errorf(
			"delete-data preparation tuple: %w", errAppPlatformPlanChanged)
	}
	source.OwnerInstallationID = owner.String
	source.State = p.Data.State
	source.Revision = p.Data.Revision
	source.WriterFence = p.Data.WriterFence
	source.ObservedBytes = p.Data.ObservedBytes
	p.Data = source
	p.RecoverySetsSHA256, err = platformDeleteDataRecoverySetsDigest(
		e, namespaceID)
	if err != nil {
		return p, err
	}
	if p.Generation == math.MaxInt64 {
		return p, errAppPlatformPlanChanged
	}
	p.Format = platformDeleteDataReviewFormat
	p.InstallationID = installationID
	p.Action = appplatform.ActionDeleteData
	p.Managed = true
	p.TargetGeneration = p.Generation + 1
	p.RuntimeID = runtime.id
	p.RuntimeRevision = runtime.revision
	p.ActorAuthority = actor.authorityFingerprint
	p.DataEffect = "delete"
	return p, nil
}

func (s *Server) createPlatformDeleteDataReview(
	ctx context.Context,
	session platformActorSession,
	installationID, namespaceID string,
) (platformRemovalReview, error) {
	if !platformValidUUID(installationID) ||
		!platformValidUUID(namespaceID) {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return platformRemovalReview{}, err
	}
	var result platformRemovalReview
	err = s.withPlatformTrustActorContext(ctx, session,
		func(e migExec, actor platformActorFacts,
			control platformActorControl,
			_ appplatform.PublisherTrust,
		) error {
			var paused, epoch string
			if err := e.queryRow(`SELECT
				(SELECT v FROM meta WHERE k='app_platform_restore_paused'),
				(SELECT v FROM meta WHERE k='app_platform_controller_epoch')`).
				Scan(&paused, &epoch); err != nil ||
				paused != "0" || epoch != control.epoch {
				return fmt.Errorf(
					"delete-data review control markers: %w",
					errAppPlatformControlPaused)
			}
			runtime, seed, err := s.platformDataRuntime(
				e, control.epoch, dek)
			clear(seed)
			if err != nil {
				return err
			}
			p, err := platformLoadDeleteDataReviewProjection(
				e, installationID, namespaceID, actor, control, runtime)
			if err != nil {
				return err
			}
			control, err = platformRefreshActor(
				e, session, actor, control)
			if err != nil {
				return fmt.Errorf(
					"delete-data review actor refresh: %w", err)
			}
			raw, err := json.Marshal(p)
			if err != nil || len(raw) > 65536 ||
				control.nowMS > math.MaxInt64-900000 {
				return errAppPlatformPlanChanged
			}
			id, err := platformNewUUID()
			if err != nil {
				return err
			}
			result = platformRemovalReview{
				ID: id, SHA256: platformDigest(raw), JSON: string(raw),
				ExpiresMS: control.nowMS + 900000,
			}
			_, err = e.exec(`INSERT INTO app_platform_review_plans(
				id,installation_id,app_id,publisher_id,action,
				source_release_sha256,target_release_sha256,
				installation_revision,policy_revision,trust_revision,
				controller_epoch,actor_scope,username,account_created,
				account_generation,authority_fingerprint,plan_sha256,
				plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
				VALUES(?,?,?,?,'delete_data',?,NULL,
				?,?,?,?,?,?,?,?,?,?,?,?,
				NULL,?,?)`,
				id, p.InstallationID, p.AppID, p.PublisherID,
				p.ReleaseSHA256, p.InstallationRevision,
				p.PolicyRevision, p.TrustRevision, p.ControllerEpoch,
				actor.scope, actor.username, actor.created,
				actor.generation, actor.authorityFingerprint,
				result.SHA256, result.JSON, result.ExpiresMS,
				control.nowMS, control.nowMS)
			return err
		})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func (s *Server) consumePlatformDeleteDataReview(
	session platformActorSession, planID, planSHA, key string,
) (platformRemovalCommit, error) {
	return s.consumePlatformDeleteDataReviewContext(
		context.Background(), session, planID, planSHA, key)
}

func (s *Server) consumePlatformDeleteDataReviewContext(
	ctx context.Context,
	session platformActorSession,
	planID, planSHA, key string,
) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidUUID(key) ||
		!platformValidSHA(planSHA) {
		return result, errAppPlatformPlanChanged
	}
	request, _ := json.Marshal([]string{
		"portal-operation-commit/v1", planID, planSHA,
	})
	requestDigest, keyDigest := platformDigest(request),
		platformIdempotencySHA(key)
	dek, err := s.dek()
	if err != nil {
		return result, err
	}
	err = s.withAppPlatformActorContext(ctx, session, false,
		func(e migExec, actor platformActorFacts,
			control platformActorControl,
		) error {
			found, exists, err := platformFindRemovalCommit(
				e, actor.scope, keyDigest, requestDigest)
			if err != nil {
				return err
			}
			if exists {
				var action string
				if err := e.queryRow(`SELECT action
					FROM app_platform_operations WHERE id=?`,
					found.OperationID).Scan(&action); err != nil ||
					action != string(appplatform.ActionDeleteData) {
					return errAppPlatformOperationConflict
				}
				result = found
				return nil
			}
			var installation, actionRaw, scope string
			var consumed sql.NullInt64
			var expires int64
			if err := e.queryRow(`SELECT
				COALESCE(installation_id,''),action,actor_scope,
				consumed_ms,expires_ms
				FROM app_platform_review_plans WHERE id=?`,
				planID).Scan(&installation, &actionRaw, &scope,
				&consumed, &expires); err != nil {
				return errAppPlatformPlanChanged
			}
			if actionRaw != string(appplatform.ActionDeleteData) ||
				scope != actor.scope || installation == "" {
				return errAppPlatformPlanChanged
			}
			if consumed.Valid {
				return errAppPlatformOperationConflict
			}
			if expires <= control.nowMS {
				return errAppPlatformPlanExpired
			}
			var namespaceID string
			var raw string
			if err := e.queryRow(`SELECT plan_json
				FROM app_platform_review_plans WHERE id=?`,
				planID).Scan(&raw); err != nil {
				return errAppPlatformPlanChanged
			}
			var preliminary platformDeleteDataReviewProjection
			if err := json.Unmarshal([]byte(raw), &preliminary); err != nil ||
				preliminary.Action != appplatform.ActionDeleteData ||
				preliminary.InstallationID != installation ||
				!platformValidUUID(preliminary.Data.ID) {
				return errAppPlatformPlanChanged
			}
			namespaceID = preliminary.Data.ID
			runtime, seed, err := s.platformDataRuntime(
				e, control.epoch, dek)
			clear(seed)
			if err != nil {
				return err
			}
			p, err := platformLoadDeleteDataReviewProjection(
				e, installation, namespaceID, actor, control, runtime)
			if err != nil {
				return err
			}
			var storedSHA, storedRaw, authority, username sql.NullString
			var accountCreated, appID, publisher, epoch, storedScope string
			var accountGeneration, installRevision, policyRevision,
				trustRevision int64
			var targetRelease sql.NullString
			if err := e.queryRow(`SELECT plan_sha256,plan_json,
				expires_ms,consumed_ms,authority_fingerprint,username,
				account_created,account_generation,installation_revision,
				policy_revision,trust_revision,app_id,publisher_id,
				controller_epoch,source_release_sha256,
				target_release_sha256,actor_scope
				FROM app_platform_review_plans WHERE id=?`+lockForDriver(e),
				planID).Scan(
				&storedSHA, &storedRaw, &expires, &consumed, &authority,
				&username, &accountCreated, &accountGeneration,
				&installRevision, &policyRevision, &trustRevision,
				&appID, &publisher, &epoch, &p.ReleaseSHA256,
				&targetRelease, &storedScope); err != nil {
				return err
			}
			control, err = platformRefreshActor(
				e, session, actor, control)
			if err != nil {
				return fmt.Errorf(
					"delete-data consume actor refresh: %w", err)
			}
			current, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if consumed.Valid || expires <= control.nowMS ||
				!username.Valid || username.String != actor.username ||
				accountCreated != actor.created ||
				accountGeneration != actor.generation ||
				storedScope != actor.scope || !storedSHA.Valid ||
				storedSHA.String != planSHA || !storedRaw.Valid ||
				len(storedRaw.String) > 65536 ||
				platformDigest([]byte(storedRaw.String)) != planSHA ||
				storedRaw.String != string(current) ||
				!authority.Valid ||
				authority.String != actor.authorityFingerprint ||
				installRevision != p.InstallationRevision ||
				policyRevision != p.PolicyRevision ||
				trustRevision != p.TrustRevision || appID != p.AppID ||
				publisher != p.PublisherID || epoch != control.epoch ||
				targetRelease.Valid {
				return errAppPlatformPlanChanged
			}
			id, err := platformNewUUID()
			if err != nil {
				return err
			}
			changed, err := e.exec(`UPDATE app_platform_review_plans
				SET consumed_ms=?,updated_ms=?
				WHERE id=? AND consumed_ms IS NULL AND plan_sha256=?
				AND expires_ms>?`, control.nowMS, control.nowMS,
				planID, planSHA, control.nowMS)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			changed, err = e.exec(`UPDATE app_platform_installations
				SET generation=?,revision=revision+1,
				control_fence=control_fence+1,updated_ms=?
				WHERE id=? AND generation IN (?,?) AND revision=?
				AND policy_revision=? AND control_fence=?
				AND controller_epoch=? AND desired_state=? AND phase=?`,
				p.TargetGeneration, control.nowMS, p.InstallationID,
				p.Generation, p.TargetGeneration,
				p.InstallationRevision, p.PolicyRevision,
				p.ControlFence, p.ControllerEpoch, p.DesiredState,
				p.Phase)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			ownerClause := "owner_installation_id IS NULL"
			args := []any{
				control.nowMS, p.Data.ID, p.Data.OriginInstallationID,
			}
			if p.Data.OwnerInstallationID != "" {
				ownerClause = "owner_installation_id=?"
				args = append(args, p.Data.OwnerInstallationID)
			}
			args = append(args,
				p.Data.State, p.Data.Revision,
				p.Data.WriterFence,
			)
			changed, err = e.exec(`UPDATE app_platform_data_namespaces
				SET state='deleting',owner_installation_id=NULL,
				writer_fence=writer_fence+1,revision=revision+1,
				updated_ms=?
				WHERE id=? AND origin_installation_id=?
				AND `+ownerClause+` AND state=? AND revision=?
				AND writer_fence=?`, args...)
			if err := platformLeaseCAS(changed, err); err != nil {
				return errAppPlatformPlanChanged
			}
			_, err = e.exec(`INSERT INTO app_platform_operations(
				id,installation_id,review_plan_id,actor_scope,
				idempotency_sha256,request_sha256,action,managed,
				source_generation,target_generation,stage,state_revision,
				row_revision,lease_owner,lease_until_ms,fence,
				controller_epoch,error_code,completed_ms,created_ms,
				updated_ms)
				VALUES(?,?,?,?,?,?,'delete_data',1,?,?,'queued',1,1,
				NULL,NULL,0,?,NULL,NULL,?,?)`,
				id, p.InstallationID, planID, actor.scope, keyDigest,
				requestDigest, p.Generation, p.TargetGeneration,
				p.ControllerEpoch, control.nowMS, control.nowMS)
			if err != nil {
				return err
			}
			detail, _ := json.Marshal(struct {
				Plan, Action     string
				NamespaceID      string `json:"namespace_id"`
				TargetGeneration int64  `json:"target_generation"`
			}{planID, string(appplatform.ActionDeleteData),
				p.Data.ID, p.TargetGeneration})
			if _, err := e.exec(`INSERT INTO audit_log(
				at,actor,actor_ou,action,target_type,target_id,detail,ip)
				VALUES(?,?,?,'app_platform.operation_admitted',
				'app_platform_operation',?,?,'')`,
				timeUnixMilliUTC(control.nowMS), actor.username,
				actor.ouID, id, string(detail)); err != nil {
				return err
			}
			result = platformRemovalCommit{
				OperationID: id, InstallationID: p.InstallationID,
				Generation: p.TargetGeneration, Revision: 1,
				Stage: appplatform.StageQueued,
			}
			return nil
		})
	if err != nil {
		return platformRemovalCommit{}, fmt.Errorf(
			"delete-data consume: %w", err)
	}
	return result, nil
}

func lockForDriver(e migExec) string {
	if e.s.driver == "postgres" {
		return " FOR UPDATE"
	}
	return ""
}

// timeUnixMilliUTC keeps SQL audit formatting identical across the lifecycle
// package without duplicating the conversion in every deletion transaction.
func timeUnixMilliUTC(value int64) string {
	return time.UnixMilli(value).UTC().Format(time.RFC3339)
}
