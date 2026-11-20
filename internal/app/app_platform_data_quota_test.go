package app

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func quotaManifest(quota *appplatform.DataQuota) appplatform.Manifest {
	m := appplatform.Manifest{Backend: &appplatform.Backend{}}
	m.Data.Quota = quota
	return m
}

func TestInstallQuotaApprovalDefaultsAndSmallerValues(t *testing.T) {
	want := &appplatform.DataQuota{Bytes: 3 << 30, Inodes: 100000}
	for _, c := range []struct {
		name     string
		manifest appplatform.Manifest
		approval platformQuotaApproval
		bytes    int64
		inodes   int64
		reqBytes int64
		reqIno   int64
		err      bool
	}{
		{"no block keeps today's limits", quotaManifest(nil), platformQuotaApproval{}, 256 << 20, 65536, 256 << 20, 65536, false},
		{"approve the request as is", quotaManifest(want), platformQuotaApproval{}, 3 << 30, 100000, 3 << 30, 100000, false},
		{"approve a smaller value", quotaManifest(want), platformQuotaApproval{bytes: 1 << 30, inodes: 70000}, 1 << 30, 70000, 3 << 30, 100000, false},
		{"approve bytes only", quotaManifest(want), platformQuotaApproval{bytes: 1 << 30}, 1 << 30, 100000, 3 << 30, 100000, false},
		{"larger than requested", quotaManifest(want), platformQuotaApproval{bytes: 4 << 30}, 0, 0, 0, 0, true},
		{"more inodes than requested", quotaManifest(want), platformQuotaApproval{inodes: 100001}, 0, 0, 0, 0, true},
		{"below the platform minimum", quotaManifest(want), platformQuotaApproval{bytes: 128 << 20}, 0, 0, 0, 0, true},
		{"not a 64 MiB multiple", quotaManifest(want), platformQuotaApproval{bytes: 1<<30 + 1}, 0, 0, 0, 0, true},
		{"inodes below the minimum", quotaManifest(want), platformQuotaApproval{inodes: 1000}, 0, 0, 0, 0, true},
	} {
		var p platformInstallProjection
		err := platformApplyInstallQuota(&p, platformManagedInstallFormat, c.manifest, c.approval)
		if (err != nil) != c.err {
			t.Fatalf("%s: err = %v", c.name, err)
		}
		if c.err {
			if !errors.Is(err, errAppPlatformPlanChanged) {
				t.Fatalf("%s: %v", c.name, err)
			}
			continue
		}
		if p.DataQuotaBytes != c.bytes || p.DataQuotaInodes != c.inodes || p.RequestedDataQuotaBytes != c.reqBytes || p.RequestedDataQuotaInodes != c.reqIno {
			t.Fatalf("%s: projection %+v", c.name, p)
		}
		if err := platformCheckInstallQuota(p, platformManagedInstallFormat, c.manifest); err != nil {
			t.Fatalf("%s: a plan the review wrote must verify: %v", c.name, err)
		}
	}
}

func TestInstallQuotaCheckRefusesATamperedPlan(t *testing.T) {
	manifest := quotaManifest(&appplatform.DataQuota{Bytes: 3 << 30, Inodes: 100000})
	var p platformInstallProjection
	if err := platformApplyInstallQuota(&p, platformManagedInstallFormat, manifest, platformQuotaApproval{}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*platformInstallProjection){
		"raised above the request":        func(p *platformInstallProjection) { p.DataQuotaBytes = 4 << 30 },
		"request does not match the app":  func(p *platformInstallProjection) { p.RequestedDataQuotaBytes = 2 << 30 },
		"approved inodes dropped":         func(p *platformInstallProjection) { p.DataQuotaInodes = 0 },
		"approved bytes off the grid":     func(p *platformInstallProjection) { p.DataQuotaBytes = 3<<30 - 1 },
		"inodes above the platform bound": func(p *platformInstallProjection) { p.DataQuotaInodes = 2000000 },
	} {
		q := p
		mutate(&q)
		if err := platformCheckInstallQuota(q, platformManagedInstallFormat, manifest); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestFrontendInstallKeepsItsObjectQuota(t *testing.T) {
	m := appplatform.Manifest{}
	m.Capabilities.Required = []string{"storage.files"}
	var p platformInstallProjection
	if err := platformApplyInstallQuota(&p, platformFrontendInstallFormat, m, platformQuotaApproval{}); err != nil {
		t.Fatal(err)
	}
	if p.DataQuotaBytes != 256<<20 || p.DataQuotaInodes != 0 || p.RequestedDataQuotaBytes != 0 {
		t.Fatalf("frontend object quota changed: %+v", p)
	}
	if err := platformApplyInstallQuota(&p, platformFrontendInstallFormat, m, platformQuotaApproval{bytes: 512 << 20}); err == nil {
		t.Fatal("a frontend install has no per-installation quota to approve")
	}
}

func TestPlansWrittenBeforeTheQuotaFieldsReadAsToday(t *testing.T) {
	var p platformInstallProjection
	if err := json.Unmarshal([]byte(`{"format":"`+platformManagedInstallFormat+`","data_quota_bytes":268435456}`), &p); err != nil {
		t.Fatal(err)
	}
	if b, i := p.approvedQuota(); b != 256<<20 || i != 65536 {
		t.Fatalf("legacy plan reads %d %d", b, i)
	}
}

func TestPrepareDataCommandCarriesTheApprovedQuota(t *testing.T) {
	manifest := quotaManifest(&appplatform.DataQuota{Bytes: 3 << 30, Inodes: 100000})
	var p platformInstallProjection
	p.Format = platformManagedInstallFormat
	if err := platformApplyInstallQuota(&p, p.Format, manifest, platformQuotaApproval{bytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	b, i, err := platformPlanQuota(string(raw))
	if err != nil || b != 1<<30 || i != 100000 {
		t.Fatalf("plan quota %d %d %v", b, i, err)
	}
	if _, _, err := platformPlanQuota(`{"format":"` + platformManagedInstallFormat + `","data_quota_bytes":12345}`); err == nil {
		t.Fatal("an invalid approved quota was read from a plan")
	}
}
