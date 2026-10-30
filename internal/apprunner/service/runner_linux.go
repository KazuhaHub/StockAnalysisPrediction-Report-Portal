//go:build linux && (amd64 || arm64)

package service

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"runtime"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
	"golang.org/x/sys/unix"
)

var ErrUnsupported = errors.New("app-runner service requires native Linux amd64 or arm64")
var ErrNotReady = errors.New("app-runner runtime is not ready")

// Serve owns one already-provisioned runtime. Configuration never selects an
// arbitrary command, mount, app, image or controller key.
func Serve(ctx context.Context, configPath string, logger *log.Logger) error {
	if ctx == nil || logger == nil || os.Geteuid() != 0 || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return ErrNotReady
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return ErrConfig
	}
	store, err := journal.Open(cfg.JournalDir)
	if err != nil {
		return ErrNotReady
	}
	defer store.Close()
	deployment, err := store.ControllerDeployment(ctx)
	expectedKey, keyErr := deploymentPublicKey(ctx, store, cfg.Controller)
	if err != nil || keyErr != nil || deployment.DeploymentID != cfg.Controller.DeploymentID ||
		deployment.ControllerEpoch != cfg.Controller.ControllerEpoch ||
		deployment.ControlRevision != cfg.Controller.ControlRevision ||
		deployment.Fingerprint != cfg.Controller.Fingerprint || deployment.Revision < 1 || len(expectedKey) != 32 {
		return ErrNotReady
	}
	rootFile, err := openRootDirectory(cfg.DataRoot)
	if err != nil {
		return ErrNotReady
	}
	defer rootFile.Close()
	root, err := data.Open(rootFile)
	if err != nil {
		return ErrNotReady
	}
	defer root.Close()
	pin, err := root.Identity(ctx)
	if err != nil {
		return ErrNotReady
	}
	report, err := driver.InspectDaemon(ctx, cfg.DockerSocket)
	if err != nil || !report.MetadataCompatible || report.ReportedCPUs < cfg.MinimumCPUs || report.ReportedMemoryBytes < cfg.MinimumMemory {
		return ErrNotReady
	}
	native, err := driver.OpenCandidateRuntime(ctx, cfg.DockerSocket, cfg.DataRoot, cfg.SocketRoot, root, pin)
	if err != nil {
		return ErrNotReady
	}
	defer native.Close()
	server, err := transport.ListenNetworkManagedController(ctx, cfg.ControlSocket, cfg.ControllerUID, cfg.ControllerGID, store, root, pin, native, cfg.NetworkActive, cfg.NetworkEntries)
	if err != nil {
		return ErrNotReady
	}
	defer server.Close()
	logger.Printf("app-runner ready profile=%s platform=linux/%s", driver.Profile, runtime.GOARCH)
	if err := server.Serve(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return ErrNotReady
	}
	return nil
}

func deploymentPublicKey(ctx context.Context, store *journal.Store, expected Controller) ([]byte, error) {
	deployment, err := store.ControllerDeployment(ctx)
	if err != nil || deployment.DeploymentID != expected.DeploymentID || deployment.ControllerEpoch != expected.ControllerEpoch ||
		deployment.ControlRevision != expected.ControlRevision || deployment.Fingerprint != expected.Fingerprint {
		return nil, ErrNotReady
	}
	return bytes.Clone(deployment.PublicKey), nil
}

func openRootDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrNotReady
	}
	return os.NewFile(uintptr(fd), "app-runner-data-root"), nil
}
