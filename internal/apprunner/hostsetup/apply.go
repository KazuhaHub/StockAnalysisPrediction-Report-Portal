package hostsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
)

// Defaults of the operator-owned fields of a new service.json. They are the shipped example's values; from then
// on they are the operator's, and a later run never reads or rewrites them.
// PortalSocketPath is where the control socket appears inside the Portal container: the target of the compose
// file's read-only bind of RP_APP_RUNNER_CONTROL_DIR.
const PortalSocketPath = "/var/lib/portal-app-runner/control/runner.sock"

const (
	defaultNetworkActive = 4
	// The broker's pools hold at most 32 executors (broker.MaxNetworkPoolEntries), and a runner whose
	// network_entries is above that fails to start although LoadConfig accepts up to 4096. The shipped example
	// used to say 1024, which a real start refuses.
	defaultNetworkEntries  = 32
	defaultMinimumCPUs     = 2
	defaultMinimumMemory   = int64(2) << 30
	configFileMode         = 0o600
	configValidationSuffix = ".portal-app-runner.new"
)

// measure reads the usable size of each volume root that exists. It must run before the data-root lock is
// taken, because the runner's own checks lock the root. A root held by a runner is skipped.
func (r *run) measure(ctx context.Context) {
	for root, into := range map[string]*int64{r.cfg.dataRoot: &r.usableData, r.cfg.checkpointRoot: &r.usableCheckpoint} {
		if !isDir(r.env.path(root)) {
			continue
		}
		if verified, err := r.env.Provision.Verify(ctx, root); err == nil {
			*into = verified.UsableBytes
		}
	}
}

// writeService writes service.json for a new host from the defaults, the provisioned roots and provision's
// suggested disk_bytes. It never replaces an existing file.
func (r *run) writeService(ctx context.Context, doc *enrollment) error {
	if r.usableData == 0 || r.usableCheckpoint == 0 {
		r.measure(ctx)
	}
	disk := provision.RecommendedDiskBytes(r.usableData, r.usableCheckpoint)
	if disk == 0 {
		return errors.New("the volumes are too small for any capacity.disk_bytes of at least 1 GiB, or their size could not be read")
	}
	c := r.cfg
	cfg := service.Config{
		Format: service.ConfigFormat, DockerSocket: c.dockerSocket, JournalDir: c.journalDir, DataRoot: c.dataRoot, CheckpointRoot: c.checkpointRoot,
		ImageRoot: c.imageRoot, IPCRoot: c.ipcRoot, ControlRoot: c.controlRoot, ControlSocket: c.controlSocket,
		ControllerUID: c.uid, ControllerGID: c.gid, NetworkActive: defaultNetworkActive, NetworkEntries: defaultNetworkEntries,
		MinimumCPUs: defaultMinimumCPUs, MinimumMemory: defaultMinimumMemory, Controller: doc.doc.Controller,
		Capacity: &service.CapacityConfig{DiskBytes: &disk},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return r.writeConfigFile(r.servicePath(), append(raw, '\n'), func(path string) error {
		_, err := service.LoadConfig(path)
		return err
	})
}

func (r *run) writeBootstrap(doc *enrollment) error {
	cfg := doc.doc
	cfg.JournalDir = r.cfg.journalDir
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return r.writeConfigFile(r.bootstrapPath(), append(raw, '\n'), func(path string) error {
		_, err := service.LoadBootstrapConfig(path)
		return err
	})
}

// writeConfigFile writes root-owned 0600 content beside its destination, validates it with the runner's own
// loader, and links it into place without replacing a file that appeared meanwhile.
func (r *run) writeConfigFile(logical string, content []byte, validate func(path string) error) error {
	final := r.env.path(logical)
	temp := final + configValidationSuffix
	os.Remove(temp)
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, configFileMode)
	if err != nil {
		return err
	}
	cleanup := func() { os.Remove(temp) }
	if err := file.Chown(int(r.env.physicalUID(0)), int(r.env.physicalGID(0))); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Chmod(configFileMode); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	if err := validate(temp); err != nil {
		cleanup()
		return fmt.Errorf("the file host-setup generated does not load: %w", err)
	}
	if err := os.Link(temp, final); err != nil {
		cleanup()
		return fmt.Errorf("%s: %w", logical, err)
	}
	cleanup()
	syncDir(filepath.Dir(final))
	return nil
}

// report prints the .env lines. host-setup never writes them: the compose project directory is not known to the
// runner, and it never writes Portal's files.
func (r *run) report() {
	c := r.cfg
	r.printf("\nAdd these lines to the .env file of the compose project (host-setup does not write it):\n\n")
	r.printf("COMPOSE_PROFILES=app-runner\n")
	if r.opts.Release != "" {
		r.printf("PORTAL_VERSION=%s\n", r.opts.Release)
	} else {
		r.printf("# PORTAL_VERSION=<the release tag of the Portal and app-runner images, for example v2026.47.1>\n")
	}
	r.printf("PORTAL_UID=%d\nPORTAL_GID=%d\n", c.uid, c.gid)
	// Portal sees the control directory at a fixed path inside its container (the compose file's bind target), so
	// its socket variable names that path whatever the host path is.
	r.printf("RP_APP_RUNNER_CONTROL_DIR=%s\nRP_APP_RUNNER_SOCKET=%s\n", c.controlRoot, PortalSocketPath)
	r.printf("RP_APP_RUNNER_CONTROLLER_UID=%d\nRP_APP_RUNNER_CONTROLLER_GID=%d\n", c.uid, c.gid)
}
