//go:build !linux

package service

import "context"

// Health is Linux-only, like the runner.
func Health(context.Context, string) error { return ErrUnsupported }
