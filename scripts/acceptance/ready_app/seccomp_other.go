//go:build !linux

package main

import "errors"

func verifyFixtureSeccomp() error {
	return errors.New("native fixture requires Linux seccomp")
}
