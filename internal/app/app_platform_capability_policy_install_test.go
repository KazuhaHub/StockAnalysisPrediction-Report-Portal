package app

import (
	"context"
	"testing"
)

// An install may carry a v2 policy with public targets only: a private target
// needs the runner allowlist digest, which the install review cannot bind.
func TestPlatformInstallNetworkPolicyV2PublicOnly(t *testing.T) {
	t.Parallel()
	profile := platformNetworkRuntimeProfileFixture()
	public := &platformManagedInstallTarget{NetworkPolicy: platformV2PolicyFixture(t, platformV2DefaultTarget(), platformV2ExtraTarget()), NetworkProfile: profile}
	if http, _, err := platformNetworkTargetPolicies(public); err != nil || http != public.NetworkPolicy {
		t.Fatal("public v2 install policy refused", err)
	}
	private := &platformManagedInstallTarget{NetworkPolicy: platformV2PolicyFixture(t, platformV2DefaultTarget(), platformTestTarget("lan", "https://10.20.0.5:8443", "private", []string{"/"}, []string{"GET"})), NetworkProfile: profile}
	if _, _, err := platformNetworkTargetPolicies(private); err == nil {
		t.Fatal("private target accepted at install")
	}
	s := &Server{}
	if _, err := s.observePlatformNetworkInstallRuntime(context.Background(), platformActorSession{}, platformRuntimeEndpoint{}, private.NetworkPolicy); err == nil {
		t.Fatal("private install policy reached the runtime observation")
	}
}
