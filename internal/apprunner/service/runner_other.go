//go:build !linux

package service

import (
	"context"
	"errors"
	"log"
)

var ErrUnsupported = errors.New("app-runner service requires native Linux amd64 or arm64")

func Serve(context.Context, string, *log.Logger) error { return ErrUnsupported }
