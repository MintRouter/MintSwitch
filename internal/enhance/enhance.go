// Package enhance installs a "/enhance" slash command into the AI coding
// tools MintSwitch manages, and implements the local CLI mode that command
// calls back into.
//
// The command file rendered for each tool (Claude Code, Codex, OpenCode and
// Pi) sends the user's rough task text to the effective provider's
// POST {base}/v1/enhance-prompt endpoint and asks the tool to work from the
// enhanced prompt. The HTTP call is performed by the MintSwitch binary itself
// (`MintSwitch enhance-prompt --tool <id>`), so the tools need no curl/jq, no
// shell script on PATH and never see the API key: the key is read from
// MintSwitch's own settings (keychain-first) at call time.
//
// Command files are written atomically with a backup of whatever was there
// before, and removal restores that pre-install state, mirroring the tool
// adapters' apply/restore discipline. Each rendered file carries a marker
// comment so a user-authored enhance.md is never overwritten silently: it is
// backed up first and restored on Remove.
package enhance

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mintswitch/internal/backup"
	"mintswitch/internal/core"
	"mintswitch/internal/paths"
)

// Status values reported by [Manager.Status].
const (
	// StatusNotInstalled means the tool has no /enhance command file.
	StatusNotInstalled = "not_installed"
	// StatusInstalled means the command file matches what MintSwitch would
	// render right now (same binary path, same template).
	StatusInstalled = "installed"
	// StatusOutdated means a MintSwitch-rendered command file exists but its
	// content differs from the current render — typically the app was moved
	// or upgraded — so it should be re-installed.
	StatusOutdated = "outdated"
	// StatusForeign means an enhance command file exists that MintSwitch did
	// not write. Install backs it up first; Remove restores it.
	StatusForeign = "foreign"
)

// marker is the comment every rendered command file carries so Status can
// tell a MintSwitch-managed file from a user-authored one.
const marker = "<!-- managed by MintSwitch: reinstall from MintSwitch after moving or updating the app -->"

// CommandName is the slash command installed in every supported tool.
const CommandName = "enhance"

// Manager renders, installs and removes the /enhance command files.
type Manager struct {
	r   *paths.Resolver
	e   *backup.Engine
	exe string
}

// New builds a Manager. exe is the absolute path of the MintSwitch binary the
// rendered command files call back into (normally os.Executable()).
func New(r *paths.Resolver, e *backup.Engine, exe string) *Manager {
	return &Manager{r: r, e: e, exe: exe}
}

// Supported reports whether toolID gets a /enhance command. Claude Desktop
// has no slash commands, so it is excluded.
func Supported(toolID string) bool {
	switch toolID {
	case "claude-code", "codex", "opencode", "pi":
		return true
	}
	return false
}

// Supports is the method form of [Supported].
func (m *Manager) Supports(toolID string) bool { return Supported(toolID) }

// Path returns the command file location for toolID and whether the tool is
// supported.
func (m *Manager) Path(toolID string) (string, bool) { return m.path(toolID) }

func (m *Manager) path(toolID string) (string, bool) {
	switch toolID {
	case "claude-code":
		// Claude Code: ~/.claude/commands/<name>.md → /<name>. Supports the
		// !`cmd` preamble, so the call happens before the model runs.
		return filepath.Join(m.r.ClaudeDir(), "commands", CommandName+".md"), true
	case "codex":
		// Codex CLI: ~/.codex/prompts/<name>.md → /prompts:<name>. Top-level
		// Markdown files only; no shell substitution.
		return filepath.Join(m.r.CodexDir(), "prompts", CommandName+".md"), true
	case "opencode":
		// OpenCode: ~/.config/opencode/commands/<name>.md → /<name>. Supports
		// the !`cmd` substitution like Claude Code.
		return m.r.ConfigJoin("opencode", "commands", CommandName+".md"), true
	case "pi":
		// Pi: ~/.pi/agent/prompts/<name>.md → /<name>. Direct children only.
		return m.r.Join(".pi", "agent", "prompts", CommandName+".md"), true
	}
	return "", false
}

