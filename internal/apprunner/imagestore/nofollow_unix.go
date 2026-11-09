//go:build unix

package imagestore

import "syscall"

const noFollow = syscall.O_NOFOLLOW
