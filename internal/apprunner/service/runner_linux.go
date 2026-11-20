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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
	"golang.org/x/sys/unix"
)

// hostInterfaces is the interface source of the startup refusal; tests replace it.
var hostInterfaces InterfaceLister = SystemInterfaces

// checkDaemonNamespaces is the fail-fast namespace check (doc 187 section 7); tests replace it.
var checkDaemonNamespaces = driver.CheckDaemonNamespaces

var ErrUnsupported = errors.New("app-runner service requires native Linux amd64 or arm64")

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
	// An allowlisted prefix must never reach this host: refuse before anything is opened.
	if err := CheckPrivateNetworksAgainstHost(cfg.PrivateNetworks, hostInterfaces); err != nil {
		logger.Printf("app-runner not ready: private_networks overlap a host interface or bridge, or the interfaces could not be read")
		return ErrConfig
	}
	store, err := journal.Open(cfg.JournalDir)
	if err != nil {
		if errors.Is(err, journal.ErrInUse) {
			logger.Printf("app-runner not ready: in use: another runner or host-setup holds the journal")
		}
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
	if err := requireXFSRoots(logger, cfg); err != nil {
		return err
	}
	rootFile, err := openRootDirectory(cfg.DataRoot)
	if err != nil {
		return ErrNotReady
	}
	defer rootFile.Close()
	root, err := data.Open(rootFile)
	if err != nil {
		logger.Printf("app-runner not ready: data_root did not pass the data-root checks (XFS mounted prjquota,nodev,nosuid,noexec; root-owned mode 0700; project quota enforcement on)")
		return ErrNotReady
	}
	defer root.Close()
	pin, err := root.Identity(ctx)
	if err != nil {
		return ErrNotReady
	}
	checkpointFile, err := openRootDirectory(cfg.CheckpointRoot)
	if err != nil {
		return ErrNotReady
	}
	defer checkpointFile.Close()
	checkpointRoot, err := data.Open(checkpointFile)
	if err != nil {
		logger.Printf("app-runner not ready: checkpoint_root did not pass the data-root checks (XFS mounted prjquota,nodev,nosuid,noexec; root-owned mode 0700; project quota enforcement on)")
		return ErrNotReady
	}
	defer checkpointRoot.Close()
	checkpointPin, err := checkpointRoot.Identity(ctx)
	if err != nil || checkpointPin == pin {
		return ErrNotReady
	}
	report, err := driver.InspectDaemon(ctx, cfg.DockerSocket)
	if err != nil || !report.MetadataCompatible || report.ReportedCPUs < cfg.MinimumCPUs || report.ReportedMemoryBytes < cfg.MinimumMemory {
		return ErrNotReady
	}
	// The runner and dockerd must share a mount namespace and a network namespace (doc 187 section 2). Checked
	// before anything is created or bound, so a runner started without the container entry, or beside another
	// daemon, fails here and not in the first operation that needs the guarantee.
	if err := checkDaemonNamespaces(ctx, cfg.DockerSocket); err != nil {
		logger.Printf("app-runner not ready: namespaces: the container daemon is not identified as a local dockerd in the runner's mount and network namespaces (a Compose runner starts through its entry with pid: host and network_mode: host)")
		return ErrNotReady
	}
	// The IPC root lives on /run and does not survive a reboot: serve makes it (doc 187 section 7).
	if err := ensureIPCRoot(cfg.IPCRoot, rootIdentity); err != nil {
		logger.Printf("app-runner not ready: ipc_root: %v", err)
		return ErrNotReady
	}
	// A pinned proxy destination is an address of the runner's own network namespace (doc 186 section 3): with
	// proxy_pins set, the daemon must be identified in that namespace, or the runner does not start. An
	// unreadable namespace file counts as another namespace.
	if err := CheckProxyPinsNamespace(ctx, cfg.ProxyPins, func(ctx context.Context) error {
		return driver.CheckHostNetworkNamespace(ctx, cfg.DockerSocket)
	}); err != nil {
		logger.Printf("app-runner not ready: proxy_pins_netns: proxy_pins are set but the container daemon is not identified as a local dockerd in the runner's network namespace (a Compose runner needs network_mode: host and pid: host)")
		return err
	}
	// The declared capacity envelope turns percentages of the host into absolute budgets. A fact that cannot be
	// read, a volume smaller than the disk budget, or a budget below one minimum backend is not ready.
	// A reserved CPU needs the online list, the runner's own allowed set and a daemon that can pin containers; a
	// reading that fails is left empty and, when a reservation depends on it, makes the runner not ready.
	facts := HostFacts{VolumeBytes: volumeBytes(cfg.DataRoot), CheckpointSeparate: !sameFilesystem(cfg.DataRoot, cfg.CheckpointRoot), CheckpointVolumeBytes: volumeBytes(cfg.CheckpointRoot)}
	facts.OnlineCPUs, _ = onlineCPUs()
	facts.AffinityCPUs, _ = allowedCPUs()
	budgets, options, err := planCapacity(cfg, report, facts)
	if err != nil {
		var notReady *NotReadyError
		if errors.As(err, &notReady) {
			logger.Printf("app-runner not ready: capacity: %s", notReady.Reason)
		}
		return ErrNotReady
	}
	if err := store.SetCapacityBudgets(budgets); err != nil {
		return ErrNotReady
	}
	// The scratch is opened first: it removes any blobs an earlier run left, before anything can read them.
	images, err := imagestore.Open(cfg.ImageRoot)
	if err != nil {
		return ErrNotReady
	}
	if cfg.AllowLocalReferenceImages {
		options = append(options, driver.WithLocalReferenceImages())
	}
	if len(cfg.ProxyPins) > 0 {
		// The same requirement is applied to every connection the runtime's daemon client makes.
		options = append(options, driver.WithHostNetworkNamespace())
	}
	native, err := driver.OpenCandidateRuntime(ctx, cfg.DockerSocket, cfg.DataRoot, cfg.IPCRoot, root, pin, options...)
	if err != nil {
		return ErrNotReady
	}
	defer native.Close()
	// Nothing is admitted before the upgrade's estimated reservations are refined from the live containers: a
	// daemon that cannot answer leaves the runner not ready, an unobservable row keeps its fail-closed estimate.
	refined, err := store.RefineEstimatedReservations(ctx, native)
	if err != nil {
		logger.Printf("app-runner not ready: capacity: refine estimated reservations: %v", err)
		return ErrNotReady
	}
	estimated, err := store.EstimatedReservations(ctx)
	if err != nil {
		return ErrNotReady
	}
	// A socket left by a runner that was killed would be refused by the listener. The journal's owner lock is held
	// here, so no live runner can own it; only a socket of exactly the runner's shape is removed.
	if err := transport.RemoveStaleControlSocket(cfg.ControlSocket, cfg.ControllerGID, store); err != nil {
		logger.Printf("app-runner not ready: control_socket: something other than a runner's own stale socket is at the configured path, or its directory is not root:controller-gid 0750; it is left in place")
		return ErrNotReady
	}
	server, err := transport.ListenNetworkManagedController(ctx, cfg.ControlSocket, cfg.ControllerUID, cfg.ControllerGID, store, root, pin, checkpointRoot, checkpointPin, native, images, cfg.NetworkActive, cfg.NetworkEntries, transport.WithPrivateNetworks(cfg.PrivateNetworks), transport.WithProxyPins(cfg.ProxyPins))
	if err != nil {
		return ErrNotReady
	}
	defer server.Close()
	// Files a checkpoint removal moved aside as unrecognised stay on disk for an operator: say so at every start.
	if quarantined, err := checkpointRoot.QuarantineSummary(ctx, checkpointPin); err != nil {
		logger.Printf("app-runner checkpoint quarantine: not read: %v", err)
	} else if quarantined.Files > 0 {
		logger.Printf("app-runner checkpoint quarantine: %d file(s), %d bytes, moved aside by a checkpoint removal and never deleted by the runner", quarantined.Files, quarantined.Bytes)
	}
	logger.Printf("app-runner ready profile=%s platform=linux/%s images=%s capacity=%s refined_rows=%d estimated_rows=%d version=%s commit=%s", driver.Profile, runtime.GOARCH, imageProfile(cfg), FormatBudgets(budgets), refined, estimated, version.Display(), version.Commit)
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

func imageProfile(cfg Config) string {
	if cfg.AllowLocalReferenceImages {
		return "development"
	}
	return "production"
}