// Status inspects the command file for toolID. ok is false for unsupported
// tools.
func (m *Manager) Status(toolID string) (status string, path string, ok bool) {
	p, ok := m.path(toolID)
	if !ok {
		return "", "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return StatusNotInstalled, p, true
	}
	if bytes.Equal(data, m.Render(toolID)) {
		return StatusInstalled, p, true
	}
	if bytes.Contains(data, []byte(marker)) {
		return StatusOutdated, p, true
	}
	return StatusForeign, p, true
}

// Install renders the command file for toolID and writes it atomically after
// backing up any existing file (or recording its absence) so Remove can
// restore the pre-install state. It is idempotent: an up-to-date file is left
// untouched and no new backup is taken.
func (m *Manager) Install(toolID string) (core.ApplyResult, error) {
	p, ok := m.path(toolID)
	if !ok {
		return core.ApplyResult{}, fmt.Errorf("enhance: tool %q has no slash-command support", toolID)
	}
	if strings.TrimSpace(m.exe) == "" {
		return core.ApplyResult{}, errors.New("enhance: MintSwitch executable path is unknown")
	}
	want := m.Render(toolID)
	if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, want) {
		return core.ApplyResult{ChangedPath: p, Message: "/" + CommandName + " command is already up to date."}, nil
	}
	bk, err := m.e.Backup(p)
	if err != nil {
		return core.ApplyResult{}, fmt.Errorf("enhance: backup %s: %w", p, err)
	}
	if err := core.WriteFileAtomic(p, want, 0o600); err != nil {
		return core.ApplyResult{}, fmt.Errorf("enhance: write %s: %w", p, err)
	}
	return core.ApplyResult{
		ChangedPath: p,
		BackupPath:  bk,
		Message:     "/" + CommandName + " command installed. Restart the tool (or start a new session) to pick it up.",
	}, nil
}

// Remove restores the pre-install state: the oldest backup (which may be an
// "absent" marker, deleting the file) when one exists, otherwise a
// MintSwitch-rendered file is deleted. A file MintSwitch never wrote and never
// backed up is left alone with an error. It is a safe no-op when nothing is
// installed.
func (m *Manager) Remove(toolID string) (core.RestoreResult, error) {
	p, ok := m.path(toolID)
	if !ok {
		return core.RestoreResult{}, fmt.Errorf("enhance: tool %q has no slash-command support", toolID)
	}
	has, err := m.e.HasBackup(p)
	if err != nil {
		return core.RestoreResult{}, err
	}
	if has {
		_, entry, err := m.e.RestorePristine(p)
		if err != nil {
			return core.RestoreResult{}, fmt.Errorf("enhance: restore %s: %w", p, err)
		}
		return core.RestoreResult{ChangedPath: p, BackupPath: entry, Message: "/" + CommandName + " command removed."}, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return core.RestoreResult{ChangedPath: p, Message: "Nothing to remove."}, nil
		}
		return core.RestoreResult{}, err
	}
	if !bytes.Contains(data, []byte(marker)) {
		return core.RestoreResult{}, fmt.Errorf("enhance: %s was not written by MintSwitch; remove it manually", p)
	}
	if err := os.Remove(p); err != nil {
		return core.RestoreResult{}, err
	}
	return core.RestoreResult{ChangedPath: p, Message: "/" + CommandName + " command removed."}, nil
}

