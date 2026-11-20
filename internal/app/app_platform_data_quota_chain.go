package app

import (
	"encoding/json"
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The quota of a namespace is a chain of reviewed values (doc 185 section 3.4): the install review's approved
// pair is its first link, and each completed set_data_quota operation is the next, raising the limits from exactly
// where the previous link ended. The namespace row always holds the last link, because the operation that
// completes a raise updates the row in the same transaction. Every loader that used to compare the row with the
// install plan reads the chain instead, so a raised quota survives update, restore and fresh-host import.

// platformQuotaLink is one link of the chain.
type platformQuotaLink struct {
	Bytes, Inodes int64
	// CompletedMS is when the link took effect: zero for the install's approval.
	CompletedMS int64
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
	rows, err := e.query(`SELECT p.plan_json,o.completed_ms FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='set_data_quota' AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.error_code IS NULL
		AND p.action='set_data_quota' AND p.consumed_ms IS NOT NULL ORDER BY o.completed_ms,o.id LIMIT 1025`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var planRaw string
		var completed int64
		if err := rows.Scan(&planRaw, &completed); err != nil {
			return nil, err
		}
		var plan platformDataQuotaPlan
		last := chain[len(chain)-1]
		if len(chain) > 1024 || json.Unmarshal([]byte(planRaw), &plan) != nil || plan.Format != platformSetDataQuotaFormat ||
			plan.InstallationID != installation || plan.DataNamespaceID != namespace ||
			plan.CurrentQuotaBytes != last.Bytes || plan.CurrentQuotaInodes != last.Inodes ||
			plan.NewQuotaBytes < last.Bytes || plan.NewQuotaInodes < last.Inodes ||
			(plan.NewQuotaBytes == last.Bytes && plan.NewQuotaInodes == last.Inodes) ||
			!appplatform.WithinRunnerDataQuota(plan.NewQuotaBytes, plan.NewQuotaInodes) {
			return nil, fmt.Errorf("%w: quota chain", errAppPlatformStageEvidence)
		}
		chain = append(chain, platformQuotaLink{Bytes: plan.NewQuotaBytes, Inodes: plan.NewQuotaInodes, CompletedMS: completed})
	}
	return chain, rows.Err()
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
