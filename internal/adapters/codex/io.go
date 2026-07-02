package codex

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/pelletier/go-toml/v2"

	"mintswitch/internal/core"
)

// readTOML parses a TOML file into a generic map. A missing file yields an
// empty map (not an error), so Apply can create the file from scratch.
func readTOML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	m := map[string]any{}
	if len(data) == 0 {
		return m, nil
	}
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// writeTOML marshals the map to TOML and writes it atomically with 0600 perms,
// creating the parent directory as needed.
func writeTOML(path string, m map[string]any) error {
	data, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(path, data, 0o600)
}

// readJSON parses a JSON object file into a generic map. A missing or empty
// file yields an empty map.
func readJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	m := map[string]any{}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// writeJSON marshals the map to indented JSON and writes it atomically with
// 0600 perms.
func writeJSON(path string, m map[string]any) error {
	return core.WriteJSONObjectAtomic(path, m)
}

// markerMap builds the MintSwitch managed-marker as a generic map keyed by the
// marker's JSON field names, so it serializes consistently into TOML.
func markerMap(p core.Profile) (map[string]any, error) {
	raw, err := json.Marshal(core.NewMarker(p, p.Label))
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// markerFingerprint extracts the fingerprint from a parsed config's managed
// marker. It reports ok=false when no marker is present.
func markerFingerprint(cfg map[string]any) (string, bool) {
	raw, ok := cfg[core.MarkerKey]
	if !ok {
		return "", false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", false
	}
	fp, ok := m["fingerprint"].(string)
	if !ok {
		return "", false
	}
	return fp, true
}
