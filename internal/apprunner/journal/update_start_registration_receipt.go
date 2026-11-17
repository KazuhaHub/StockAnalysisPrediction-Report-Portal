package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const updateStartRegistrationClaimFormat = "portal.runner.update-start-registration-claim/1-dev.1"
const updateStartRegistrationReceiptFormat = "portal.runner.update-start-registration-delivery/1-dev.1"
const updateStartRegistrationRecoveredFormat = "portal.runner.reconciled-update-start-registration-delivery/1-dev.1"

type updateStartRegistrationClaim struct {
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

func validUpdateStartRegistrationClaim(m updateStartRegistrationClaim) bool {
	if m.Format != updateStartRegistrationClaimFormat ||
		!enrollmentUUID.MatchString(m.ClaimID) ||
		m.ClaimID == "00000000-0000-0000-0000-000000000000" ||
		!enrollmentUUID.MatchString(m.EffectID) ||
		m.EffectID == "00000000-0000-0000-0000-000000000000" ||
		!dataNumber(m.ControlRevision, true) ||
		!dataNumber(m.ClaimedMS, false) || !dataNumber(m.ExpiresMS, true) {
		return false
	}
	for _, digest := range []string{
		m.PayloadSHA256, m.SourceSHA256, m.NativeID, m.DocumentSHA256,
	} {
		if !dataSHA(digest) {
			return false
		}
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	return expires > claimed && expires-claimed <= 30000
}

func updateStartRegistrationBinding(m updateStartRegistrationClaim) string {
	raw, _ := json.Marshal(m)
	return candidateHash(raw)
}

type UpdateStartRegistrationReceipt struct {
	Format                  string                       `json:"format"`
	Claim                   updateStartRegistrationClaim `json:"claim"`
	BindingSHA256           string                       `json:"binding_sha256"`
	Device                  string                       `json:"device"`
	Inode                   string                       `json:"inode"`
	ObservedMS              string                       `json:"observed_ms"`
	SettledMS               string                       `json:"settled_ms"`
	RecoveryEnvelopeSHA256  string                       `json:"recovery_envelope_sha256,omitempty"`
	RecoveryControlRevision string                       `json:"recovery_control_revision,omitempty"`
	RecoveryIssuedMS        string                       `json:"recovery_issued_ms,omitempty"`
	RecoveryExpiresMS       string                       `json:"recovery_expires_ms,omitempty"`
}

func ParseUpdateStartRegistrationReceipt(raw []byte) (UpdateStartRegistrationReceipt, error) {
	var r UpdateStartRegistrationReceipt
	if len(raw) == 0 || len(raw) > 8192 ||
		!canonicalDataJSON(string(raw), &r) ||
		!validUpdateStartRegistrationClaim(r.Claim) ||
		r.BindingSHA256 != updateStartRegistrationBinding(r.Claim) ||
		!nativeNumber(r.Device, false) || !nativeNumber(r.Inode, true) ||
		!dataNumber(r.ObservedMS, false) || !dataNumber(r.SettledMS, false) {
		return UpdateStartRegistrationReceipt{}, ErrFenceReconciliation
	}
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(r.Claim.ExpiresMS, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	valid := observed >= claimed && settled >= observed
	switch r.Format {
	case updateStartRegistrationReceiptFormat:
		valid = valid && settled < expires &&
			r.RecoveryEnvelopeSHA256 == "" &&
			r.RecoveryControlRevision == "" &&
			r.RecoveryIssuedMS == "" && r.RecoveryExpiresMS == ""
	case updateStartRegistrationRecoveredFormat:
		if !dataSHA(r.RecoveryEnvelopeSHA256) ||
			!dataNumber(r.RecoveryControlRevision, true) ||
			!dataNumber(r.RecoveryIssuedMS, false) ||
			!dataNumber(r.RecoveryExpiresMS, true) {
			valid = false
			break
		}
		revision, _ := strconv.ParseInt(r.RecoveryControlRevision, 10, 64)
		original, _ := strconv.ParseInt(r.Claim.ControlRevision, 10, 64)
		issued, _ := strconv.ParseInt(r.RecoveryIssuedMS, 10, 64)
		end, _ := strconv.ParseInt(r.RecoveryExpiresMS, 10, 64)
		valid = valid && revision >= original && issued > expires &&
			observed >= issued && end > issued && end-issued <= 30000 &&
			settled < end
	default:
		valid = false
	}
	if !valid {
		return UpdateStartRegistrationReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}

type updateStartRegistrationRow struct {
	stored           admittedCommand
	receipt          string
	created, updated int64
	marker           updateStartRegistrationClaim
}

func updateStartRegistrationMarkerMatches(m updateStartRegistrationClaim, v control.VerifiedUpdateStartRegistrationCommand, running UpdateStartRunningReceipt, created, now int64) bool {
	c, start := v.Command(), v.Start().Command()
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	revision, _ := strconv.ParseInt(m.ControlRevision, 10, 64)
	sourceRevision, _ := strconv.ParseInt(running.Claim.ControlRevision, 10, 64)
	current, _ := strconv.ParseInt(start.ControlRevision, 10, 64)
	return validUpdateStartRegistrationClaim(m) &&
		m.EffectID == c.EffectID && m.PayloadSHA256 == c.PayloadSHA256 &&
		m.SourceSHA256 == c.RunningReceiptSHA256 &&
		m.DocumentSHA256 == c.DocumentSHA256 &&
		m.NativeID == running.NativeID && revision >= sourceRevision &&
		revision <= current && claimed >= created && claimed <= now
}

func readUpdateStartRegistrationRow(ctx context.Context, tx *sql.Tx, v control.VerifiedUpdateStartRegistrationCommand, now int64) (updateStartRegistrationRow, bool, error) {
	var row updateStartRegistrationRow
	index := updateStartRegistrationIndex(v)
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
	if err := tx.QueryRowContext(ctx,
		`SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`,
		index.EffectID).
		Scan(&row.receipt, &ack, &row.created, &row.updated); err != nil {
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
	start := v.Start().Command()
	if err := tx.QueryRowContext(ctx,
		`SELECT receipt_json FROM runner_commands WHERE effect_id=?`,
		start.EffectID).Scan(&raw); err != nil {
		return row, true, err
	}
	running, err := ParseUpdateStartRunningReceipt([]byte(raw))
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
		receipt, err := ParseUpdateStartRegistrationReceipt([]byte(row.receipt))
		if err != nil {
			return row, true, err
		}
		row.marker = receipt.Claim
		settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
		if row.updated != settled {
			return row, true, ErrFenceReconciliation
		}
		if receipt.Format == updateStartRegistrationRecoveredFormat {
			revision, _ := strconv.ParseInt(receipt.RecoveryControlRevision, 10, 64)
			current, _ := strconv.ParseInt(index.ControlRevision, 10, 64)
			if revision > current {
				return row, true, ErrFenceReconciliation
			}
		}
	default:
		return row, true, ErrFenceReconciliation
	}
	if !updateStartRegistrationMarkerMatches(row.marker, v, running, row.created, now) {
		return row, true, ErrFenceReconciliation
	}
	return row, true, nil
}
