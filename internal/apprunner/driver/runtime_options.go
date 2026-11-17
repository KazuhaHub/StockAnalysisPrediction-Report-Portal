package driver

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/cpuset"

type runtimeOptions struct {
	imagePolicy ImagePolicy
	appCPUs     string
}

// RuntimeOption configures OpenCandidateRuntime. The zero configuration is the production profile.
type RuntimeOption func(*runtimeOptions)

// WithLocalReferenceImages selects the development profile: a descriptor's registry reference is inspected in the
// local daemon for an image already present. Production never sets it, and nothing is ever pulled either way.
func WithLocalReferenceImages() RuntimeOption {
	return func(o *runtimeOptions) { o.imagePolicy.AllowLocalReference = true }
}

// WithAppCPUSet pins every app container the runtime creates to the given CPU list, in the kernel's list
// notation ("1-3"). The runner derives it from the CPUs it reserves for itself and the Portal. Without the
// option, or with an empty list, containers are not pinned. The runtime is refused when the daemon cannot pin
// containers to CPUs.
func WithAppCPUSet(cpus string) RuntimeOption {
	return func(o *runtimeOptions) { o.appCPUs = cpus }
}

// checkAppCPUSet refuses an app CPU set that is malformed or that the daemon cannot honour. Nothing is asked of
// the daemon when no set is configured.
func checkAppCPUSet(report Report, appCPUs string) error {
	if appCPUs == "" {
		return nil
	}
	if !cpuset.Valid(appCPUs) {
		return ErrInvalidResponse
	}
	if !report.CPUSetSupported {
		return ErrUnsupported
	}
	return nil
}

// RuntimeAppCPUSet is the app CPU list the options configure, or empty when they pin nothing.
func RuntimeAppCPUSet(options ...RuntimeOption) string {
	var configuration runtimeOptions
	for _, option := range options {
		option(&configuration)
	}
	return configuration.appCPUs
}
