//go:build !linux || (!amd64 && !arm64)

package data

import (
	"context"
	"os"
)

type Root struct{}

func openRoot(*os.File) (*Root, error) { return nil, ErrUnsupported }
func (*Root) Close() error             { return nil }
func (*Root) Prepare(context.Context, Request) (Observation, error) {
	return Observation{}, ErrUnsupported
}
func (*Root) Inspect(context.Context, string, uint32) (Observation, error) {
	return Observation{}, ErrUnsupported
}
