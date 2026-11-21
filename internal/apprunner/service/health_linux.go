//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// healthTimeout bounds the connect(): a runner that accepts into a full backlog must not hang the check.
const healthTimeout = 2 * time.Second

// Health is the container healthcheck (doc 187 section 6). In the host namespaces it checks that the control
// socket exists as a socket owned 0:<controller_gid> with mode 0660 and that connect() succeeds. The runner
// drops the connection, because the peer is not the controller UID; that is the expected answer. Health takes
// no lock and writes nothing, so it cannot race a starting runner, and nothing depends on it.
func Health(ctx context.Context, configPath string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return ErrConfig
	}
	return checkControlSocket(ctx, cfg.ControlSocket, 0, cfg.ControllerGID)
}

func checkControlSocket(ctx context.Context, path string, uid, gid uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("the control socket does not exist")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o660 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		st.Uid != uid || st.Gid != gid {
		return errors.New("the control socket is not a socket owned root:controller-gid with mode 0660")
	}
	if err := transport.ProbeControlSocket(ctx, path, healthTimeout); err != nil {
		return errors.New("the control socket does not accept connections")
	}
	return nil
}
