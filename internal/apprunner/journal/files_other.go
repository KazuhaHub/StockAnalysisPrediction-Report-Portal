//go:build !linux && !darwin

package journal

type directoryGuard struct{}

func lockDirectory(string) (*directoryGuard, error) { return nil, ErrUnsupported }
func (*directoryGuard) prepareDatabase() error      { return ErrUnsupported }
func (*directoryGuard) checkFiles() error           { return ErrUnsupported }
func (*directoryGuard) sync() error                 { return ErrUnsupported }
func (*directoryGuard) close() error                { return nil }
