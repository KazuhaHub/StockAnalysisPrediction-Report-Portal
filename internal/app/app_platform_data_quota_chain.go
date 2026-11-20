package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The quota of a namespace is a chain of reviewed values (doc 185 section 3.4): the install review's approved
// pair is its first link, and each applied set-data-quota effect is the next, raising the limits from exactly
// where the previous link ended. A link is either a reviewed set_data_quota operation or an approved update that
// raised the quota as one of its effects. The namespace row always holds the last link, because the transaction
// that applies a raise updates the row with it. Every loader that used to compare the row with the install plan
// reads the chain instead, so a raised quota survives update, restore and fresh-host import.
//
// The chain is derived from the applied effect rows alone, never from an operation's stage: an update's raise is a
// link from the moment it applies, while its operation is still running, and stays one if the update later fails
// and rolls back (a checkpoint restores contents, not limits). An applied raise row is therefore never rewritten:
// its state, receipt and update time are the evidence, and retention keeps the operation that holds it.

// platformQuotaLink is one link of the chain.
type platformQuotaLink struct {
	Bytes, Inodes int64
	// CompletedMS is when the link took effect: zero for the install's approval, otherwise the update time of
	// the applied effect row, which the same transaction wrote into the namespace row.
	CompletedMS int64
}

// platformUpdateRaiseEffectNumber is the Portal's slot of an update's quota raise in its migrate stage. Slot 1
// is the source removal, slot 2 the migrate-data effect and slot 3 the settlement's discard (a fixed number of the
// runner's contract), so the raise takes the next free one. Slot 4 runs before slot 2: the raise must come before
// a migration, whose payload carries the quota, so the order of execution is 1, 4, 2 and nothing may assume the
// numbers ascend with time. The runner never sees this number: its command header names prepare_data/1, which
// the update's operation does not otherwise use.
const platformUpdateRaiseEffectNumber = 4

// platformAppliedQuotaRaise is an applied set-data-quota effect row with the operation and plan it belongs to.
type platformAppliedQuotaRaise struct {
	operation, effect, stage, digest, receiptRaw string
	number                                       int
	updatedMS                                    int64
	action, operationStage, planRaw              string
	errorCode                                    sql.NullString
	completedMS                                  sql.NullInt64
	consumedMS                                   sql.NullInt64
	oldBytes, oldInodes, newBytes, newInodes     int64
}

