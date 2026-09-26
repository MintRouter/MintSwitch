package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// utf8BOM is the UTF-8 byte-order mark some Windows editors (older Notepad,
// PowerShell 5's Out-File -Encoding utf8) prepend. encoding/json rejects it,
// so ReadJSONObject strips it; the file is written back without one.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ReadJSONObject reads path as a JSON object. A missing or empty file yields an
// empty object so callers can merge without special-casing first-run.
func ReadJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// WriteJSONObjectAtomic writes m as indented JSON to path via a sibling temp
// file + rename, creating parent dirs. The file carries 0600 perms since it may
// contain an auth token.
func WriteJSONObjectAtomic(path string, m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return WriteFileAtomic(path, data, 0o600)
}

// WriteFileAtomic writes data to path atomically: it creates parent
// directories (0700), writes to a sibling temp file with the given perm,
// fsyncs, renames over path (os.Rename replaces an existing file on every
// supported OS), then best-effort fsyncs the parent directory so the rename
// itself survives a power loss. A crash mid-write can never leave a truncated
// config behind.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mintswitch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameReplace(tmpName, path); err != nil {
		return err
	}
	SyncDir(dir)
	return nil
}

// renameRetries and renameRetryDelay bound how long renameReplace retries a
// transiently failing rename (about 0.5s in total).
const (
	renameRetries    = 5
	renameRetryDelay = 100 * time.Millisecond
)

// renameReplace renames src over dst, retrying briefly while
// renameRetryable reports the error as transient. On Windows, replacing a
// file fails with "Access is denied" / a sharing violation while another
// process (antivirus, the search indexer, a sync client, an editor) holds dst
// open without delete sharing; such locks are usually released within
// milliseconds. Elsewhere renameRetryable is always false, so this is a plain
// os.Rename.
func renameReplace(src, dst string) error {
	err := os.Rename(src, dst)
	for i := 0; i < renameRetries && err != nil && renameRetryable(err); i++ {
		time.Sleep(renameRetryDelay)
		err = os.Rename(src, dst)
	}
	return err
}

// SyncDir fsyncs the directory so a rename inside it survives a power loss.
// It is deliberately best-effort: the data itself was already fsynced, and on
// some platforms (notably Windows) opening or syncing a directory fails even
// though the rename is durable, so an error here must not fail an otherwise
// successful write.
func SyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// AsJSONObject returns v as a JSON object, or a fresh object when v is not one.
func AsJSONObject(v any) map[string]any {
	if obj, ok := v.(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}