// Render returns the command file content for toolID (nil for an unsupported
// tool). The binary path is embedded verbatim, so the file must be
// re-rendered when the app moves — Status reports that as outdated.
func (m *Manager) Render(toolID string) []byte {
	if _, ok := m.path(toolID); !ok {
		return nil
	}
	exe := filepath.ToSlash(m.exe)
	cmd := shellQuote(exe) + " enhance-prompt --tool " + toolID
	var b strings.Builder
	switch toolID {
	case "claude-code":
		b.WriteString("---\n")
		b.WriteString("description: Enhance a rough task through MintRouter's /v1/enhance-prompt (via MintSwitch), show the enhanced prompt first, then work from it\n")
		b.WriteString("argument-hint: <rough task description>\n")
		b.WriteString("allowed-tools: Bash(" + exe + " enhance-prompt:*)\n")
		b.WriteString("---\n")
		b.WriteString(marker + "\n\n")
		b.WriteString("## Enhanced prompt (MintRouter `/v1/enhance-prompt`)\n\n")
		b.WriteString("!`printf '%s' \"$ARGUMENTS\" | " + cmd + "`\n\n")
		writeInlineInstructions(&b)
	case "opencode":
		b.WriteString("---\n")
		b.WriteString("description: Enhance a rough task through MintRouter's /v1/enhance-prompt (via MintSwitch), show the enhanced prompt first, then work from it\n")
		b.WriteString("---\n")
		b.WriteString(marker + "\n\n")
		b.WriteString("## Enhanced prompt (MintRouter `/v1/enhance-prompt`)\n\n")
		b.WriteString("!`printf '%s' \"$ARGUMENTS\" | " + cmd + "`\n\n")
		writeInlineInstructions(&b)
	case "codex", "pi":
		b.WriteString("---\n")
		b.WriteString("description: Enhance a rough task through MintRouter's /v1/enhance-prompt (via MintSwitch), show the enhanced prompt first, then work from it\n")
		b.WriteString("argument-hint: <rough task description>\n")
		b.WriteString("---\n")
		b.WriteString(marker + "\n\n")
		writeAgentInstructions(&b, cmd)
	}
	return []byte(b.String())
}

// writeInlineInstructions is the body for tools that substitute the shell
// call before the model runs (the enhanced prompt is already in the message).
func writeInlineInstructions(b *strings.Builder) {
	b.WriteString(`## Instructions

The block above is the user's request after enhancement by MintRouter's prompt
enhancer. Treat it as the task statement.

- First, before any tool call or plan, echo the enhanced prompt back to the
  user verbatim under a short heading (e.g. **Enhanced prompt:**) in a fenced
  block, so they can see what the task statement became. Then continue.
- If the block is an ` + "`enhance-prompt:`" + ` error line instead of a prompt, stop and
  report the error (no provider configured in MintSwitch, missing API key,
  server unreachable, HTTP status); do not guess the task.
- Before editing, ground the enhanced prompt in the current repo: locate the
  files involved and read the project's agent guidance if present (AGENTS.md /
  CLAUDE.md / .cursorrules) and follow its rules.
- Restate the final plan in 3–6 bullets, then execute.
- The enhancer uses the provider MintSwitch has applied to this tool; change
  it in MintSwitch, not here.

Original request, for reference: $ARGUMENTS
`)
}

// writeAgentInstructions is the body for tools without shell substitution:
// the model itself runs the MintSwitch CLI first.
func writeAgentInstructions(b *strings.Builder, cmd string) {
	b.WriteString(`## Task

Enhance the request below through MintRouter's prompt enhancer, then work from
the enhanced prompt.

1. Run exactly this shell command, passing the original request text on
   stdin (a quoted heredoc is safest; never put the text in the command line):

   ` + "```sh\n   " + cmd + " <<'MINTSWITCH_EOF'\n   <original request>\n   MINTSWITCH_EOF\n   ```" + `

   It prints the enhanced prompt on stdout. It reads the provider and API key
   from MintSwitch's own settings — no environment variables are needed.
2. If the output is an ` + "`enhance-prompt:`" + ` error line instead of a prompt, stop and
   report the error (no provider configured in MintSwitch, missing API key,
   server unreachable, HTTP status); do not guess the task.
3. Echo the enhanced prompt back to the user verbatim under a short heading
   (e.g. **Enhanced prompt:**) in a fenced block, then treat it as the task
   statement.
4. Before editing, ground it in the current repo: locate the files involved
   and read the project's agent guidance if present (AGENTS.md / CLAUDE.md /
   .cursorrules) and follow its rules.
5. Restate the final plan in 3–6 bullets, then execute.

## Original request

$ARGUMENTS
`)
}

// shellQuote single-quotes s for a POSIX shell (bash / Git Bash), so a binary
// path containing spaces survives the tools' shell substitution.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
