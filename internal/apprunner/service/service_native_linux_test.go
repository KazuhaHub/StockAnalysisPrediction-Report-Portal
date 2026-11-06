//go:build linux && (amd64 || arm64)

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestNativeManagedServiceProfile(t *testing.T) {
	if os.Getenv("PORTAL_APP_RUNNER_SERVICE_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable managed service acceptance")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("managed service acceptance requires a disposable GitHub-hosted worker")
	}
	uid, err := parseUint32Env("PORTAL_APP_RUNNER_SERVICE_UID")
	if err != nil || os.Geteuid() != int(uid) {
		t.Fatal("managed service acceptance must run as the controller")
	}
	gid, err := parseUint32Env("PORTAL_APP_RUNNER_SERVICE_GID")
	if err != nil {
		t.Fatal(err)
	}
	public, err := base64.RawURLEncoding.DecodeString(os.Getenv("PORTAL_APP_RUNNER_SERVICE_PUBLIC_KEY"))
	if err != nil {
		t.Fatal("invalid acceptance controller key")
	}
	authority := control.Authority{
		DeploymentID:    os.Getenv("PORTAL_APP_RUNNER_SERVICE_DEPLOYMENT_ID"),
		ControllerEpoch: os.Getenv("PORTAL_APP_RUNNER_SERVICE_CONTROLLER_EPOCH"),
		ControlRevision: 1,
		PublicKey:       public,
		Fingerprint:     os.Getenv("PORTAL_APP_RUNNER_SERVICE_FINGERPRINT"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observed, err := transport.ObserveControllerManagedRuntime(ctx, os.Getenv("PORTAL_APP_RUNNER_SERVICE_SOCKET"), uid, gid, authority)
	if err != nil {
		t.Fatal(err)
	}
	profile := observed.Profile()
	if profile.ExecutionReady || profile.Protocol != transport.ManagedRunnerProtocol ||
		profile.BackendProtocol != transport.ManagedBackendProtocol || profile.Platform != "linux/"+os.Getenv("GOARCH") ||
		len(profile.RootFilesystemUUID) != 36 || profile.RootInode == "" || !bytes.Equal(observed.Controller().PublicKey, public) {
		t.Fatal("managed service returned an unexpected profile")
	}
	t.Log("NATIVE_MANAGED_SERVICE_PROFILE_VERIFIED")
}

func parseUint32Env(name string) (uint32, error) {
	parsed, err := strconv.ParseUint(os.Getenv(name), 10, 32)
	if err != nil || parsed == 0 {
		return 0, ErrConfig
	}
	return uint32(parsed), nil
}
