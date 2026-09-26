//go:build !windows

package core

// renameRetryable is always false off Windows: rename(2) replaces a file even
// while other processes hold it open, so a failure there is never a
// transient lock.
func renameRetryable(error) bool { return false }
