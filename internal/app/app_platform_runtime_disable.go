package app

import (
	"database/sql"
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// Revocation concerns Portal signing authority only. It never claims that a
// runner, container or data writer stopped or that an already signed command was
// recalled. The enrolled runner identity must be reconciled independently.
type platformRuntimeRevocation struct {
	id                        string
	revision, controlRevision int64
}
type platformRuntimeRevocationAudit struct {
	Format          string `json:"format"`
	DeploymentID    string `json:"deployment_id"`
	Epoch           string `json:"controller_epoch"`
	SourceRevision  string `json:"source_revision"`
	Revision        string `json:"revision"`
	ControlRevision string `json:"control_revision"`
}

// revokePlatformRuntimeControl is a denial-only current-administrator repository.
// It atomically clears the purpose-sealed signer, disables its binding and raises
// control/row revisions. It does not open the signing key, connect to a runtime,
// create a replacement identity or change application/operation state. The
// supplied expected revision is a management CAS, not a browser execution grant.
func (s *Server) revokePlatformRuntimeControl(session platformActorSession, id string, expectedRevision int64) (platformRuntimeRevocation, error) {
	var zero, result platformRuntimeRevocation
	if !platformValidUUID(id) || expectedRevision < 1 || expectedRevision == math.MaxInt64 {
		return zero, errAppPlatformRuntimeConflict
	}
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		lock := ""
		if s.st.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var deployment, epoch string
		var revision, controlRevision int64
		var enabled int
		var sealed sql.NullString
		if err := e.queryRow(`SELECT deployment_id,controller_epoch,revision,control_revision,enabled,sealed_control_key FROM app_platform_runtime_bindings WHERE id=?`+lock, id).Scan(&deployment, &epoch, &revision, &controlRevision, &enabled, &sealed); err != nil {
			return platformLeaseReadError(err)
		}
		if !platformValidUUID(deployment) || epoch != control.epoch || revision < 1 || controlRevision < 1 {
			return errAppPlatformRuntimeConflict
		}
		control, err := platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		// An exact lost-response retry must be backed by this API's matching atomic
		// audit. A disabled/restored/manual row alone is not a completed revocation.
		if revision == expectedRevision+1 && enabled == 0 && !sealed.Valid {
			detail := platformRuntimeRevocationDetail(deployment, epoch, expectedRevision, revision, controlRevision)
			var count int
			if err := e.queryRow(`SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_control_revoked' AND target_type='app_platform_runtime' AND target_id=? AND detail=?`, id, detail).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return errAppPlatformRuntimeConflict
			}
		} else {
			if revision != expectedRevision || controlRevision == math.MaxInt64 || (enabled == 0 && !sealed.Valid) {
				return errAppPlatformRuntimeConflict
			}
			changed, err := e.exec(`UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL,control_revision=control_revision+1,revision=revision+1,updated_ms=? WHERE id=? AND revision=? AND control_revision=? AND controller_epoch=?`, control.nowMS, id, expectedRevision, controlRevision, control.epoch)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			revision++
			controlRevision++
			detail := platformRuntimeRevocationDetail(deployment, epoch, expectedRevision, revision, controlRevision)
			if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.runtime_control_revoked','app_platform_runtime',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, id, detail); err != nil {
				return err
			}
		}
		if _, err := platformRefreshActor(e, session, actor, control); err != nil {
			return err
		}
		result = platformRuntimeRevocation{id: id, revision: revision, controlRevision: controlRevision}
		return nil
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}
func platformRuntimeRevocationDetail(deployment, epoch string, source, revision, controlRevision int64) string {
	raw, _ := json.Marshal(platformRuntimeRevocationAudit{Format: "portal.runtime-revocation/1-dev.1", DeploymentID: deployment, Epoch: epoch, SourceRevision: strconv.FormatInt(source, 10), Revision: strconv.FormatInt(revision, 10), ControlRevision: strconv.FormatInt(controlRevision, 10)})
	return string(raw)
}
