//go:build !linux

package packimport

import (
	"context"
	"errors"
)

var ErrExists = errors.New("an archive with that name already exists in the import inbox")

type TreeError struct{ Path, Reason string }

func (e *TreeError) Error() string { return e.Reason }

type Options struct{ From, Out, Packer string }

type Result struct {
	SHA256                       string
	Bytes, Entries, ContentBytes int64
}

// Pack is unsupported off Linux: the runner host is Linux.
func Pack(context.Context, Options) (Result, error) {
	return Result{}, errors.New("pack-import is supported on Linux only")
}
