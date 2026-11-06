package app

import (
	"errors"
	"testing"
)

func TestPlatformRuntimeEndpointBootstrapIsExplicitAndStrict(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	for name, values := range map[string]map[string]string{
		"partial": {platformRuntimeSocketEnv: "/run/portal/runner.sock"},
		"relative": {
			platformRuntimeSocketEnv: "runner.sock", platformRuntimeUIDEnv: "41001", platformRuntimeGIDEnv: "41001",
		},
		"zero": {
			platformRuntimeSocketEnv: "/run/portal/runner.sock", platformRuntimeUIDEnv: "0", platformRuntimeGIDEnv: "41001",
		},
		"overflow": {
			platformRuntimeSocketEnv: "/run/portal/runner.sock", platformRuntimeUIDEnv: "4294967296", platformRuntimeGIDEnv: "41001",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, configured, err := platformRuntimeEndpointFromEnv(env(values)); !configured || !errors.Is(err, errPlatformRuntimeBootstrap) {
				t.Fatalf("unsafe bootstrap accepted: configured=%t err=%v", configured, err)
			}
		})
	}
	if _, configured, err := platformRuntimeEndpointFromEnv(env(nil)); configured || err != nil {
		t.Fatal("unconfigured runtime did not remain optional")
	}
	endpoint, configured, err := platformRuntimeEndpointFromEnv(env(map[string]string{
		platformRuntimeSocketEnv: "/run/portal/runner.sock", platformRuntimeUIDEnv: "41001", platformRuntimeGIDEnv: "41001",
	}))
	if err != nil || !configured || endpoint.path != "/run/portal/runner.sock" || endpoint.controllerUID != 41001 || endpoint.controllerGID != 41001 {
		t.Fatalf("valid endpoint: %+v configured=%t err=%v", endpoint, configured, err)
	}
}
