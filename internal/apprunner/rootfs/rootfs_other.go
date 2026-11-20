//go:build !linux

package rootfs

// Inspect is unavailable off Linux.
func Inspect(string) (Info, error) { return Info{}, ErrUnsupported }
