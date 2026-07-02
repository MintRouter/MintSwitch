# MintSwitch — AI Tool API Config Switcher

Cross-platform desktop app to switch AI coding tools (Claude Code, Codex, OpenCode, Factory Droid, Zed, Kilo Code) to a custom OpenAI-compatible endpoint. One profile, per-tool or global apply, automatic backups, one-click restore. Built with Go + Wails.

## Project layout

```
.
├── main.go              # App entry point (desktop + web/server via build tags)
├── Taskfile.yml         # Top-level tasks (dev, build, build:server, run:server)
├── build/               # Wails build config, per-OS Taskfiles, Docker, icons
├── internal/            # Go backend packages
│   ├── adapters/        # Per-tool config adapters (claudecode, codex, opencode, droid, zed, kilo)
│   ├── injectors/       # Per-tool MCP server injectors (claudecode, codex, opencode, droid, kilo)
│   ├── backup/          # Config backup and restore
│   ├── core/            # Domain model: profiles, adapter interfaces, registry
│   ├── installer/       # Tool install detection
│   ├── paths/           # Home/data directory resolution
│   ├── settings/        # App settings persistence
│   └── service/         # Backend service exposed to the frontend via bindings
└── frontend/            # Svelte + TS + Vite frontend
    ├── src/             # App.svelte and UI components (lib/)
    ├── bindings/        # Auto-generated TS bindings (do not edit by hand)
    └── dist/            # Built assets, embedded into the Go binary (generated)
```

The Go binary embeds the frontend with `//go:embed all:frontend/dist`, so
`frontend/dist` must be built before `go build` (the Task targets do this).

## Prerequisites

- **Go** 1.24+ (developed against go1.26)
- **Node.js** 20+ and **npm** (for the Svelte/Vite frontend)
- **Wails v3 CLI** and **Task** (the Wails task runner):

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
```

Ensure `"$(go env GOPATH)/bin"` is on your `PATH` so `wails3` is found.
Run `wails3 doctor` to verify your platform's native GUI dependencies.

## Run — desktop (default)

Native WebView desktop app with hot reload:

```sh
wails3 dev
```

Or build/run a desktop binary directly:

```sh
wails3 task build      # production .app/binary for the current OS
go build -o bin/mintswitch . && ./bin/mintswitch
```

## Run — web / server mode

The **same** codebase serves the frontend over HTTP when built with the
`server` build tag (Wails v3's experimental HTTP server mode — integrated into
`pkg/application`, enabled by the tag; no separate plugin import is required).

```sh
wails3 task run:server          # build (-tags server) + run, then open the URL
```

Equivalent manual commands:

```sh
wails3 task common:build:frontend                 # generate bindings + build dist
go build -tags server -o bin/mintswitch-server .  # build the server binary
./bin/mintswitch-server                           # serves http://localhost:8080
```

Open <http://localhost:8080> in a browser. Host/port default to
`localhost:8080` (see `ServerOptions` in `main.go`) and can be overridden at
runtime:

```sh
WAILS_SERVER_HOST=0.0.0.0 WAILS_SERVER_PORT=8099 ./bin/mintswitch-server
```

A Docker image for the server build is also available:
`wails3 task run:docker` (exposes port 8080).

## Common tasks

```sh
go build ./...        # compile the desktop build of all packages
go vet ./...          # static checks
wails3 task --list    # list all available tasks
```

## License

MIT — see [LICENSE](LICENSE).

