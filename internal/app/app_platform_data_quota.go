package app

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Per-installation data quota (doc 185). The descriptor requests a quota; the administrator approves it
// or a smaller value inside the platform bounds; the approved pair is part of the canonical review bytes
// and is what the runner prepares, what the namespace row records and what every later start must see.

// platformQuotaApproval is what the administrator chose at install review. A zero field approves what
// the descriptor requested.
type platformQuotaApproval struct{ bytes, inodes int64 }

type platformQuotaApprovalKey struct{}

// platformWithQuotaApproval carries the approval from the management request to the review that writes it.
func platformWithQuotaApproval(ctx context.Context, bytes, inodes int64) context.Context {
	return context.WithValue(ctx, platformQuotaApprovalKey{}, platformQuotaApproval{bytes, inodes})
}

func platformQuotaApprovalFrom(ctx context.Context) platformQuotaApproval {
	approval, _ := ctx.Value(platformQuotaApprovalKey{}).(platformQuotaApproval)
	return approval
}

func platformBackendQuotaFormat(format string) bool {
	return format == platformNativeInstallFormat || platformManagedFormat(format)
}

// platformManagementQuotaApproval reads the optional approved quota of an install request. It reports how
// many of the two optional fields the request carried.
func platformManagementQuotaApproval(fields map[string]json.RawMessage) (bytes, inodes int64, present int, err error) {
	read := func(name string) (int64, error) {
		if fields[name] == nil {
			return 0, nil
		}
		present++
		value, err := platformManagementString(fields, name)
		if err != nil || !platformPositiveCounter(value) {
			return 0, errPlatformManagementInput
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, errPlatformManagementInput
		}
		return n, nil
	}
	if bytes, err = read("data_quota_bytes"); err != nil {
		return 0, 0, present, err
	}
	inodes, err = read("data_quota_inodes")
	return bytes, inodes, present, err
}

// platformApplyInstallQuota sets the quota fields of an install projection from the descriptor's request
// and the administrator's approval. Frontend installs have no per-installation quota: they keep the
// fixed object-storage limit and refuse an approval.
func platformApplyInstallQuota(p *platformInstallProjection, format string, manifest appplatform.Manifest, approval platformQuotaApproval) error {
	requestedBytes, err := platformInstallProfileQuota(format, manifest)
	if err != nil {
		return err
	}
	if !platformBackendQuotaFormat(format) {
		if approval != (platformQuotaApproval{}) {
			return errAppPlatformPlanChanged
		}
		p.DataQuotaBytes, p.DataQuotaInodes, p.RequestedDataQuotaBytes, p.RequestedDataQuotaInodes = requestedBytes, 0, 0, 0
		return nil
	}
	_, requestedInodes := manifest.Data.QuotaOrDefault()
	bytes, inodes := approval.bytes, approval.inodes
	if bytes == 0 {
		bytes = requestedBytes
	}
	if inodes == 0 {
		inodes = requestedInodes
	}
	if !appplatform.ValidDataQuota(bytes, inodes) || bytes > requestedBytes || inodes > requestedInodes {
		return errAppPlatformPlanChanged
	}
	p.DataQuotaBytes, p.DataQuotaInodes = bytes, inodes
	p.RequestedDataQuotaBytes, p.RequestedDataQuotaInodes = requestedBytes, requestedInodes
	return nil
}

// platformCheckInstallQuota proves a stored plan's quota fields are exactly what review would have
// written: the request is the descriptor's, and the approval is inside the bounds and not above it.
func platformCheckInstallQuota(p platformInstallProjection, format string, manifest appplatform.Manifest) error {
	if platformPreUpgradeQuota(p) {
		// Reviewed before the quota was per installation: 256 MiB and 65,536 inodes, for a descriptor that
		// asks for no more than that.
		if requestedBytes, requestedInodes := manifest.Data.QuotaOrDefault(); p.DataQuotaBytes != appplatform.DataQuotaDefaultBytes ||
			requestedBytes != appplatform.DataQuotaDefaultBytes || requestedInodes != appplatform.DataQuotaDefaultInode {
			return errAppPlatformPlanChanged
		}
		return nil
	}
	approval := platformQuotaApproval{}
	if platformBackendQuotaFormat(format) {
		if p.DataQuotaInodes == 0 {
			return errAppPlatformPlanChanged
		}
		approval = platformQuotaApproval{p.DataQuotaBytes, p.DataQuotaInodes}
	}
	var want platformInstallProjection
	if err := platformApplyInstallQuota(&want, format, manifest, approval); err != nil {
		return err
	}
	if want.DataQuotaBytes != p.DataQuotaBytes || want.DataQuotaInodes != p.DataQuotaInodes ||
		want.RequestedDataQuotaBytes != p.RequestedDataQuotaBytes || want.RequestedDataQuotaInodes != p.RequestedDataQuotaInodes {
		return errAppPlatformPlanChanged
	}
	return nil
}

