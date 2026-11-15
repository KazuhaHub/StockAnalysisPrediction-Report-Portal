//go:build !linux

package driver

// Device and inode identity of a mount source is read with Linux stat semantics, and the runner's inventory
// exists only on Linux. Anywhere else the answer is that an overlap cannot be ruled out: it reports a
// reference and an unsupported error, so a caller that is ever built here refuses instead of passing.
func candidateIPCIdentityOverlap(namespace, source string) (bool, error) {
	return true, ErrUnsupported
}
