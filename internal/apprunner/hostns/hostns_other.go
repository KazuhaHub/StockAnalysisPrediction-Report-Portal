//go:build !linux

package hostns

// ConsumeExecFD is Linux-only. Off Linux the variable is never set by the entry, so there is nothing to do.
func ConsumeExecFD() error { return nil }

// RequireHostNamespaces is Linux-only.
func RequireHostNamespaces(string) error { return ErrNamespace }
