package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const writableRegistrationClaimFormat = "portal.runner.writable-backend-registration-claim/1-dev.1"
const writableRegistrationReceiptFormat = "portal.runner.writable-backend-registration-delivery/1-dev.1"
const writableRegistrationRecoveredFormat = "portal.runner.reconciled-writable-backend-registration-delivery/1-dev.1"

type writableRegistrationClaim struct {
	Format          string `json:"format"`
	ClaimID         string `json:"claim_id"`
	EffectID        string `json:"effect_id"`
	PayloadSHA256   string `json:"payload_sha256"`
	SourceSHA256    string `json:"source_sha256"`
	NativeID        string `json:"native_id"`
	DocumentSHA256  string `json:"document_sha256"`
	ControlRevision string `json:"control_revision"`
	ClaimedMS       string `json:"claimed_ms"`
	ExpiresMS       string `json:"expires_ms"`
}

func validWritableRegistrationClaim(m writableRegistrationClaim) bool {
	if m.Format != writableRegistrationClaimFormat || !enrollmentUUID.MatchString(m.ClaimID) || m.ClaimID == "00000000-0000-0000-0000-000000000000" || !enrollmentUUID.MatchString(m.EffectID) || m.EffectID == "00000000-0000-0000-0000-000000000000" || !dataNumber(m.ControlRevision, true) || !dataNumber(m.ClaimedMS, false) || !dataNumber(m.ExpiresMS, true) {
		return false
	}
	for _, h := range []string{m.PayloadSHA256, m.SourceSHA256, m.NativeID, m.DocumentSHA256} {
		if !dataSHA(h) {
			return false
		}
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	return expires > claimed && expires-claimed <= 30000
}
func writableRegistrationBinding(m writableRegistrationClaim) string {
	raw, _ := json.Marshal(m)
	return candidateHash(raw)
}

// Historical protected-file evidence, not handshake, backend access or activation.
type WritableBackendRegistrationReceipt struct {
	Format                  string                    `json:"format"`
	Claim                   writableRegistrationClaim `json:"claim"`
	BindingSHA256           string                    `json:"binding_sha256"`
	Device                  string                    `json:"device"`
	Inode                   string                    `json:"inode"`
	ObservedMS              string                    `json:"observed_ms"`
	SettledMS               string                    `json:"settled_ms"`
	RecoveryEnvelopeSHA256  string                    `json:"recovery_envelope_sha256,omitempty"`
	RecoveryControlRevision string                    `json:"recovery_control_revision,omitempty"`
	RecoveryIssuedMS        string                    `json:"recovery_issued_ms,omitempty"`
	RecoveryExpiresMS       string                    `json:"recovery_expires_ms,omitempty"`
}

func ParseWritableBackendRegistrationReceipt(raw []byte) (WritableBackendRegistrationReceipt, error) {
	var r WritableBackendRegistrationReceipt
	if len(raw) == 0 || len(raw) > 8192 || !canonicalDataJSON(string(raw), &r) || !validWritableRegistrationClaim(r.Claim) || r.BindingSHA256 != writableRegistrationBinding(r.Claim) || !nativeNumber(r.Device, false) || !nativeNumber(r.Inode, true) || !dataNumber(r.ObservedMS, false) || !dataNumber(r.SettledMS, false) {
		return WritableBackendRegistrationReceipt{}, ErrFenceReconciliation
	}
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(r.Claim.ExpiresMS, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	valid := observed >= claimed && settled >= observed
	switch r.Format {
	case writableRegistrationReceiptFormat:
		valid = valid && settled < expires && r.RecoveryEnvelopeSHA256 == "" && r.RecoveryControlRevision == "" && r.RecoveryIssuedMS == "" && r.RecoveryExpiresMS == ""
	case writableRegistrationRecoveredFormat:
		if !dataSHA(r.RecoveryEnvelopeSHA256) || !dataNumber(r.RecoveryControlRevision, true) || !dataNumber(r.RecoveryIssuedMS, false) || !dataNumber(r.RecoveryExpiresMS, true) {
			valid = false
			break
		}
		revision, _ := strconv.ParseInt(r.RecoveryControlRevision, 10, 64)
		original, _ := strconv.ParseInt(r.Claim.ControlRevision, 10, 64)
		issued, _ := strconv.ParseInt(r.RecoveryIssuedMS, 10, 64)
		end, _ := strconv.ParseInt(r.RecoveryExpiresMS, 10, 64)
		valid = valid && revision >= original && issued > expires && observed >= issued && end > issued && end-issued <= 30000 && settled < end
	default:
		valid = false
	}
	if !valid {
		return WritableBackendRegistrationReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}

type writableRegistrationRow struct {
	stored           admittedCommand
	receipt          string
	created, updated int64
	marker           writableRegistrationClaim
}

func writableRegistrationMarkerMatches(m writableRegistrationClaim, v control.VerifiedWritableBackendRegistrationCommand, running WritableCandidateRunningReceipt, created, now int64) bool {
	c, ref := v.Command(), v.Candidate().Index()
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	revision, _ := strconv.ParseInt(m.ControlRevision, 10, 64)
	sourceRevision, _ := strconv.ParseInt(running.Claim.ControlRevision, 10, 64)
	if running.Format == writableRecoveredRunningFormat {
		observedRevision, _ := strconv.ParseInt(running.RecoveryControlRevision, 10, 64)
		if observedRevision > sourceRevision {
			sourceRevision = observedRevision
		}
	}
	current, _ := strconv.ParseInt(ref.ControlRevision, 10, 64)
	return validWritableRegistrationClaim(m) && m.EffectID == c.EffectID && m.PayloadSHA256 == c.PayloadSHA256 && m.SourceSHA256 == c.RunningReceiptSHA256 && m.DocumentSHA256 == c.DocumentSHA256 && m.NativeID == running.NativeID && revision >= sourceRevision && revision <= current && claimed >= created && claimed <= now
}

// Validate this exact row before exempting it from source history. Missing is
// allowed only for new admission; callers reading/claiming require presence.
func readWritableRegistrationRow(ctx context.Context, tx *sql.Tx, v control.VerifiedWritableBackendRegistrationCommand, now int64) (writableRegistrationRow, bool, error) {
	var row writableRegistrationRow
	index := writableRegistrationIndex(v)
	own, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	row.stored = own
	fence, _ := strconv.ParseInt(index.InstallationFence, 10, 64)
	if !sameLogicalCommand(own, index) || own.fence != fence {
		return row, true, ErrFenceReconciliation
	}
	var ack sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`, index.EffectID).Scan(&row.receipt, &ack, &row.created, &row.updated); err != nil {
		return row, true, err
	}
	if ack.Valid || row.created < 0 || row.updated < row.created || row.updated > now {
		return row, true, ErrFenceReconciliation
	}
	if own.state == "prepared" {
		if row.receipt != "{}" || row.updated != row.created {
			return row, true, ErrFenceReconciliation
		}
		return row, true, nil
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, v.Candidate().Index().EffectID).Scan(&raw); err != nil {
		return row, true, err
	}
	running, err := ParseWritableCandidateRunningReceipt([]byte(raw))
	if err != nil || candidateHash([]byte(raw)) != v.Command().RunningReceiptSHA256 {
		return row, true, ErrFenceReconciliation
	}
	switch own.state {
	case "unknown":
		if !canonicalDataJSON(row.receipt, &row.marker) {
			return row, true, ErrFenceReconciliation
		}
		claimed, _ := strconv.ParseInt(row.marker.ClaimedMS, 10, 64)
		if row.updated != claimed {
			return row, true, ErrFenceReconciliation
		}
	case "applied":
		r, err := ParseWritableBackendRegistrationReceipt([]byte(row.receipt))
		if err != nil {
			return row, true, err
		}
		row.marker = r.Claim
		settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
		if row.updated != settled {
			return row, true, ErrFenceReconciliation
		}
		if r.Format == writableRegistrationRecoveredFormat {
			rev, _ := strconv.ParseInt(r.RecoveryControlRevision, 10, 64)
			current, _ := strconv.ParseInt(index.ControlRevision, 10, 64)
			if rev > current {
				return row, true, ErrFenceReconciliation
			}
		}
	default:
		return row, true, ErrFenceReconciliation
	}
	if !writableRegistrationMarkerMatches(row.marker, v, running, row.created, now) {
		return row, true, ErrFenceReconciliation
	}
	return row, true, nil
}
