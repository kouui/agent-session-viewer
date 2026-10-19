# agent-viewer

Read-only, realtime web viewer for coding-agent session history. Sessions are grouped by
project and sorted by last modified. Multiple sessions can be watched at once in split
panes / tabs.

Supported: **opencode** (`opencode.db`, SQLite) and **codex** (`~/.codex/sessions/**/rollout-*.jsonl`).

```
make build      # ./agent-viewer          (Linux / WSL)
make windows    # dist/agent-viewer.exe   (Windows)
./agent-viewer  # http://127.0.0.1:9191
```

## Detection
Each build looks only at its own user home: the WSL/Linux build at `~`, the Windows build at
`%USERPROFILE%`. Run the matching build on each side.

- opencode: `$XDG_DATA_HOME/opencode` or `~/.local/share/opencode`
- codex: `$CODEX_HOME` or `~/.codex`
- extra: `-dir <path>` (repeatable; opencode.db / opencode data dir / codex home)

## Read-only
opencode DB is opened with `mode=ro` + `query_only`; codex files are opened read-only and only
read forward. Nothing is ever written.

## Architecture
`model.go` defines the normalized contract (`Session`, `Message`, `Part`, `Delta`) and the
`Backend` interface. `opencode.go` and `codex.go` convert native formats into it; the server and
frontend only know the contract. Add an agent = one new file implementing `Backend` + a case in
`detect.go`.

Realtime: the server polls file stats (db/wal mtime, rollout dir) once a second and pushes SSE
`change` events; the browser then fetches incremental deltas (opaque cursor per view).

## Limits
- Opening a Windows-side opencode DB from WSL (`/mnt/c`) falls back to `immutable` mode, which can't
  see the WAL; run the Windows build instead (hence no `/mnt/c` auto-detect).
- opencode session `mtime` = max(session.time_updated, newest message time_created).
