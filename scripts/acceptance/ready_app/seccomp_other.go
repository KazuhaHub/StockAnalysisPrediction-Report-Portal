//go:build !linux

package main

import "errors"

func verifyFixtureSeccomp() error {
	return errors.New("native fixture requires Linux seccomp")
}

func verifyFixtureWritableData() error {
	return errors.New("native fixture write probe requires Linux")
}

func fixtureHasWritableData() bool { return false }
