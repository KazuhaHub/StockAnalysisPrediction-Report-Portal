//go:build !linux && !darwin

package journal

import "context"

type directoryGuard struct{}

func lockDirectory(string) (*directoryGuard, error) { return nil, ErrUnsupported }
func (*directoryGuard) prepareDatabase() error      { return ErrUnsupported }
func (*directoryGuard) checkFiles() error           { return ErrUnsupported }
func (*directoryGuard) sync() error                 { return ErrUnsupported }
func (*directoryGuard) close() error                { return nil }

// ReadControllerDeployment needs the owner and link checks of files_unix.go, which this platform lacks.
func ReadControllerDeployment(context.Context, string) (Deployment, error) {
	return Deployment{}, ErrUnsupported
}