// platformNamespaceQuotaChain returns the chain of a namespace, oldest first, without comparing it with the row.
func platformNamespaceQuotaChain(e migExec, installation, namespace string) ([]platformQuotaLink, error) {
	var raw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='install' ORDER BY o.created_ms,o.id LIMIT 1`, installation).Scan(&raw); err != nil {
		return nil, err
	}
	bytes, inodes, err := platformPlanQuota(raw)
	if err != nil {
		return nil, err
	}
	chain := []platformQuotaLink{{Bytes: bytes, Inodes: inodes}}
	rows, err := e.query(`SELECT f.operation_id,f.effect_id,f.stage,f.effect_number,f.payload_sha256,f.receipt_json,f.updated_ms,
		o.action,o.stage,o.error_code,o.completed_ms,p.plan_json,p.consumed_ms
		FROM app_platform_operation_effects f
		JOIN app_platform_operations o ON o.id=f.operation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND f.kind='set-data-quota' AND f.state='applied' AND o.action IN ('set_data_quota','update')
		ORDER BY f.updated_ms,f.operation_id LIMIT 1025`, installation)
	if err != nil {
		return nil, err
	}
	var raises []platformAppliedQuotaRaise
	for rows.Next() {
		var r platformAppliedQuotaRaise
		if err := rows.Scan(&r.operation, &r.effect, &r.stage, &r.number, &r.digest, &r.receiptRaw, &r.updatedMS,
			&r.action, &r.operationStage, &r.errorCode, &r.completedMS, &r.planRaw, &r.consumedMS); err != nil {
			rows.Close()
			return nil, err
		}
		raises = append(raises, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(raises) > 1024 {
		return nil, fmt.Errorf("%w: quota chain is too long", errAppPlatformStageEvidence)
	}
	for i := range raises {
		if err := raises[i].verify(installation, namespace); err != nil {
			return nil, err
		}
	}
	// Order by linkage, never by time: each link starts where the one before it ended. A fork (two links from one
	// point), a leftover receipt (one that is never reached) and a link that does not grow are corrupt evidence.
	used := make([]bool, len(raises))
	for range raises {
		last := chain[len(chain)-1]
		next := -1
		for i, r := range raises {
			if used[i] || r.oldBytes != last.Bytes || r.oldInodes != last.Inodes {
				continue
			}
			if next >= 0 {
				return nil, fmt.Errorf("%w: quota chain forks", errAppPlatformStageEvidence)
			}
			next = i
		}
		if next < 0 {
			break
		}
		used[next] = true
		chain = append(chain, platformQuotaLink{Bytes: raises[next].newBytes, Inodes: raises[next].newInodes, CompletedMS: raises[next].updatedMS})
	}
	for _, linked := range used {
		if !linked {
			return nil, fmt.Errorf("%w: quota chain has a receipt that is not linked", errAppPlatformStageEvidence)
		}
	}
	return chain, nil
}

// verify proves an applied raise row is what it says: its receipt names this row's operation, effect and payload
// digest, this installation and namespace, and the operation's own plan agrees with the receipt. It fills in the
// receipt's limits.
func (r *platformAppliedQuotaRaise) verify(installation, namespace string) error {
	bad := func(reason string) error {
		return fmt.Errorf("%w: quota chain %s", errAppPlatformStageEvidence, reason)
	}
	receipt, err := journal.ParseSetDataQuotaReceipt([]byte(r.receiptRaw))
	if err != nil {
		return bad("receipt")
	}
	if receipt.OperationID != r.operation || receipt.EffectID != r.effect || receipt.PayloadSHA256 != r.digest ||
		receipt.InstallationID != installation || receipt.NamespaceID != namespace || receipt.Stage != "prepare_data" {
		return bad("receipt binding")
	}
	var ok1, ok2, ok3, ok4 bool
	r.oldBytes, ok1 = parsePlatformQuotaInt(receipt.OldBytes)
	r.oldInodes, ok2 = parsePlatformQuotaInt(receipt.OldInodes)
	r.newBytes, ok3 = parsePlatformQuotaInt(receipt.NewBytes)
	r.newInodes, ok4 = parsePlatformQuotaInt(receipt.NewInodes)
	if !ok1 || !ok2 || !ok3 || !ok4 || r.newBytes < r.oldBytes || r.newInodes < r.oldInodes ||
		(r.newBytes == r.oldBytes && r.newInodes == r.oldInodes) || !appplatform.WithinRunnerDataQuota(r.newBytes, r.newInodes) {
		return bad("link")
	}
	switch r.action {
	case "set_data_quota":
		if r.stage != "prepare_data" || r.number != 1 || r.operationStage != "complete" || r.errorCode.Valid ||
			!r.completedMS.Valid || !r.consumedMS.Valid {
			return bad("operation")
		}
		var plan platformDataQuotaPlan
		if json.Unmarshal([]byte(r.planRaw), &plan) != nil || plan.Format != platformSetDataQuotaFormat ||
			plan.InstallationID != installation || plan.DataNamespaceID != namespace ||
			plan.CurrentQuotaBytes != r.oldBytes || plan.CurrentQuotaInodes != r.oldInodes ||
			plan.NewQuotaBytes != r.newBytes || plan.NewQuotaInodes != r.newInodes {
			return bad("plan")
		}
	case "update":
		// An update's raise is a link while the update is in flight, failed or recovering, so no stage is required.
		if r.stage != "migrate" || r.number != platformUpdateRaiseEffectNumber || !r.consumedMS.Valid {
			return bad("operation")
		}
		var plan platformManagedUpdatePlan
		if json.Unmarshal([]byte(r.planRaw), &plan) != nil || plan.Format != platformManagedUpdateFormat ||
			plan.InstallationID != installation || plan.DataNamespaceID != namespace || plan.DataQuota == nil || !plan.DataQuota.RaiseApplied ||
			plan.DataQuota.CurrentBytes != r.oldBytes || plan.DataQuota.CurrentInodes != r.oldInodes ||
			plan.DataQuota.NewBytes != r.newBytes || plan.DataQuota.NewInodes != r.newInodes {
			return bad("update plan")
		}
	default:
		return bad("action")
	}
	return nil
}

func parsePlatformQuotaInt(value string) (int64, bool) {
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && n > 0
}

// platformCurrentNamespaceQuota is the last link of the chain, after proving that the namespace row holds it.
func platformCurrentNamespaceQuota(e migExec, installation, namespace string) (bytes, inodes int64, chain []platformQuotaLink, err error) {
	chain, err = platformNamespaceQuotaChain(e, installation, namespace)
	if err != nil {
		return 0, 0, nil, err
	}
	last := chain[len(chain)-1]
	var rowBytes, rowInodes int64
	if err := e.queryRow(`SELECT quota_bytes,quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace).Scan(&rowBytes, &rowInodes); err != nil {
		return 0, 0, nil, err
	}
	if rowBytes != last.Bytes || rowInodes != last.Inodes {
		return 0, 0, nil, errAppPlatformStageEvidence
	}
	return last.Bytes, last.Inodes, chain, nil
}

// platformQuotaInChain reports whether the pair is a link of the chain: the quota a start recorded at its own time
// may be any link, because a raise can come before or after it.
func platformQuotaInChain(chain []platformQuotaLink, bytes, inodes int64) bool {
	for _, link := range chain {
		if link.Bytes == bytes && link.Inodes == inodes {
			return true
		}
	}
	return false
}

// platformNamespaceRaises counts the raises of the chain and returns when the last took effect.
func platformNamespaceRaises(chain []platformQuotaLink) (count, lastCompletedMS int64) {
	for _, link := range chain[1:] {
		count++
		lastCompletedMS = link.CompletedMS
	}
	return count, lastCompletedMS
}