// platformPreUpgradeQuota reports a backend install plan written before the inode and request fields existed.
func platformPreUpgradeQuota(p platformInstallProjection) bool {
	return platformBackendQuotaFormat(p.Format) && p.DataQuotaInodes == 0 && p.RequestedDataQuotaBytes == 0 && p.RequestedDataQuotaInodes == 0
}

// platformApprovalOfStoredPlan is the approval a stored plan carries, for rebuilding it at consumption.
func platformApprovalOfStoredPlan(p platformInstallProjection) platformQuotaApproval {
	if !platformBackendQuotaFormat(p.Format) {
		return platformQuotaApproval{}
	}
	return platformQuotaApproval{p.DataQuotaBytes, p.DataQuotaInodes}
}

// platformKeepPreUpgradeForm makes the rebuilt projection of a pre-upgrade plan match its stored bytes. A
// pre-upgrade plan is only ever the 256 MiB / 65,536 plan of a descriptor that asks for no more, so anything
// else is refused here, at consumption, and not only at the install stage.
func platformKeepPreUpgradeForm(stored platformInstallProjection, rebuilt *platformInstallProjection) error {
	if !platformPreUpgradeQuota(stored) {
		return nil
	}
	if stored.DataQuotaBytes != appplatform.DataQuotaDefaultBytes ||
		rebuilt.RequestedDataQuotaBytes != appplatform.DataQuotaDefaultBytes || rebuilt.RequestedDataQuotaInodes != appplatform.DataQuotaDefaultInode {
		return errAppPlatformPlanChanged
	}
	rebuilt.DataQuotaInodes, rebuilt.RequestedDataQuotaBytes, rebuilt.RequestedDataQuotaInodes = 0, 0, 0
	return nil
}

// approvedQuota is the quota an install plan approved. A plan written before the inode fields existed
// reads as the 65,536 every namespace then had.
func (p platformInstallProjection) approvedQuota() (bytes, inodes int64) {
	inodes = p.DataQuotaInodes
	if inodes == 0 && p.DataQuotaBytes > 0 {
		inodes = appplatform.DataQuotaDefaultInode
	}
	return p.DataQuotaBytes, inodes
}

// platformPlanQuota reads the approved quota out of a stored backend install plan.
func platformPlanQuota(raw string) (bytes, inodes int64, err error) {
	if _, err := platformParseInstallProfile(raw); err != nil {
		return 0, 0, err
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(raw), &p) != nil || !platformBackendQuotaFormat(p.Format) {
		return 0, 0, errAppPlatformStageEvidence
	}
	bytes, inodes = p.approvedQuota()
	if !appplatform.ValidDataQuota(bytes, inodes) {
		return 0, 0, errAppPlatformStageEvidence
	}
	return bytes, inodes, nil
}

// platformUpdateQuotaReview is what an update review shows when the target release asks for more data quota
// than the installation has approved. The approved quota only ever rises, so an equal or smaller request is
// not shown. RaiseApplied is false in this phase: the update keeps the approved quota, and the raise is its
// own reviewed action (set_data_quota), which the review says.
type platformUpdateQuotaReview struct {
	CurrentBytes    int64 `json:"current_bytes"`
	CurrentInodes   int64 `json:"current_inodes"`
	RequestedBytes  int64 `json:"requested_bytes"`
	RequestedInodes int64 `json:"requested_inodes"`
	RaiseBytes      int64 `json:"raise_bytes"`
	RaiseInodes     int64 `json:"raise_inodes"`
	RaiseApplied    bool  `json:"raise_applied"`
}

// platformUpdateQuotaDifference compares the quota the installation runs with against the target's request.
func platformUpdateQuotaDifference(currentBytes, currentInodes int64, target appplatform.Manifest) *platformUpdateQuotaReview {
	requestedBytes, requestedInodes := target.Data.QuotaOrDefault()
	raiseBytes, raiseInodes := max(requestedBytes-currentBytes, 0), max(requestedInodes-currentInodes, 0)
	if raiseBytes == 0 && raiseInodes == 0 {
		return nil
	}
	return &platformUpdateQuotaReview{CurrentBytes: currentBytes, CurrentInodes: currentInodes, RequestedBytes: requestedBytes, RequestedInodes: requestedInodes, RaiseBytes: raiseBytes, RaiseInodes: raiseInodes}
}

// platformValidQuotaBytes is the platform's bound on one namespace's byte limit.
func platformValidQuotaBytes(bytes int64) bool {
	return appplatform.ValidDataQuota(bytes, appplatform.DataQuotaMinInodes)
}

// platformApprovedNamespaceQuota returns the namespace's byte quota after proving that both its limits are the last
// link of its quota chain: the install review's approved pair, or the pair of the latest completed raise
// (set_data_quota). The quota only changes through a reviewed action, and the operation that completes one updates
// the row in the same transaction, so the row and the chain always agree.
func platformApprovedNamespaceQuota(e migExec, installation, namespace string) (int64, error) {
	bytes, _, _, err := platformCurrentNamespaceQuota(e, installation, namespace)
	return bytes, err
}
