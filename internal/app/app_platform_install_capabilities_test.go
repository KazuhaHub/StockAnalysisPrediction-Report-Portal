package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformNetworkPolicyFixture(t *testing.T) string {
	t.Helper()
	raw, err := appplatform.CanonicalHTTPPolicy(appplatform.HTTPPolicy{Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"GET"}, RequestBytes: 1024, ResponseBytes: 4096, Deadline: time.Second, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func platformNetworkTargetFixture(t *testing.T) *platformManagedInstallTarget {
	return &platformManagedInstallTarget{NetworkPolicy: platformNetworkPolicyFixture(t), NetworkProfile: transport.NetworkRuntimeProfile{Format: transport.NetworkRuntimeFormat, RequestFormat: control.NetworkHTTPRequestFormat, ConfirmationFormat: control.NetworkHTTPCheckFormat, PolicyFormat: appplatform.HTTPPolicyFormat, MaxRequestBytes: appplatform.MaxHTTPRequestBytes, MaxResponseBytes: appplatform.MaxHTTPResponseBytes, MaxDeadlineMS: appplatform.MaxHTTPDeadlineMS, MaxConcurrency: appplatform.MaxHTTPConcurrency, ActiveRequests: 2, CacheEntries: 3, WritableBackend: true}}
}
func TestPlatformInstallCapabilitySelection(t *testing.T) {
	raw, err := os.ReadFile("../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	m := parsed.Manifest
	m.Capabilities.Required = []string{"network.http"}
	m.Capabilities.Optional = []string{"storage.files", "notifications.publish"}
	target := platformNetworkTargetFixture(t)
	selection, err := platformInstallCapabilitySelection(m, target)
	if err != nil || len(selection) != 3 {
		t.Fatal("required network selection", err)
	}
	if selection[0].capability != "network.http" || !selection[0].granted || selection[0].policy != target.NetworkPolicy || selection[1].granted || selection[1].policy != "{}" {
		t.Fatal("wrong exact selected/denied restrictions")
	}
	if _, err := platformInstallCapabilitySelection(m, nil); err == nil {
		t.Fatal("required provider absent")
	}
	m.Capabilities.Required = []string{}
	m.Capabilities.Optional = []string{"network.http", "storage.files"}
	selected, err := platformInstallCapabilitySelection(m, target)
	if err != nil || !selected[0].granted {
		t.Fatal("explicit optional selection refused", err)
	}
	declined, err := platformInstallCapabilitySelection(m, nil)
	if err != nil || declined[0].granted || declined[0].policy != "{}" {
		t.Fatal("optional implicitly granted", err)
	}
	for _, mode := range []string{"undeclared", "unsupported_required", "duplicate", "bad_policy", "profile", "missing_profile"} {
		t.Run(mode, func(t *testing.T) {
			copyM := m
			copyTarget := *target
			switch mode {
			case "undeclared":
				copyM.Capabilities.Optional = []string{"storage.files"}
			case "unsupported_required":
				copyM.Capabilities.Required = []string{"secrets.use"}
			case "duplicate":
				copyM.Capabilities.Required = []string{"network.http"}
			case "bad_policy":
				copyTarget.NetworkPolicy += " "
			case "profile":
				copyTarget.NetworkProfile.MaxConcurrency++
			case "missing_profile":
				copyTarget.NetworkProfile = transport.NetworkRuntimeProfile{}
			}
			if _, err := platformInstallCapabilitySelection(copyM, &copyTarget); err == nil {
				t.Fatal("invalid capability selection accepted")
			}
		})
	}
	// Omitting the new restrictions preserves the original development review JSON.
	baseline, _ := json.Marshal(platformManagedInstallTarget{})
	if strings.Contains(string(baseline), "network_") {
		t.Fatal("empty network metadata changed legacy reviews")
	}
}

// This unit fixture proves exact row matching only. Actual signed review,
// native observation and execution history are exercised by Linux acceptance.
func testPlatformInstallCapabilityRows(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedPendingControl(t, st)
	var m appplatform.Manifest
	m.Capabilities.Required = []string{"network.http"}
	m.Capabilities.Optional = []string{"storage.files"}
	p := platformInstallProjection{Runtime: platformNetworkTargetFixture(t), DeclinedCapabilities: []string{"storage.files"}}
	for _, c := range []platformSelectedCapability{{"network.http", true, p.Runtime.NetworkPolicy}, {"storage.files", false, "{}"}} {
		mustExec(t, st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) VALUES(?,?,?,?,1,?,1,1)`, platformFixtureID(1), c.capability, boolInt(c.granted), c.policy, platformFixtureID(2))
	}
	if err := verifyPlatformInstallCapabilityRows(st.poolExec(), platformFixtureID(1), m, p, true, 1, platformFixtureID(2), errAppPlatformStageEvidence); err != nil {
		t.Fatal("exact selected source rows refused", err)
	}
	mustExec(t, st, `UPDATE app_platform_capability_grants SET granted=0 WHERE capability='network.http'`)
	if err := verifyPlatformInstallCapabilityRows(st.poolExec(), platformFixtureID(1), m, p, false, 1, platformFixtureID(2), errAppPlatformStageEvidence); err != nil {
		t.Fatal("pending reviewed restriction refused", err)
	}
	if err := verifyPlatformInstallCapabilityRows(st.poolExec(), platformFixtureID(1), m, p, true, 1, platformFixtureID(2), errAppPlatformStageEvidence); err == nil {
		t.Fatal("pending permission became active")
	}
	mustExec(t, st, `UPDATE app_platform_capability_grants SET granted=1 WHERE capability='network.http'`)
	for _, q := range []string{
		`UPDATE app_platform_capability_grants SET granted=0 WHERE capability='network.http'`,
		`UPDATE app_platform_capability_grants SET policy_json=policy_json||' ' WHERE capability='network.http'`,
		`UPDATE app_platform_capability_grants SET policy_revision=2`,
		`UPDATE app_platform_capability_grants SET review_plan_id='00000000-0000-4000-8000-000000009999'`,
		`UPDATE app_platform_capability_grants SET granted=1 WHERE capability='storage.files'`,
		`DELETE FROM app_platform_capability_grants WHERE capability='network.http'`,
		`UPDATE app_platform_capability_grants SET capability='secrets.use' WHERE capability='storage.files'`,
	} {
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(st.bind(q)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		err = verifyPlatformInstallCapabilityRows(migExec{s: st, ex: tx}, platformFixtureID(1), m, p, true, 1, platformFixtureID(2), errAppPlatformStageEvidence)
		tx.Rollback()
		if err == nil {
			t.Fatal("inexact grant row admitted", q)
		}
	}
}
func TestPlatformInstallCapabilityRowsSQLite(t *testing.T) {
	testPlatformInstallCapabilityRows(t, false)
}
func TestPlatformInstallCapabilityRowsPostgres(t *testing.T) {
	testPlatformInstallCapabilityRows(t, true)
}
