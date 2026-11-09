package driver

type runtimeOptions struct{ imagePolicy ImagePolicy }

// RuntimeOption configures OpenCandidateRuntime. The zero configuration is the production profile.
type RuntimeOption func(*runtimeOptions)

// WithLocalReferenceImages selects the development profile: a descriptor's registry reference is inspected in the
// local daemon for an image already present. Production never sets it, and nothing is ever pulled either way.
func WithLocalReferenceImages() RuntimeOption {
	return func(o *runtimeOptions) { o.imagePolicy.AllowLocalReference = true }
}
