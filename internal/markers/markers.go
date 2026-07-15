// Package markers persists MintSwitch's per-tool managed markers in a sidecar
// JSON store (<DataDir>/markers.json) instead of inside each tool's own config
// file. Several tools validate their config strictly (OpenCode/Kilo via zod
// .strict(), Claude Code's settings.json) and reject files carrying the legacy
// top-level "mintswitchManaged" key, so the marker must live outside the tool
// configs entirely.
//
// The store is a single JSON object mapping tool adapter ID → [core.Marker],
// written atomically with 0600 perms. Adapters call Put on Apply, Get on
// Status, and Delete on Restore.
package markers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"mintswitch/internal/core"
)

// Store is a concurrency-safe sidecar marker store backed by a single JSON
// file. Construct it with [NewStore]; the file is created lazily on the first
// Put.
type Store struct {
	path string
	// mu serializes load-modify-save cycles so concurrent adapter calls cannot
	// interleave and lose writes.
	mu sync.Mutex
}

// NewStore returns a Store backed by the JSON file at path (typically
// paths.Resolver.MarkersPath()).
func NewStore(path string) *Store { return &Store{path: path} }

// Path returns the store's backing file path.
func (s *Store) Path() string { return s.path }

// Get returns the marker recorded for toolID and whether one exists. A missing
// or empty store file yields (zero, false, nil); a corrupt (unparseable) file
// yields an error so callers surface it rather than silently reporting an
// unmanaged state.
func (s *Store) Get(toolID string) (core.Marker, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return core.Marker{}, false, err
	}
	marker, ok := m[toolID]
	return marker, ok, nil
}

// Put records marker under toolID, replacing any prior entry. A corrupt store
// file does not block the write: it is first quarantined to <path>.corrupt
// (preserving the unreadable contents for recovery) and then replaced by a
// fresh store holding only this entry. If quarantining fails, Put returns an
// error rather than silently destroying the other tools' markers.
func (s *Store) Put(toolID string, marker core.Marker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		if qErr := s.quarantineCorrupt(err); qErr != nil {
			return qErr
		}
		m = map[string]core.Marker{}
	}
	m[toolID] = marker
	return s.save(m)
}

// Delete removes the entry for toolID. It is a no-op (and does not create the
// file) when the store is missing or has no such entry. A corrupt store file
// is quarantined to <path>.corrupt (preserving the unreadable contents for
// recovery) instead of being overwritten; the store is then absent, which is
// the deleted state. If quarantining fails, Delete returns an error rather
// than silently destroying the other tools' markers.
func (s *Store) Delete(toolID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return s.quarantineCorrupt(err)
	}
	if _, ok := m[toolID]; !ok {
		return nil
	}
	delete(m, toolID)
	return s.save(m)
}

// errCorrupt marks load errors caused by an unparseable store file (as
// opposed to I/O errors like permission failures). Put/Delete quarantine the
// file only for this class of error.
var errCorrupt = errors.New("markers: store file is corrupt")

// load reads the backing file into a map. A missing or empty file yields an
// empty map; malformed JSON yields an error wrapping [errCorrupt].
func (s *Store) load() (map[string]core.Marker, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]core.Marker{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]core.Marker{}, nil
	}
	var m map[string]core.Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %w", errCorrupt, s.path, err)
	}
	if m == nil {
		m = map[string]core.Marker{}
	}
	return m, nil
}

// quarantineCorrupt handles a load failure before a mutating write. For a
// corrupt (unparseable) file it renames the file to <path>.corrupt so the
// original bytes stay recoverable, replacing any previous quarantine (the
// newest evidence wins). Any other load error — e.g. a permission failure,
// where the store contents may be intact — is returned unchanged so the
// caller does not overwrite data it could not read.
func (s *Store) quarantineCorrupt(loadErr error) error {
	if !errors.Is(loadErr, errCorrupt) {
		return loadErr
	}
	if err := os.Rename(s.path, s.path+".corrupt"); err != nil {
		return fmt.Errorf("markers: quarantine corrupt store: %w", errors.Join(loadErr, err))
	}
	return nil
}

// save writes the map as indented JSON atomically with 0600 perms, creating
// parent directories as needed.
func (s *Store) save(m map[string]core.Marker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return core.WriteFileAtomic(s.path, data, 0o600)
}
