package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// A raise or an import whose native call returned an error has a known outcome: it did not complete. The runner
// records that as a reverted row carrying a typed failure record, never as an unknown one. 'unknown' stays for the
// crash window only (the process died between the claim and the result), which a re-sent signed command finishes.
// A failure record is not a receipt: no later evidence is ever derived from it except the dirty-namespace guard
// below.

const dataEffectFailureFormat = "portal.runner.data-effect-failure/1-dev.1"

// The failure codes a data effect can end with. They are a closed set the runner writes and Portal reads.
const (
	dataFailureArchiveMissing   = "import_archive_missing"
	dataFailureArchiveAmbiguous = "import_archive_ambiguous"
	dataFailureArchiveInvalid   = "import_archive_invalid"
	dataFailureTooLarge         = "import_too_large"
	dataFailureFormatMismatch   = "import_format_mismatch"
	dataFailureUnsafe           = "import_unsafe"
	dataFailureIncomplete       = "import_incomplete"
	dataFailureImport           = "import_failed"
	dataFailureRaiseUnsafe      = "quota_raise_unsafe"
	dataFailureRaiseLower       = "quota_raise_lower"
	// A checkpoint never writes to the data, so each of these leaves the namespace as it was (Dirty is false).
	dataFailureCheckpointUnsafe   = "checkpoint_unsafe"
	dataFailureCheckpointTooLarge = "checkpoint_too_large"
	// An earlier snapshot abandoned unfinished, in favour of a later checkpoint of the installation.
	dataFailureCheckpointIncomplete = "checkpoint_incomplete"
)

var dataFailureCodes = map[string]bool{
	dataFailureArchiveMissing: true, dataFailureArchiveAmbiguous: true, dataFailureArchiveInvalid: true, dataFailureTooLarge: true,
	dataFailureFormatMismatch: true, dataFailureUnsafe: true, dataFailureIncomplete: true, dataFailureImport: true,
	dataFailureRaiseUnsafe: true, dataFailureRaiseLower: true,
	dataFailureCheckpointUnsafe: true, dataFailureCheckpointTooLarge: true, dataFailureCheckpointIncomplete: true,
}

// dataEffectFailure is the receipt_json of a reverted data effect. Dirty is true when the failed native call may have
// changed the namespace: the namespace is then not startable until a later restore has applied.
type dataEffectFailure struct {
	Format      string `json:"format"`
	NamespaceID string `json:"namespace_id"`
	Code        string `json:"code"`
	Dirty       bool   `json:"dirty"`
	SettledMS   string `json:"settled_ms"`
}

func validDataEffectFailure(f dataEffectFailure) bool {
	return f.Format == dataEffectFailureFormat && enrollmentUUID.MatchString(f.NamespaceID) && dataFailureCodes[f.Code] && dataNumber(f.SettledMS, true)
}

func parseDataEffectFailure(raw string) (dataEffectFailure, bool) {
	var f dataEffectFailure
	if !canonicalDataJSON(raw, &f) || !validDataEffectFailure(f) {
		return dataEffectFailure{}, false
	}
	return f, true
}

// DataEffectFailedError is the typed answer for a raise or an import that ended failed. The failure is already
// durable when it is returned. Cause, when present, is the native error.
type DataEffectFailedError struct {
	Header control.CommandHeader
	Code   string
	Cause  error
}

func (e *DataEffectFailedError) Error() string {
	if e.Cause != nil {
		return "runner data command failed (" + e.Code + "): " + e.Cause.Error()
	}
	return "runner data command failed (" + e.Code + ")"
}

func (e *DataEffectFailedError) Unwrap() error { return e.Cause }

// Status is the failed answer to the command, observed now.
func (e *DataEffectFailedError) Status(now int64) MigrateDataStatus {
	status := dataEffectStatus(e.Header, "failed", now)
	status.Code = e.Code
	return status
}

// classifyImportFailure maps a native import error to its code and whether the namespace may have been changed.
// Only errors the native layer returns before it clears anything are clean; an incomplete import and any error not
// recognised are dirty.
func classifyImportFailure(err error) (code string, dirty bool) {
	switch {
	case errors.Is(err, data.ErrImportIncomplete):
		return dataFailureIncomplete, true
	case errors.Is(err, data.ErrImportArchiveMissing):
		return dataFailureArchiveMissing, false
	case errors.Is(err, data.ErrImportArchiveAmbiguous):
		return dataFailureArchiveAmbiguous, false
	case errors.Is(err, data.ErrImportArchiveInvalid):
		return dataFailureArchiveInvalid, false
	case errors.Is(err, data.ErrImportTooLarge):
		return dataFailureTooLarge, false
	case errors.Is(err, data.ErrImportFormatMismatch):
		return dataFailureFormatMismatch, false
	case errors.Is(err, data.ErrImportUnsafe):
		return dataFailureUnsafe, false
	}
	return dataFailureImport, true
}

