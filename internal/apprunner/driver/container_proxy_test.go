package driver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// proxyContainerEnv is the Env of the container planned for a managed descriptor that does or
// does not declare network.proxy.
func proxyContainerEnv(t *testing.T, declare bool) []string {
	t.Helper()
	raw, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if declare {
		d["capabilities"] = map[string]any{"required": []string{"background.run"}, "optional": []string{"network.proxy"}}
		d["network_proxy"] = map[string]any{"hosts": []string{"qt.gtimg.cn"}}
	}
	raw, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	p := containerPlanFor(t, raw)
	var request struct{ Env []string }
	if err := json.Unmarshal(p.body, &request); err != nil {
		t.Fatal(err)
	}
	return request.Env
}

func TestContainerEnvironmentCarriesTheProxyOnlyWhenDeclared(t *testing.T) {
	declared := strings.Join(proxyContainerEnv(t, true), "\n")
	for _, want := range []string{
		"HTTPS_PROXY=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128",
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost", "PORTAL_APP_PROXY_LISTEN=127.0.0.1:3128",
		"PORTAL_APP_HTTP_SOCKET=/run/portal-app/http/app.sock", "PORTAL_APP_BROKER_SOCKET=/run/portal-app/broker/broker.sock",
	} {
		if !strings.Contains("\n"+declared+"\n", "\n"+want+"\n") {
			t.Errorf("a declaring app's environment lacks %s: %s", want, declared)
		}
	}
	// Plain HTTP stays refused (D5): no HTTP_PROXY of either case, and no ALL_PROXY that would carry it.
	for _, forbidden := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if strings.Contains("\n"+declared, "\n"+forbidden+"=") {
			t.Errorf("%s is set", forbidden)
		}
	}
	plain := strings.Join(proxyContainerEnv(t, false), "\n")
	if strings.Contains(plain, "PROXY") || strings.Contains(plain, "proxy") {
		t.Fatalf("an app that does not declare the capability has proxy variables: %s", plain)
	}
	if got := proxyContainerEnv(t, false); len(got) != 2 {
		t.Fatalf("the environment of an app without the capability changed: %v", got)
	}
}

func containerPlanFor(t *testing.T, raw []byte) containerPlan {
	t.Helper()
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	id := func(s string) string { return s + "0000000-0000-4000-8000-000000000000" }
	owner := containerOwner{DeploymentID: id("1"), ControllerEpoch: id("2"), InstallationID: id("3"), OperationID: id("4"), ResourceID: id("5"), NamespaceID: id("6"), ReleaseSHA256: parsed.SHA256, Generation: "1", ControlFence: "2", WriterFence: "3"}
	reference, ok := backendImageReference(parsed.Manifest.Backend, "linux/amd64")
	if !ok {
		t.Fatal("no image reference")
	}
	image := BackendImageInspection{ReleaseSHA256: parsed.SHA256, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	p, err := planCandidateContainer(raw, image, owner, containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContainerWithTheProxyKeepsNoNetwork(t *testing.T) {
	// The forwarder listens on the container's own loopback, so nothing about the network policy changes.
	raw, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	d["capabilities"] = map[string]any{"required": []string{"background.run"}, "optional": []string{"network.proxy"}}
	d["network_proxy"] = map[string]any{"hosts": []string{"qt.gtimg.cn"}}
	raw, _ = json.Marshal(d)
	var v map[string]any
	if err := json.Unmarshal(containerPlanFor(t, raw).body, &v); err != nil {
		t.Fatal(err)
	}
	if v["NetworkDisabled"] != true || v["HostConfig"].(map[string]any)["NetworkMode"] != "none" {
		t.Fatal("the proxy gave the container a network")
	}
}
