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

// extractLegacyMarker pulls a legacy in-file MintSwitch marker out of a parsed
// config (a [mintswitchManaged] TOML table, converted via a JSON round-trip).
// It reports false when the key is absent or its value does not decode as a
// [core.Marker].
func extractLegacyMarker(cfg map[string]any) (core.Marker, bool) {
	raw, ok := cfg[core.MarkerKey]
	if !ok {
		return core.Marker{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return core.Marker{}, false
	}
	var marker core.Marker
	if err := json.Unmarshal(b, &marker); err != nil {
		return core.Marker{}, false
	}
	return marker, true
}
