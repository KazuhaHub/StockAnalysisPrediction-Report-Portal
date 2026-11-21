package app

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestUpdateQuotaReviewShowsOnlyAnIncrease(t *testing.T) {
	for _, c := range []struct {
		name             string
		curBytes, curIno int64
		quota            *appplatform.DataQuota
		want             *platformUpdateQuotaReview
	}{
		{"no block and today's limits", 256 << 20, 65536, nil, nil},
		{"asks for less keeps the approved quota", 3 << 30, 100000, &appplatform.DataQuota{Bytes: 1 << 30, Inodes: 70000}, nil},
		{"asks for the same", 3 << 30, 100000, &appplatform.DataQuota{Bytes: 3 << 30, Inodes: 100000}, nil},
		{"asks for more bytes", 256 << 20, 65536, &appplatform.DataQuota{Bytes: 3 << 30, Inodes: 65536},
			&platformUpdateQuotaReview{CurrentBytes: 256 << 20, CurrentInodes: 65536, RequestedBytes: 3 << 30, RequestedInodes: 65536, RaiseBytes: 3<<30 - 256<<20,
				RaiseApplied: true, NewBytes: 3 << 30, NewInodes: 65536, Capacity: "fits"}},
		{"asks for more of one and less of the other", 1 << 30, 100000, &appplatform.DataQuota{Bytes: 2 << 30, Inodes: 70000},
			&platformUpdateQuotaReview{CurrentBytes: 1 << 30, CurrentInodes: 100000, RequestedBytes: 2 << 30, RequestedInodes: 70000, RaiseBytes: 1 << 30,
				RaiseApplied: true, NewBytes: 2 << 30, NewInodes: 100000, Capacity: "fits"}},
	} {
		manifest := appplatform.Manifest{}
		manifest.Data.Quota = c.quota
		got := platformUpdateQuotaDifference(c.curBytes, c.curIno, manifest)
		switch {
		case c.want == nil && got != nil, c.want != nil && (got == nil || *got != *c.want):
			t.Fatalf("%s: got %+v want %+v", c.name, got, c.want)
		}
		// The update applies the raise, and the new quota never falls below the current one in either dimension.
		if got != nil && (!got.RaiseApplied || got.NewBytes < got.CurrentBytes || got.NewInodes < got.CurrentInodes ||
			got.NewBytes != max(got.CurrentBytes, got.RequestedBytes) || got.NewInodes != max(got.CurrentInodes, got.RequestedInodes)) {
			t.Fatalf("%s: the raise is not the larger of each dimension: %+v", c.name, got)
		}
	}
}