// classifyRaiseFailure reports the code of a raise the native layer refused before it changed anything. A raise that
// may have applied (incomplete) or failed in any unrecognised way stays unknown: its native call converges when the
// same command is sent again.
func classifyRaiseFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, data.ErrQuotaRaiseIncomplete):
		return "", false
	case errors.Is(err, data.ErrQuotaRaiseUnsafe):
		return dataFailureRaiseUnsafe, true
	case errors.Is(err, data.ErrQuotaLower):
		return dataFailureRaiseLower, true
	}
	return "", false
}

// failDataEffect turns the claimed row into the reverted failure record, if the row is still exactly the claim.
//
// The caller passes a context that outlives the native call's own bounded one: a native call that failed because its
// deadline expired must still be able to record that. same is an optional step in the same transaction.
func (s *Store) failDataEffect(ctx context.Context, h control.CommandHeader, namespace, code string, dirty bool, check func(ctx context.Context, tx *sql.Tx, a control.Authority, now int64) error, same func(ctx context.Context, tx *sql.Tx, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if err := check(ctx, tx, authority, now); err != nil {
		return err
	}
	encoded, err := json.Marshal(dataEffectFailure{Format: dataEffectFailureFormat, NamespaceID: namespace, Code: code, Dirty: dirty, SettledMS: strconv.FormatInt(now, 10)})
	if err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json='{}' AND payload_sha256=?`,
		string(encoded), now, h.EffectID, h.PayloadSHA256)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("fail data effect rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	if same != nil {
		if err := same(ctx, tx, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// failureRecordContext is the context a failure is recorded on: detached from the native call's expired or cancelled
// one, with its own short bound.
func failureRecordContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
}

// restoreNamespaceReservation puts a namespace's two reservation rows back to the size of the given limits, when a raise
// that had enlarged them ended definitively failed. Only rows still at the enlarged size are touched.
func (s *Store) restoreNamespaceReservation(ctx context.Context, tx *sql.Tx, namespace, installation string, oldBytes, oldInodes, newBytes, newInodes, now int64) error {
	oldPlan, ok := s.namespacePlan(namespace, installation, oldBytes, oldInodes)
	if !ok {
		return nil
	}
	newPlan, _ := s.namespacePlan(namespace, installation, newBytes, newInodes)
	if _, err := tx.ExecContext(ctx, `UPDATE runner_reservations SET disk_bytes=?,updated_ms=? WHERE resource_id=? AND state='held' AND disk_bytes=?`,
		oldPlan.diskBytes, now, namespace, newPlan.diskBytes); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE runner_reservations SET disk_bytes=?,updated_ms=? WHERE resource_id=? AND state='held' AND disk_bytes=?`,
		oldPlan.checkpointBytes, now, checkpointReservationID(namespace), newPlan.checkpointBytes)
	return err
}

// failedDataEffectOf answers a reverted row of a data effect with its typed failure, or nil when the row is not one.
func failedDataEffectOf(r *importRow, h control.CommandHeader) *DataEffectFailedError {
	if r == nil || r.state != "reverted" {
		return nil
	}
	failure, ok := parseDataEffectFailure(r.receipt)
	if !ok {
		return nil
	}
	return &DataEffectFailedError{Header: h, Code: failure.Code}
}

// requireNoUnrestoredImport refuses a start while the namespace holds the debris of an import that failed after it
// began to change the namespace. Only a restore of that namespace applied later (in the runner's admission order,
// the commands table's rowid) makes it startable again, so a half-imported namespace is never started by accident.
func requireNoUnrestoredImport(ctx context.Context, tx *sql.Tx, installation, namespace string) error {
	type dirtyImport struct {
		rowid int64
	}
	var dirty []dirtyImport
	rows, err := tx.QueryContext(ctx, `SELECT rowid,receipt_json FROM runner_commands WHERE installation_id=? AND kind='import-data' AND state='reverted' ORDER BY rowid`, installation)
	if err != nil {
		return err
	}
	for rows.Next() {
		var rowid int64
		var raw string
		if err := rows.Scan(&rowid, &raw); err != nil {
			rows.Close()
			return err
		}
		if failure, ok := parseDataEffectFailure(raw); ok && failure.Dirty && failure.NamespaceID == namespace {
			dirty = append(dirty, dirtyImport{rowid})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(dirty) == 0 {
		return nil
	}
	last := dirty[len(dirty)-1].rowid
	restores, err := tx.QueryContext(ctx, `SELECT receipt_json FROM runner_commands WHERE installation_id=? AND kind='restore' AND state='applied' AND rowid>?`, installation, last)
	if err != nil {
		return err
	}
	defer restores.Close()
	for restores.Next() {
		var raw string
		if err := restores.Scan(&raw); err != nil {
			return err
		}
		if restored, err := ParseRestoreReceipt([]byte(raw)); err == nil && restored.NamespaceID == namespace {
			return restores.Err()
		}
	}
	if err := restores.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: namespace has a failed import that no restore has repaired", ErrFenceReconciliation)
}
