# MintSwitch — AI Tool API Config Switcher

MintSwitch is a cross-platform **desktop-only** app for switching AI coding tools (Claude Code, Claude Desktop, Codex, OpenCode, and Pi) to a custom OpenAI-compatible endpoint. It supports per-tool or global apply, automatic backups, and one-click restore.

A profile can contain multiple Providers, each with a user-chosen name, base URL, API key, its own model list, and a default model. One active Provider applies to all tools, with optional per-tool Provider overrides. API keys are stored in the OS keychain (with a settings-file fallback), and key values are never displayed.

MintSwitch is built with Go and Wails. It does not provide or support an HTTP server, web deployment, or server container image.

## Features

- **Model tiers** — optionally pin secondary models per tool: Claude Code's opus / sonnet / haiku / fable aliases plus background (small/fast) tasks, OpenCode's small/fast model for lightweight tasks such as title generation, and the model Codex uses for its `/review` flow. Empty tiers follow the default model.
- **Theme toggle** — Light, Dark, or System (follows the OS `prefers-color-scheme` and updates in real time).
- **Internationalization** — English, Tiếng Việt, and 中文, switchable live from the UI.

## Project layout

- `main.go` — desktop application entry point
- `Taskfile.yml` — top-level desktop development, build, and package tasks
- `build/` — Wails config, platform packaging, icons, and optional desktop cross-compilation tooling
- `internal/` — adapters, backup/restore, settings, key storage, and the backend service
- `frontend/` — Svelte, TypeScript, and Vite desktop UI

The Go binary embeds `frontend/dist`, so the frontend must be built before direct Go build, test, vet, or vulnerability commands that load the root package. Wails tasks handle this automatically.

## Prerequisites

- Go 1.26.6 or newer
- Node.js 22 and npm
- Wails v3 CLI at the version used by `go.mod`
- Task-compatible Wails task runner
- Native GUI dependencies reported by `wails3 doctor`

Install the matching Wails CLI and check the host:

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.111
wails3 doctor
```

Ensure `$(go env GOPATH)/bin` is on `PATH`.

## Develop and package

Run the desktop app with hot reload:

```sh
wails3 dev
```

Build or package for the current desktop platform:

```sh
wails3 task build
wails3 task package
```

`wails3 task setup:docker` builds the optional `Dockerfile.cross` image used only to cross-compile desktop binaries. It is not a runtime or server image.

## Verify changes

Run the same quality gates used by CI:

```sh
(cd frontend && npm ci && npm run check && npm run build && npm audit --audit-level=high)
go test -race ./...
go vet ./...
govulncheck ./...
```

Install `govulncheck` if it is not already available:

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
```

CI runs these checks for pull requests and branch pushes. Tag releases must pass the same checks before any unsigned desktop package is built or published.

## License

MIT — see [LICENSE](LICENSE).
