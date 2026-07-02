package backup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RestoreLatest restores path from its most recent backup entry.
//
//   - If the latest entry is a content backup, path is overwritten with the
//     backed-up bytes (parent directories are created as needed), restoring it
//     byte-for-byte.
//   - If the latest entry is an "absent" marker, path is removed (it did not
//     exist before Apply), making restore the inverse of an adapter creating it.
//   - If no backup exists, RestoreLatest is a safe no-op.
//
// It returns restored=true when it changed the filesystem (content restored or
// file removed) and the entry path that was used.
func (e *Engine) RestoreLatest(path string) (restored bool, entry string, err error) {
	dir := e.dirFor(path)
	entry, err = latest(dir)
	if err != nil {
		return false, "", err
	}
	if entry == "" {
		return false, "", nil
	}
	if err := restoreFrom(path, entry); err != nil {
		return false, entry, err
	}
	return true, entry, nil
}

// RestorePristine restores path from its OLDEST backup entry — the pristine
// pre-MintSwitch snapshot taken on the first modification of an unmanaged
// file. Later entries may have been captured after the file was already
// MintSwitch-managed (e.g. a second component snapshotting the same file
// moments after the first one modified it), so the latest entry can be
// contaminated; the oldest is the true original. The entry semantics match
// [Engine.RestoreLatest]: a content backup overwrites path, an "absent"
// marker removes it, and no backup is a safe no-op.
//
// After a successful restore, every backup entry for path is pruned so a
// contaminated snapshot can never resurface on a later restore.
func (e *Engine) RestorePristine(path string) (restored bool, entry string, err error) {
	dir := e.dirFor(path)
	entry, err = oldest(dir)
	if err != nil {
		return false, "", err
	}
	if entry == "" {
		return false, "", nil
	}
	if err := restoreFrom(path, entry); err != nil {
		return false, entry, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return true, entry, err
	}
	return true, entry, nil
}

// restoreFrom applies a single backup entry to path: an "absent" marker
// removes path (tolerating an already-missing file), a content entry
// overwrites it byte-for-byte, creating parent directories as needed.
func restoreFrom(path, entry string) error {
	if strings.HasSuffix(entry, absentSuffix) {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := os.ReadFile(entry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// HasBackup reports whether any backup entry exists for path.
func (e *Engine) HasBackup(path string) (bool, error) {
	entry, err := latest(e.dirFor(path))
	if err != nil {
		return false, err
	}
	return entry != "", nil
}
