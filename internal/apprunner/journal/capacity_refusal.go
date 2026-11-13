package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const capacityRefusalFormat = "portal.runner.capacity-refusal/1-dev.1"

// CapacityIdentity is the identity of the signed command a refusal answers.
type CapacityIdentity struct {
	DeploymentID, ControllerEpoch, InstallationID, OperationID, TargetGeneration string
	Stage, EffectNumber, EffectID, Kind, PayloadSHA256                           string
}

// CapacityIdentityOfCommand takes the identity from a generic signed command.
func CapacityIdentityOfCommand(c control.Command) CapacityIdentity {
	return CapacityIdentity{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.EffectID, c.Kind, c.PayloadSHA256}
}

// CapacityIdentityOfHeader takes the identity from the header of a migration or load command.
func CapacityIdentityOfHeader(h control.CommandHeader) CapacityIdentity {
	return CapacityIdentity{h.DeploymentID, h.ControllerEpoch, h.InstallationID, h.OperationID, h.TargetGeneration, h.Stage, h.EffectNumber, h.EffectID, h.Kind, h.PayloadSHA256}
}

// CapacityRefusal is the definite answer of a start-bearing claim the runner refused because the declared
// envelope has no room: outcome "capacity_exhausted", the dimension that did not fit, and what was requested and
// what remained in it. It is stored as the receipt of a reverted command, so a replay returns the same receipt,
// and it is the only receipt of an effect that never claimed anything.
type CapacityRefusal struct {
	Format           string `json:"format"`
	DeploymentID     string `json:"deployment_id"`
	ControllerEpoch  string `json:"controller_epoch"`
	InstallationID   string `json:"installation_id"`
	OperationID      string `json:"operation_id"`
	TargetGeneration string `json:"target_generation"`
	Stage            string `json:"stage"`
	EffectNumber     string `json:"effect_number"`
	EffectID         string `json:"effect_id"`
	Kind             string `json:"kind"`
	PayloadSHA256    string `json:"payload_sha256"`
	Outcome          string `json:"outcome"`
	Dimension        string `json:"dimension"`
	Requested        string `json:"requested"`
	Remaining        string `json:"remaining"`
	SettledMS        string `json:"settled_ms"`
}

// CapacityRefusalError reports a refusal. The refusal is already durable when it is returned.
type CapacityRefusalError struct{ Receipt CapacityRefusal }

func (e *CapacityRefusalError) Error() string {
	return "runner capacity exhausted: " + e.Receipt.Dimension + " requested=" + e.Receipt.Requested + " remaining=" + e.Receipt.Remaining
}

func newCapacityRefusal(id CapacityIdentity, r capacityRefusal, now int64) CapacityRefusal {
	return CapacityRefusal{
		Format: capacityRefusalFormat, DeploymentID: id.DeploymentID, ControllerEpoch: id.ControllerEpoch, InstallationID: id.InstallationID,
		OperationID: id.OperationID, TargetGeneration: id.TargetGeneration, Stage: id.Stage, EffectNumber: id.EffectNumber, EffectID: id.EffectID,
		Kind: id.Kind, PayloadSHA256: id.PayloadSHA256, Outcome: CapacityExhaustedOutcome, Dimension: r.dimension,
		Requested: strconv.FormatInt(r.requested, 10), Remaining: strconv.FormatInt(r.remaining, 10), SettledMS: strconv.FormatInt(now, 10),
	}
}

func validCapacityDimension(d string) bool {
	switch d {
	case CapacityMemory, CapacityCPU, CapacityDisk, CapacityImageStore, CapacityRunningBackends:
		return true
	}
	return false
}

func validCapacityRefusal(r CapacityRefusal) bool {
	if r.Format != capacityRefusalFormat || r.Outcome != CapacityExhaustedOutcome || !validCapacityDimension(r.Dimension) || !dataSHA(r.PayloadSHA256) ||
		!dataNumber(r.TargetGeneration, true) || !dataNumber(r.EffectNumber, true) || !dataNumber(r.Requested, true) || !dataNumber(r.Remaining, false) ||
		!dataNumber(r.SettledMS, true) || r.Stage == "" || len(r.Stage) > 32 || r.Kind == "" || len(r.Kind) > 32 {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID} {
		if !importID(id) {
			return false
		}
	}
	return true
}

// ParseCapacityRefusal accepts only the canonical encoding of a valid refusal.
func ParseCapacityRefusal(raw []byte) (CapacityRefusal, error) {
	var r CapacityRefusal
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &r) || !validCapacityRefusal(r) {
		return CapacityRefusal{}, ErrFenceReconciliation
	}
	return r, nil
}

// Matches reports whether the refusal answers exactly this signed command.
func (r CapacityRefusal) Matches(id CapacityIdentity) bool {
	return r.DeploymentID == id.DeploymentID && r.ControllerEpoch == id.ControllerEpoch && r.InstallationID == id.InstallationID &&
		r.OperationID == id.OperationID && r.TargetGeneration == id.TargetGeneration && r.Stage == id.Stage && r.EffectNumber == id.EffectNumber &&
		r.EffectID == id.EffectID && r.Kind == id.Kind && r.PayloadSHA256 == id.PayloadSHA256
}

// storedCapacityRefusal decodes a reverted row's receipt when it is a refusal for this command.
func storedCapacityRefusal(raw string, id CapacityIdentity) (CapacityRefusal, bool) {
	r, err := ParseCapacityRefusal([]byte(raw))
	if err != nil || !r.Matches(id) {
		return CapacityRefusal{}, false
	}
	return r, true
}

// refuseCapacityPrepared settles a prepared, never claimed command as reverted with the refusal. It writes only
// the command row, in the caller's transaction.
func refuseCapacityPrepared(ctx context.Context, tx *sql.Tx, id CapacityIdentity, r capacityRefusal, now int64) (CapacityRefusal, error) {
	receipt := newCapacityRefusal(id, r, now)
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > 4096 || !validCapacityRefusal(receipt) {
		return CapacityRefusal{}, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL AND payload_sha256=?`,
		string(raw), now, id.EffectID, id.PayloadSHA256)
	if err != nil {
		return CapacityRefusal{}, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return CapacityRefusal{}, errors.Join(ErrFenceReconciliation, err)
	}
	return receipt, nil
}

// refuseCapacityInsert records a refusal for a command that had no row yet: the migration and load claims
// insert their row at the claim, so their refusal is the row.
func refuseCapacityInsert(ctx context.Context, tx *sql.Tx, id CapacityIdentity, fence, generation int64, r capacityRefusal, now int64) (CapacityRefusal, error) {
	receipt := newCapacityRefusal(id, r, now)
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > 4096 || !validCapacityRefusal(receipt) {
		return CapacityRefusal{}, ErrFenceReconciliation
	}
	slot, err := strconv.ParseInt(id.EffectNumber, 10, 64)
	if err != nil {
		return CapacityRefusal{}, ErrFenceReconciliation
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'reverted',?,NULL,?,?)`,
		id.EffectID, id.OperationID, id.InstallationID, id.Stage, slot, id.Kind, id.PayloadSHA256, generation, id.ControllerEpoch, fence, string(raw), now, now); err != nil {
		return CapacityRefusal{}, err
	}
	return receipt, nil
}
