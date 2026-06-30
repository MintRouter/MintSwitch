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
	if strings.HasSuffix(entry, absentSuffix) {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, entry, err
		}
		return true, entry, nil
	}
	data, err := os.ReadFile(entry)
	if err != nil {
		return false, entry, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, entry, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return false, entry, err
	}
	return true, entry, nil
}

// HasBackup reports whether any backup entry exists for path.
func (e *Engine) HasBackup(path string) (bool, error) {
	entry, err := latest(e.dirFor(path))
	if err != nil {
		return false, err
	}
	return entry != "", nil
}
