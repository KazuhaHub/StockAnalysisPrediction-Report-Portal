//go:build linux

package hostsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostns"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
	"golang.org/x/sys/unix"
)

// toolDirs is where the host's tools are looked up, the same fixed list provision uses. The caller's PATH is
// deliberately not consulted: this runs as root.
var toolDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

type storeJournal struct{ store *journal.Store }

func (s storeJournal) Deployment(ctx context.Context) (journal.Deployment, error) {
	return s.store.ControllerDeployment(ctx)
}

func (s storeJournal) Enroll(ctx context.Context, request journal.ControllerEnrollment) (bool, error) {
	_, created, err := s.store.EnrollController(ctx, request)
	return created, err
}

func (s storeJournal) Close() error { return s.store.Close() }

func readAccountsFile(path string) ([]Account, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return ParseAccounts(file)
}

// NewSystemEnv is the real host, as seen from the host mount namespace.
func NewSystemEnv() (Env, error) {
	penv, err := provision.SystemEnv()
	if err != nil {
		return Env{}, err
	}
	return Env{
		Kernel: func() (string, error) {
			var uts unix.Utsname
			if err := unix.Uname(&uts); err != nil {
				return "", err
			}
			return strings.TrimRight(string(uts.Release[:]), "\x00"), nil
		},
		Namespaces: func() error { return hostns.RequireHostNamespaces("/proc") },
		Docker: func(ctx context.Context, socket string) error {
			report, err := driver.InspectDaemon(ctx, socket)
			if err != nil {
				return err
			}
			if !report.MetadataCompatible {
				return errors.New("the daemon is not a rootful, cgroup v2 daemon with the default seccomp profile and resource limits (app-runner inspect-daemon --socket " + socket + " shows why)")
			}
			return driver.CheckDaemonNamespaces(ctx, socket)
		},
		HasTool: func(name string) bool {
			for _, dir := range toolDirs {
				if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
					return true
				}
			}
			return false
		},
		Accounts: func() (users, groups []Account, err error) {
			if users, err = readAccountsFile("/etc/passwd"); err != nil {
				return nil, nil, err
			}
			groups, err = readAccountsFile("/etc/group")
			return users, groups, err
		},
		LoopBacking: func(device string) (string, error) {
			raw, err := os.ReadFile(filepath.Join("/sys/block", filepath.Base(device), "loop", "backing_file"))
			return strings.TrimSpace(string(raw)), err
		},
		ReadDeployment: journal.ReadControllerDeployment,
		OpenJournal: func(dir string) (Journal, error) {
			store, err := journal.Open(dir)
			if err != nil {
				return nil, err
			}
			return storeJournal{store}, nil
		},
		LockDataRoot: func(dir string) (func(), error) {
			file, err := os.OpenFile(dir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return nil, err
			}
			root, err := data.Open(file)
			if err != nil {
				file.Close()
				return nil, err
			}
			return func() { root.Close(); file.Close() }, nil
		},
		IsDataRootBusy: func(err error) bool { return errors.Is(err, data.ErrConflict) },
		Provision:      penv,
	}, nil
}
