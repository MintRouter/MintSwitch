package core

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// ToolStatus describes the configuration state of a tool relative to MintSwitch.
type ToolStatus int

const (
	// StatusNotInstalled means the tool was not detected on the system.
	StatusNotInstalled ToolStatus = iota
	// StatusDefault means the tool is installed and its config carries no
	// MintSwitch-managed marker (i.e. it is in its own/default state).
	StatusDefault
	// StatusAppliedByMintSwitch means the tool config carries a MintSwitch
	// marker whose fingerprint matches the active profile.
	StatusAppliedByMintSwitch
	// StatusModifiedExternally means the tool config carries a MintSwitch
	// marker, but the managed values have since changed (fingerprint mismatch),
	// indicating the file was edited outside MintSwitch.
	StatusModifiedExternally
)

// String returns a stable lower-case identifier for the status.
func (s ToolStatus) String() string {
	switch s {
	case StatusNotInstalled:
		return "not_installed"
	case StatusDefault:
		return "default"
	case StatusAppliedByMintSwitch:
		return "applied_by_mintswitch"
	case StatusModifiedExternally:
		return "modified_externally"
	default:
		return "unknown"
	}
}

// Detail returns a human-readable description of the status.
func (s ToolStatus) Detail() string {
	switch s {
	case StatusNotInstalled:
		return "Tool is not installed."
	case StatusDefault:
		return "Tool is using its own configuration (not managed by MintSwitch)."
	case StatusAppliedByMintSwitch:
		return "MintSwitch profile is currently applied."
	case StatusModifiedExternally:
		return "Configuration was modified outside MintSwitch since it was applied."
	default:
		return "Unknown status."
	}
}

// ApplyResult summarizes the outcome of [ToolAdapter.Apply].
type ApplyResult struct {
	// ChangedPath is the tool config file that was written.
	ChangedPath string `json:"changed_path,omitempty"`
	// BackupPath is the backup created before the change, if any.
	BackupPath string `json:"backup_path,omitempty"`
	// Message is a human-readable summary, safe to display (no secrets).
	Message string `json:"message,omitempty"`
}

// RestoreResult summarizes the outcome of [ToolAdapter.Restore].
type RestoreResult struct {
	// ChangedPath is the tool config file that was restored or removed.
	ChangedPath string `json:"changed_path,omitempty"`
	// BackupPath is the backup that was used to restore, if any.
	BackupPath string `json:"backup_path,omitempty"`
	// Message is a human-readable summary, safe to display (no secrets).
	Message string `json:"message,omitempty"`
}

// ToolAdapter is the contract every supported AI coding tool implements.
//
// Implementations must be safe to construct cheaply and must never call
// os.UserHomeDir or read process-global state directly: all filesystem
// locations must derive from an injected paths.Resolver so tests can point
// HOME at a temporary directory.
type ToolAdapter interface {
	// ID returns a stable identifier, e.g. "claude-code".
	ID() string
	// Name returns the display name, e.g. "Claude Code".
	Name() string
	// ConfigPaths returns the candidate config file paths, resolved to
	// absolute paths, that this adapter manages.
	ConfigPaths() []string
	// Detect reports whether the tool is installed and, if so, the active
	// config path it would read/write.
	Detect() (installed bool, activePath string)
	// Status inspects the current config relative to the given profile.
	Status(p Profile) (status ToolStatus, detail string, err error)
	// Apply backs up the existing config first, then idempotently injects the
	// MintSwitch-managed settings (including the marker, see [Marker]).
	Apply(p Profile) (ApplyResult, error)
	// Restore reverts to the pre-apply state via backup / removal of injected
	// keys. It must be a safe no-op when nothing was applied.
	Restore() (RestoreResult, error)
}

// MarkerKey is the sentinel key adapters write into a tool's config to mark it
// as MintSwitch-managed. Adapters embed a [Marker] value under this key
// (translated to the tool's config format, e.g. a JSON object or TOML table).
const MarkerKey = "mintswitchManaged"

// MarkerVersion is the current marker schema version.
const MarkerVersion = 1

// Marker is the MintSwitch sentinel embedded in a managed tool config. It lets
// Status distinguish MintSwitch-applied configs from default/externally-edited
// ones via the Fingerprint of the managed fields.
type Marker struct {
	Managed      bool      `json:"managed"`
	ProfileLabel string    `json:"profileLabel,omitempty"`
	Fingerprint  string    `json:"fingerprint"`
	AppliedAt    time.Time `json:"appliedAt"`
	Version      int       `json:"version"`
}

// NewMarker builds a Marker for the given profile and label.
func NewMarker(p Profile, label string) Marker {
	return Marker{
		Managed:      true,
		ProfileLabel: label,
		Fingerprint:  Fingerprint(p),
		AppliedAt:    time.Now().UTC(),
		Version:      MarkerVersion,
	}
}

// Fingerprint returns a stable hex SHA-256 over the managed profile fields. All
// adapters must use this so external-modification detection is consistent.
func Fingerprint(p Profile) string {
	h := sha256.New()
	for _, f := range []string{p.BaseURL, p.APIKey, p.Model, p.SmallFastModel} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
