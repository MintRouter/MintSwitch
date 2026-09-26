package core

import (
	"errors"
	"syscall"
)

// errorSharingViolation is ERROR_SHARING_VIOLATION (32), not exported by
// package syscall.
const errorSharingViolation syscall.Errno = 32

// renameRetryable reports whether a failed rename is a transient lock held by
// another process: ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION.
func renameRetryable(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errorSharingViolation)
}
