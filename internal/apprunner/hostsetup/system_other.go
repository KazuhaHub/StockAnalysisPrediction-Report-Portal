//go:build !linux

package hostsetup

import "errors"

// NewSystemEnv is Linux-only.
func NewSystemEnv() (Env, error) { return Env{}, errors.New("host-setup requires Linux") }
