package app

import (
	"bytes"
	"testing"
)

// The read path of a consumed capability review: the foreground grant loader and
// the launch/backend snapshot overlay accept a network.http row whose policy is
// backed by the installation's latest consumed capability plan, and nothing else.

func testPlatformCapabilityReadAccepted(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	if err := platformCapabilityPrepare(t, s, actor, installation, "https://api.example.com/v1/x"); err != nil {
		t.Fatal("baseline v1 grant refused:", err)
	}
	if err := platformCapabilityPrepare(t, s, actor, installation, "https://other.example.com/v2/x"); err == nil {
		t.Fatal("unreviewed target accepted before the review")
	}
	id, _ := platformCapabilitySeedPlan(t, s, installation, platformCapabilitySeed{})
	if err := platformCapabilityLaunch(t, s, actor, installation); err != nil {
		t.Fatal("launch overlay refused a valid consumed capability plan:", err)
	}
	for _, url := range []string{"https://api.example.com/v1/x", "https://other.example.com/v2/x"} {
		if err := platformCapabilityPrepare(t, s, actor, installation, url); err != nil {
			t.Fatal("loader refused a valid consumed capability plan:", url, err)
		}
	}
	if err := platformCapabilityPrepare(t, s, actor, installation, "https://third.example.com/v2/x"); err == nil {
		t.Fatal("target outside the reviewed policy accepted")
	}
	if scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation) != id {
		t.Fatal("fixture drifted")
	}
}

func TestPlatformCapabilityReadAcceptedSQLite(t *testing.T) {
	t.Parallel()
	testPlatformCapabilityReadAccepted(t, false)
}
func TestPlatformCapabilityReadAcceptedPostgres(t *testing.T) {
	t.Parallel()
	testPlatformCapabilityReadAccepted(t, true)
}

func testPlatformCapabilityReadRefusals(t *testing.T, pg bool) {
	other := platformFixtureID(9100)
	cases := []struct {
		name string
		// apply builds the state; it may seed plans and raw-edit rows.
		apply func(t *testing.T, s *Server, installation string)
		// launchOK is true for refusals only the loader judges (runtime identity).
		launchOK bool
	}{
		{"unconsumed plan", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{unconsumed: true})
		}, false},
		{"other installation", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{mutate: func(p *platformCapabilityPolicyPlan) { p.InstallationID = other }})
		}, false},
		{"other release", func(t *testing.T, s *Server, i string) {
			release := platformCapabilityOtherRelease(t, s, i)
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{source: release, mutate: func(p *platformCapabilityPolicyPlan) { p.ReleaseSHA256 = release }})
		}, false},
		{"other generation", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{mutate: func(p *platformCapabilityPolicyPlan) { p.Generation = 2 }})
		}, false},
		{"other epoch", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{epoch: other, mutate: func(p *platformCapabilityPolicyPlan) { p.ControllerEpoch = other; p.Runtime.ControllerEpoch = other }})
		}, false},
		{"other capability", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{mutate: func(p *platformCapabilityPolicyPlan) { p.Capability = "network.stream" }})
		}, false},
		{"runtime identity", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{mutate: func(p *platformCapabilityPolicyPlan) {
				p.Runtime.Fingerprint = platformDigest([]byte("another runtime"))
			}})
		}, true},
		{"source release column", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{source: platformCapabilityOtherRelease(t, s, i)})
		}, false},
		{"plan digest", func(t *testing.T, s *Server, i string) {
			id, _ := platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{})
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=? WHERE id=?`, platformDigest([]byte("x")), id)
		}, false},
		{"row policy differs from the plan", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{rowPolicy: platformV2PolicyFixture(t, platformV2DefaultTarget())})
		}, false},
		{"not the latest plan", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{noStamp: true, revision: 5})
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{})
		}, false},
		{"grant withdrawn", func(t *testing.T, s *Server, i string) {
			platformCapabilitySeedPlan(t, s, i, platformCapabilitySeed{})
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, i)
		}, false},
		{"raw policy update, no plan", func(t *testing.T, s *Server, i string) {
			policy := platformV2PolicyFixture(t, platformV2DefaultTarget(), platformV2ExtraTarget())
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.http'`, policy, i)
		}, false},
		{"raw review id swap to an unrelated plan", func(t *testing.T, s *Server, i string) {
			const columns = "installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms"
			mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,`+columns+`) SELECT ?,`+columns+` FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE installation_id=? AND action='install')`, other, i)
			policy := platformV2PolicyFixture(t, platformV2DefaultTarget(), platformV2ExtraTarget())
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=?,review_plan_id=? WHERE installation_id=? AND capability='network.http'`, policy, other, i)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, actor, installation := platformCapabilityFixture(t, pg)
			tc.apply(t, s, installation)
			before := dumpOf(t, s.st)
			if err := platformCapabilityPrepare(t, s, actor, installation, "https://other.example.com/v2/x"); err == nil {
				t.Fatal("loader accepted a grant no valid capability plan backs")
			}
			if err := platformCapabilityPrepare(t, s, actor, installation, "https://api.example.com/v1/x"); err == nil {
				t.Fatal("loader accepted a grant no valid capability plan backs (default target)")
			}
			err := platformCapabilityLaunch(t, s, actor, installation)
			if tc.launchOK && err != nil {
				t.Fatal("launch refused for a loader-only condition:", err)
			}
			if !tc.launchOK && err == nil {
				t.Fatal("launch overlay accepted a row no valid capability plan backs")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal mutated storage")
			}
		})
	}
}

func TestPlatformCapabilityReadRefusalsSQLite(t *testing.T) {
	t.Parallel()
	testPlatformCapabilityReadRefusals(t, false)
}
func TestPlatformCapabilityReadRefusalsPostgres(t *testing.T) {
	t.Parallel()
	testPlatformCapabilityReadRefusals(t, true)
}
