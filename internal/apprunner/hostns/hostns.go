// Package hostns holds the runner's side of the container entry (doc 187 section 5): the namespace comparison
// both the entry and host-setup rely on, and the handling of the descriptor the entry opened on the runner
// binary before it joined the host mount namespace.
package hostns

import "errors"

// ExecFDEnv names the environment variable through which the entry tells the runner which descriptor holds
// its own executable.
const ExecFDEnv = "PORTAL_APP_RUNNER_EXEC_FD"

var (
	// ErrExecFD is a descriptor the entry named that is not the runner's own executable.
	ErrExecFD = errors.New("the entry descriptor is not this executable")
	// ErrNamespace is a runner that is not in the host's mount, network and PID namespaces.
	ErrNamespace = errors.New("the runner is not in the host mount, network and PID namespaces")
)
