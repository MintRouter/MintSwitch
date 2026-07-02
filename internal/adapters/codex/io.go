package codex

import (
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
