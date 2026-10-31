package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var (
	errAppPlatformPlanChanged       = errors.New("application review plan changed")
	errAppPlatformPlanExpired       = errors.New("application review plan expired")
	errAppPlatformOperationConflict = errors.New("application operation conflicts with existing work")
)

// Internal denial-only review. No package, new capability, network destination,
// raw secret, data deletion or runtime command can be supplied through this type.
type platformRemovalProjection struct {
	Format               string               `json:"format"`
	InstallationID       string               `json:"installation_id"`
	AppID                string               `json:"app_id"`
	PublisherID          string               `json:"publisher_id"`
	Action               appplatform.Action   `json:"action"`
	Managed              bool                 `json:"managed"`
	Generation           int64                `json:"generation"`
	TargetGeneration     int64                `json:"target_generation"`
	InstallationRevision int64                `json:"installation_revision"`
	PolicyRevision       int64                `json:"policy_revision"`
	TrustRevision        int64                `json:"trust_revision"`
	ControllerEpoch      string               `json:"controller_epoch"`
	ActorAuthority       string               `json:"actor_authority"`
	DesiredState         string               `json:"desired_state"`
	Phase                string               `json:"phase"`
	RuntimeID            string               `json:"runtime_id"`
	RuntimeRevision      int64                `json:"runtime_revision"`
	SourceReleaseSHA256  string               `json:"source_release_sha256"`
	GrantsSHA256         string               `json:"grants_sha256"`
	DataEffect           string               `json:"data_effect"`
	Data                 *platformRemovalData `json:"data"`
}

type platformRemovalData struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Format      int64  `json:"format"`
	Revision    int64  `json:"revision"`
	WriterFence int64  `json:"writer_fence"`
}

type platformRemovalReview struct {
	ID, SHA256, JSON string
	ExpiresMS        int64
}
type platformRemovalCommit struct {
	OperationID, InstallationID string
	Generation, Revision        int64
	Stage                       appplatform.Stage
	Replayed                    bool
}

func platformDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// Every admission path shares this identity; actions cannot create separate
// idempotency namespaces for the same account lifecycle and client key.
func platformIdempotencySHA(key string) string {
	return platformDigest([]byte("portal-app-idempotency/v1:" + key))
}

func platformValidSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func platformRemovalGrantsDigest(e migExec, installation string) (string, error) {
	h := sha256.New()
	for _, q := range []string{
		`SELECT capability,granted,policy_json,policy_revision,review_plan_id FROM app_platform_capability_grants WHERE installation_id=? ORDER BY capability`,
		`SELECT id,principal_type,username,account_created,account_generation,group_id,group_created,revision FROM app_platform_access_grants WHERE installation_id=? ORDER BY id`,
		`SELECT id,name,credential_id,credential_value_revision,operation,state,policy_json,policy_revision,review_plan_id,revision,revoked_ms FROM app_platform_secret_bindings WHERE installation_id=? ORDER BY id`,
	} {
		h.Write([]byte(q))
		rows, err := e.query(q, installation)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for index := range values {
				targets[index] = &values[index]
			}
			if err := rows.Scan(targets...); err != nil {
				rows.Close()
				return "", err
			}
			// Normalize driver TEXT representations before hashing. BYTEA/BLOB
			// and arbitrary provider data are not part of these projections.
			for index, value := range values {
				if text, ok := value.([]byte); ok {
					values[index] = string(text)
				}
			}
			raw, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				return "", err
			}
			h.Write(raw)
			h.Write([]byte{'\n'})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func platformLoadRemovalProjection(e migExec, id string, action appplatform.Action, actor platformActorFacts, control platformActorControl) (platformRemovalProjection, error) {
	var p platformRemovalProjection
	if !platformValidUUID(id) || (action != appplatform.ActionDisable && action != appplatform.ActionUninstall) {
		return p, errAppPlatformPlanChanged
	}
	var runtime sql.NullString
	if err := e.queryRow(`SELECT runtime_id FROM app_platform_installations WHERE id=?`, id).Scan(&runtime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, errAppPlatformPlanChanged
		}
		return p, err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	if runtime.Valid {
		if err := e.queryRow(`SELECT revision FROM app_platform_runtime_bindings WHERE id=?`+lock, runtime.String).Scan(&p.RuntimeRevision); err != nil {
			return p, err
		}
		p.RuntimeID = runtime.String
	}
	var currentRuntime sql.NullString
	var fence int64
	if err := e.queryRow(`SELECT app_id,publisher_id,controller_epoch,generation,revision,policy_revision,desired_state,phase,runtime_id,control_fence
        FROM app_platform_installations WHERE id=?`+lock, id).Scan(&p.AppID, &p.PublisherID, &p.ControllerEpoch, &p.Generation, &p.InstallationRevision, &p.PolicyRevision, &p.DesiredState, &p.Phase, &currentRuntime, &fence); err != nil {
		return p, err
	}
	if currentRuntime != runtime || p.ControllerEpoch != control.epoch || p.DesiredState == "removed" || p.Generation == math.MaxInt64 || p.InstallationRevision == math.MaxInt64 || fence == math.MaxInt64 {
		return p, errAppPlatformPlanChanged
	}
	switch p.Phase {
	case "pending", "setup_required", "ready", "disabled":
	default:
		return p, errAppPlatformPlanChanged
	}
	if action == appplatform.ActionDisable && p.DesiredState == "disabled" {
		return p, errAppPlatformPlanChanged
	}
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, id).Scan(&pending); err != nil {
		return p, err
	}
	if pending != 0 {
		return p, errAppPlatformOperationConflict
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, p.PublisherID).Scan(&p.TrustRevision); err != nil {
		return p, err
	}
	var source sql.NullString
	err := e.queryRow(`SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, id).Scan(&source)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.SourceReleaseSHA256 = source.String
	var data platformRemovalData
	err = e.queryRow(`SELECT id,state,data_format,revision,writer_fence FROM app_platform_data_namespaces WHERE owner_installation_id=?`, id).Scan(&data.ID, &data.State, &data.Format, &data.Revision, &data.WriterFence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if err == nil {
		if data.State != "active" || data.Revision == math.MaxInt64 || data.WriterFence == math.MaxInt64 {
			return p, errAppPlatformPlanChanged
		}
		p.Data = &data
	}
	p.GrantsSHA256, err = platformRemovalGrantsDigest(e, id)
	if err != nil {
		return p, err
	}
	p.Format, p.InstallationID, p.Action, p.Managed, p.TargetGeneration, p.ActorAuthority = "portal.removal-review/1", id, action, runtime.Valid, p.Generation+1, actor.authorityFingerprint
	p.DataEffect = "preserve"
	if action == appplatform.ActionUninstall {
		p.DataEffect = "delete"
	}
	return p, nil
}

func (s *Server) createPlatformRemovalReview(session platformActorSession, installation string, action appplatform.Action) (platformRemovalReview, error) {
	return s.createPlatformRemovalReviewContext(context.Background(), session, installation, action)
}

func (s *Server) createPlatformRemovalReviewContext(ctx context.Context, session platformActorSession, installation string, action appplatform.Action) (platformRemovalReview, error) {
	return s.createPlatformRemovalReviewWithScopeContext(ctx, session, installation, action, false)
}

func (s *Server) createPlatformFrontendRemovalReviewContext(ctx context.Context, session platformActorSession, installation string, action appplatform.Action) (platformRemovalReview, error) {
	return s.createPlatformRemovalReviewWithScopeContext(ctx, session, installation, action, true)
}

func (s *Server) createPlatformRemovalReviewWithScopeContext(ctx context.Context, session platformActorSession, installation string, action appplatform.Action, frontendOnly bool) (platformRemovalReview, error) {
	var result platformRemovalReview
	err := s.withAppPlatformActorContext(ctx, session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		p, err := platformLoadRemovalProjection(e, installation, action, actor, control)
		if err != nil {
			return err
		}
		if frontendOnly && (p.Managed || p.Data != nil) {
			return errAppPlatformInstallUnavailable
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if len(raw) > 65536 || control.nowMS > math.MaxInt64-900000 {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: platformDigest(raw), JSON: string(raw), ExpiresMS: control.nowMS + 900000}
		var source any
		if p.SourceReleaseSHA256 != "" {
			source = p.SourceReleaseSHA256
		}
		_, err = e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,
            installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,
            plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, p.AppID, p.PublisherID, string(action), source, p.InstallationRevision, p.PolicyRevision, p.TrustRevision, p.ControllerEpoch,
			actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, result.SHA256, result.JSON, result.ExpiresMS, control.nowMS, control.nowMS)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func platformFindRemovalCommit(e migExec, actorScope, keyDigest, requestDigest string) (platformRemovalCommit, bool, error) {
	var result platformRemovalCommit
	var stored string
	var action appplatform.Action
	var managed int
	err := e.queryRow(`SELECT id,installation_id,target_generation,stage,state_revision,request_sha256,action,managed FROM app_platform_operations
        WHERE actor_scope=? AND idempotency_sha256=?`, actorScope, keyDigest).Scan(&result.OperationID, &result.InstallationID, &result.Generation, &result.Stage, &result.Revision, &stored, &action, &managed)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if stored != requestDigest {
		return platformRemovalCommit{}, false, errAppPlatformOperationConflict
	}
	if result.Generation < 1 || result.Revision < 1 || (managed != 0 && managed != 1) {
		return platformRemovalCommit{}, false, errAppPlatformOperationConflict
	}
	if _, err := appplatform.OperationFromSnapshot(appplatform.OperationSnapshot{Action: action, Managed: managed == 1, Generation: uint64(result.Generation), Revision: uint64(result.Revision), Stage: result.Stage}); err != nil {
		return platformRemovalCommit{}, false, errAppPlatformOperationConflict
	}
	result.Replayed = true
	return result, true, nil
}

func (s *Server) consumePlatformRemovalReview(session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	return s.consumePlatformRemovalReviewContext(context.Background(), session, planID, planSHA, key)
}

func (s *Server) consumePlatformRemovalReviewContext(ctx context.Context, session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidUUID(key) || !platformValidSHA(planSHA) {
		return result, errAppPlatformPlanChanged
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err := s.withAppPlatformActorContext(ctx, session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		found, exists, err := platformFindRemovalCommit(e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			result = found
			return nil
		}
		var installation sql.NullString
		var scope string
		var preliminaryConsumed sql.NullInt64
		var preliminaryExpiry int64
		if err := e.queryRow(`SELECT installation_id,actor_scope,consumed_ms,expires_ms FROM app_platform_review_plans WHERE id=?`, planID).Scan(&installation, &scope, &preliminaryConsumed, &preliminaryExpiry); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAppPlatformPlanChanged
			}
			return err
		}
		if scope != actor.scope {
			return errAppPlatformActorUnavailable
		}
		if preliminaryConsumed.Valid {
			return errAppPlatformOperationConflict
		}
		if preliminaryExpiry <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if !installation.Valid {
			return errAppPlatformPlanChanged
		}
		// Lock order remains principal -> runtime -> installation -> plan.
		var action appplatform.Action
		if err := e.queryRow(`SELECT action FROM app_platform_review_plans WHERE id=?`, planID).Scan(&action); err != nil {
			return err
		}
		p, err := platformLoadRemovalProjection(e, installation.String, action, actor, control)
		if err != nil {
			return err
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var storedSHA, raw, authority, created, appID, publisher, epoch string
		var username sql.NullString
		var storedScope string
		var expires, generation, installRevision, policyRevision, trustRevision int64
		var consumed sql.NullInt64
		var source, target sql.NullString
		if err := e.queryRow(`SELECT plan_sha256,plan_json,expires_ms,consumed_ms,authority_fingerprint,username,account_created,account_generation,
            installation_revision,policy_revision,trust_revision,app_id,publisher_id,controller_epoch,source_release_sha256,target_release_sha256,actor_scope
            FROM app_platform_review_plans WHERE id=?`+lock, planID).Scan(&storedSHA, &raw, &expires, &consumed, &authority, &username, &created, &generation,
			&installRevision, &policyRevision, &trustRevision, &appID, &publisher, &epoch, &source, &target, &storedScope); err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if !username.Valid || username.String != actor.username || created != actor.created || generation != actor.generation || storedScope != actor.scope {
			return errAppPlatformActorUnavailable
		}
		current, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if storedSHA != planSHA || len(raw) > 65536 || platformDigest([]byte(raw)) != planSHA || raw != string(current) || authority != actor.authorityFingerprint ||
			installRevision != p.InstallationRevision || policyRevision != p.PolicyRevision || trustRevision != p.TrustRevision || appID != p.AppID || publisher != p.PublisherID || epoch != control.epoch || source.String != p.SourceReleaseSHA256 || target.Valid {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		desired, phase := "disabled", "disabled"
		if action == appplatform.ActionUninstall {
			desired, phase = "removed", "pending"
		}
		updated, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND plan_sha256=? AND expires_ms>?`, control.nowMS, control.nowMS, planID, planSHA, control.nowMS)
		if err := platformLeaseCAS(updated, err); err != nil {
			return err
		}
		updated, err = e.exec(`UPDATE app_platform_installations SET desired_state=?,phase=?,generation=?,revision=revision+1,control_fence=control_fence+1,updated_ms=?
            WHERE id=? AND revision=? AND generation=? AND controller_epoch=?`, desired, phase, p.TargetGeneration, control.nowMS, p.InstallationID, p.InstallationRevision, p.Generation, control.epoch)
		if err := platformLeaseCAS(updated, err); err != nil {
			return err
		}

		revokedBindings := 0
		if action == appplatform.ActionUninstall {
			revokedBindings, err = revokePlatformUninstallSecretBindings(e, p.InstallationID, control.nowMS)
			if err != nil {
				return err
			}
		}
		if _, err := e.exec(`UPDATE app_platform_activations SET state='retired',updated_ms=? WHERE installation_id=? AND state='active'`, control.nowMS, p.InstallationID); err != nil {
			return err
		}
		if action == appplatform.ActionUninstall {
			if err := cleanupPlatformInstallationObjects(e, p.InstallationID, p.AppID, control.nowMS); err != nil {
				return err
			}
		}
		if p.Data != nil {
			updated, err := e.exec(`UPDATE app_platform_data_namespaces SET writer_fence=writer_fence+1,revision=revision+1,updated_ms=? WHERE id=? AND owner_installation_id=? AND revision=? AND writer_fence=?`, control.nowMS, p.Data.ID, p.InstallationID, p.Data.Revision, p.Data.WriterFence)
			if err := platformLeaseCAS(updated, err); err != nil {
				return err
			}
		}
		_, err = e.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,
            source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
            VALUES(?,?,?,?,?,?,?,?,?,?,'queued',1,1,NULL,NULL,0,?,NULL,NULL,?,?)`, id, p.InstallationID, planID, actor.scope, keyDigest, requestDigest, string(action), boolInt(p.Managed), p.Generation, p.TargetGeneration, control.epoch, control.nowMS, control.nowMS)
		if err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan, Action          string
			Generation            int64
			RevokedSecretBindings int `json:"revoked_secret_bindings,omitempty"`
		}{planID, string(action), p.TargetGeneration, revokedBindings})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.operation_admitted','app_platform_operation',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, id, string(detail)); err != nil {
			return err
		}
		result = platformRemovalCommit{OperationID: id, InstallationID: p.InstallationID, Generation: p.TargetGeneration, Revision: 1, Stage: appplatform.StageQueued}
		return nil
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}
