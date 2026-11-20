//go:build !linux

package provision

// SystemEnv is unavailable off Linux.
func SystemEnv() (Env, error) { return Env{}, ErrUnsupported }
