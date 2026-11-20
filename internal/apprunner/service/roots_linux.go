//go:build linux

package service

import (
	"log"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/rootfs"
)

// inspectRoot is the filesystem source of the root check; tests replace it.
var inspectRoot = rootfs.Inspect

// recipeHint points an operator at the supported way to give an ext4 or other host an XFS project-quota root.
const recipeHint = "provision loop-mounted XFS images with `app-runner provision-xfs-images --help` " +
	"(docs/app-platform/182-cross-platform-backend-runtime.md section 3.2 and the handbook section 4.2)"

// requireXFSRoots refuses a runner whose data or checkpoint root is on a filesystem other than XFS, with a message
// that names the cause and the recipe. The data layer would refuse the same root a step later with a generic
// error. A path that cannot be inspected is left to that later check. Messages carry the root's role and the
// filesystem kind, never a configured path. Static Portal apps do not depend on this service.
func requireXFSRoots(logger *log.Logger, cfg Config) error {
	var refused error
	for _, root := range []struct{ role, path string }{{"data_root", cfg.DataRoot}, {"checkpoint_root", cfg.CheckpointRoot}} {
		info, err := inspectRoot(root.path)
		if err != nil {
			continue
		}
		logger.Printf("app-runner %s filesystem: %s", root.role, info.Describe())
		if info.Kind != rootfs.KindXFS {
			logger.Printf("app-runner not ready: %s is on %s, and backend apps need an XFS project-quota volume; "+
				"static apps are unaffected. On this host, %s", root.role, info.Kind, recipeHint)
			refused = ErrNotReady
		}
	}
	return refused
}
