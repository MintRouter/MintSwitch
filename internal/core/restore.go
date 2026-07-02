package core

import "encoding/json"

// restoreNothingMessage is the shared Restore result message when neither a
// backup restore nor a strip fallback touched the tool's config.
const restoreNothingMessage = "No backup found; nothing to restore."

// RestoreMarkerStore is the minimal sidecar marker-store surface the shared
// restore skeleton needs. *markers.Store satisfies it.
type RestoreMarkerStore interface {
	Get(toolID string) (Marker, bool, error)
	Delete(toolID string) error
}

// SingleFileRestore parameterizes [RestoreSingleFile], the restore skeleton
// shared by every adapter that manages a single config file. The tool-specific
// pieces — orphan-remnant detection, the strip fallback and the user-facing
// messages — stay in the adapter and are injected here.
type SingleFileRestore struct {
	// ToolID is the adapter's stable identifier (its key in the marker store).
	ToolID string
	// Path is the tool config file to restore.
	Path string
	// Store is the sidecar marker store.
	Store RestoreMarkerStore
	// RestorePristine restores path from its oldest backup snapshot,
	// reporting whether it did and which backup entry it used
	// (backup.Engine.RestorePristine).
	RestorePristine func(path string) (restored bool, entry string, err error)
	// OrphanRemnantAt reports whether the file at path still carries the
	// tool's MintSwitch injection signature without requiring a marker.
	OrphanRemnantAt func(path string) bool
	// StripManaged removes the MintSwitch-managed entries from the file at
	// path, reporting whether it modified the file.
	StripManaged func(path string) (bool, error)
	// RestoredMessage is the result message after a successful backup restore.
	RestoredMessage string
	// StrippedMessage is the result message after the strip fallback ran.
	StrippedMessage string
}

// RestoreSingleFile reverts a single-file tool config to its pristine
// pre-MintSwitch state via the backup engine and removes the tool's entry
// from the sidecar marker store. When no backup exists but the file is still
// MintSwitch-managed (marker in store, or — with the marker lost — the
// injection signature still in the file, per OrphanRemnantAt), it falls back
// to StripManaged. It is a safe no-op when nothing was applied.
func RestoreSingleFile(s SingleFileRestore) (RestoreResult, error) {
	_, inStore, err := s.Store.Get(s.ToolID)
	if err != nil {
		return RestoreResult{}, err
	}
	restored, entry, err := s.RestorePristine(s.Path)
	if err != nil {
		return RestoreResult{}, err
	}
	stripped := false
	if !restored && (inStore || s.OrphanRemnantAt(s.Path)) {
		stripped, err = s.StripManaged(s.Path)
		if err != nil {
			return RestoreResult{}, err
		}
	}
	if err := s.Store.Delete(s.ToolID); err != nil {
		return RestoreResult{}, err
	}
	msg := restoreNothingMessage
	switch {
	case restored:
		msg = s.RestoredMessage
	case stripped:
		msg = s.StrippedMessage
	}
	return RestoreResult{ChangedPath: s.Path, BackupPath: entry, Message: msg}, nil
}

// ExtractLegacyMarker pulls a legacy in-file MintSwitch marker out of a parsed
// config object (under [MarkerKey], converted via a JSON round-trip so it also
// works for TOML-parsed maps). It reports false when the key is absent or its
// value does not decode as a [Marker]; callers that only care about managed
// markers must additionally check Marker.Managed.
func ExtractLegacyMarker(m map[string]any) (Marker, bool) {
	raw, ok := m[MarkerKey]
	if !ok {
		return Marker{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return Marker{}, false
	}
	var marker Marker
	if err := json.Unmarshal(b, &marker); err != nil {
		return Marker{}, false
	}
	return marker, true
}
