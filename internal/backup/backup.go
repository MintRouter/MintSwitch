// Package backup provides a generic, reusable backup/restore engine used by
// every tool adapter. It snapshots a config file before Apply and restores it
// on Restore. Backups are timestamped copies kept under MintSwitch's own data
// directory, so the original tool config trees are never used as scratch space.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Suffixes used to distinguish a content backup from an "absent" marker. The
// absent marker records that the source file did not exist before Apply, so
// RestoreLatest knows to delete an adapter-created file rather than recreate it.
const (
	contentSuffix = ".bak"
	absentSuffix  = ".absent"
	tsFormat      = "20060102T150405.000000000"
)

// Engine writes and reads backups under a single root directory (typically
// <DataDir>/backups).
type Engine struct {
	// Root is the directory under which per-path backup folders are created.
	Root string
}

// NewEngine returns an Engine rooted at the given directory.
func NewEngine(root string) *Engine { return &Engine{Root: root} }

// dirFor returns the backup directory for a given source path. The directory
// name combines a readable, sanitized form of the path with a short hash of the
// cleaned absolute path to avoid collisions between distinct paths.
func (e *Engine) dirFor(path string) string {
	clean := filepath.Clean(path)
	sum := sha256.Sum256([]byte(clean))
	short := hex.EncodeToString(sum[:])[:8]
	safe := sanitize(clean)
	return filepath.Join(e.Root, safe+"-"+short)
}

func sanitize(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		s = "root"
	}
	if len(s) > 80 {
		s = s[len(s)-80:]
	}
	return s
}

// Backup snapshots path. If path exists, its bytes are copied to a timestamped
// "<ts>.bak" entry; if it is missing, a "<ts>.absent" marker is recorded so a
// later RestoreLatest deletes an adapter-created file. It returns the backup
// entry path written. It is safe to call when the source is missing.
func (e *Engine) Backup(path string) (string, error) {
	dir := e.dirFor(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, statErr := os.ReadFile(path)
	absent := false
	if statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return "", statErr
		}
		absent = true
	}
	suffix := contentSuffix
	if absent {
		suffix = absentSuffix
	}
	entry, err := e.uniqueEntry(dir, suffix)
	if err != nil {
		return "", err
	}
	if absent {
		if err := os.WriteFile(entry, nil, 0o600); err != nil {
			return "", err
		}
		return entry, nil
	}
	if err := os.WriteFile(entry, data, 0o600); err != nil {
		return "", err
	}
	return entry, nil
}

// uniqueEntry returns a non-colliding timestamped entry path within dir.
func (e *Engine) uniqueEntry(dir, suffix string) (string, error) {
	base := time.Now().UTC().Format(tsFormat)
	for i := 0; i < 1000; i++ {
		name := base + suffix
		if i > 0 {
			name = base + "-" + itoa(i) + suffix
		}
		entry := filepath.Join(dir, name)
		if _, err := os.Stat(entry); errors.Is(err, fs.ErrNotExist) {
			return entry, nil
		}
	}
	return "", errors.New("backup: could not allocate unique backup entry")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// latest returns the most recent backup entry in dir, or "" if none exist.
func latest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		n := en.Name()
		if strings.HasSuffix(n, contentSuffix) || strings.HasSuffix(n, absentSuffix) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}
