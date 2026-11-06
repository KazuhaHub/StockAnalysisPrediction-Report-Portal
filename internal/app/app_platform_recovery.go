package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Recovery mode (doc 168, step 9).
//
// A restored Portal is paused: every installation awaits review, no control authority is valid, and
// every mutation refuses with errAppPlatformControlPaused. That left a restore onto a fresh host with
// no way forward, because enrolling the replacement runtime, reviewing a restore import and leasing
// its operation are themselves mutations. Recovery mode is the narrow exception. While the pause is
// set, exactly these are admitted: replacement-runtime enrollment, restore_import reviews and
// operations, and (once they exist) per-installation resumes. Nothing else is, and recovery mode is
// refused when the Portal is not paused, so a normal Portal can never be driven through it.

var errAppPlatformRecoveryUnsettled = errors.New("application recovery has installations or imports still unsettled")

// withPlatformRecoveryTransaction is withPlatformTrustTransaction for a paused Portal. The control it
// hands to fn has recovery set, which platformRefreshActor honours by requiring the pause to remain.
func (s *Server) withPlatformRecoveryTransaction(session platformActorSession, fn func(migExec, platformActorFacts, platformActorControl) error) error {
	return s.withPlatformRecoveryTransactionContext(context.Background(), session, fn)
}

func (s *Server) withPlatformRecoveryTransactionContext(ctx context.Context, session platformActorSession, fn func(migExec, platformActorFacts, platformActorControl) error) error {
	for attempt := 0; ; attempt++ {
		err := s.withAppPlatformActorMode(ctx, session, false, false, true, fn)
		if err == nil || !isContentionErr(err) || attempt == 5 {
			return err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// endPlatformRecovery clears the restore pause. It refuses unless every installation is settled and
// no recovery work is outstanding: a restore import in flight, an import grant still issued, or an
// installation that is neither resumed (ready on an enrolled runtime) nor explicitly disabled.
// Ending recovery does not resume anything: notifications and tasks stay paused until each is
// reviewed through its own flow.
func (s *Server) endPlatformRecovery(session platformActorSession) error {
	return s.withPlatformRecoveryTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var inflight int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE action='restore_import' AND completed_ms IS NULL`).Scan(&inflight); err != nil {
			return err
		}
		var issued int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_import_grants WHERE state='issued'`).Scan(&issued); err != nil {
			return err
		}
		var unsettled int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_installations i WHERE i.desired_state<>'removed'
			AND NOT (i.phase='disabled' OR (i.phase='ready' AND (i.runtime_id IS NULL OR
				EXISTS(SELECT 1 FROM app_platform_runtime_bindings r WHERE r.id=i.runtime_id AND r.enabled=1))))`).Scan(&unsettled); err != nil {
			return err
		}
		if inflight != 0 || issued != 0 || unsettled != 0 {
			return errAppPlatformRecoveryUnsettled
		}
		changed, err := e.exec(`UPDATE meta SET v='0' WHERE k='app_platform_restore_paused' AND v='1'`)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Epoch string `json:"controller_epoch"`
		}{control.epoch})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.recovery_ended','app_platform',?,?,'')`,
			time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, control.epoch, string(detail))
		return err
	})
}

// platformRecoveryPaused reports whether the Portal is in the restore pause. An unreadable marker
// counts as not paused: the ordinary selectors then refuse on it themselves.
func (s *Server) platformRecoveryPaused(ctx context.Context) bool {
	var paused string
	err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`)).Scan(&paused)
	return err == nil && paused == "1"
}
