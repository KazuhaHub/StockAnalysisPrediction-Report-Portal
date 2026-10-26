//go:build !linux || (!amd64 && !arm64)

package data

import (
	"context"
	"os"
)

type Root struct{}

func openRoot(*os.File) (*Root, error) { return nil, ErrUnsupported }
func (*Root) Close() error             { return nil }
func (*Root) Prepare(context.Context, RootIdentity, Request) (Observation, error) {
	return Observation{}, ErrUnsupported
}
func (*Root) Inspect(context.Context, RootIdentity, string, uint32) (Observation, error) {
	return Observation{}, ErrUnsupported
}

func (*Root) Identity(context.Context) (RootIdentity, error) { return RootIdentity{}, ErrUnsupported }
