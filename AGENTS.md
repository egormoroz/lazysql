# AGENTS.md

## Scope

This file documents practical guidance for agents working in the `lazysql` repo.

Lazysql is a Go TUI SQL client built on `tview`/`tcell`. The most important constraint in this codebase is that UI-thread safety matters: blocking work and UI mutations must be handled carefully.

## Repo shape

- `components/`: TUI primitives and application screens.
- `drivers/`: DB-specific implementations for Postgres, MySQL, SQLite, MSSQL.
- `app/`: app bootstrap, config, theme, keymaps.
- `helpers/`, `internal/`, `lib/`: utilities and support code.
- `main.go`: CLI entrypoint.

## Build and test

- Run tests with:
  ```bash
  go test ./...
  ```
- Install local build for the current user with:
  ```bash
  go install .
  ```

## UI and concurrency rules

- Treat `tview` primitives as UI-thread owned.
- Do not mutate `tview` components from random goroutines.
- If background work is needed, do DB/network work in a goroutine and marshal results back with `App.QueueUpdateDraw(...)`.
- Avoid synchronous UI queue calls from input handlers if they can wait on the UI loop.
- If a method can be triggered from key handlers, assume it may run on the UI event loop.

### Known safe pattern

- Use async fetch methods that:
  1. guard against concurrent execution,
  2. perform DB work in a goroutine,
  3. apply UI state in `App.QueueUpdateDraw(...)`.

### Known unsafe pattern

- Direct DB calls from input handlers coupled with synchronous UI updates.
- Updating sidebar or table primitives from `go func()` callbacks without marshaling back to the UI thread.

## Table browsing behavior

The table browsing path has recent intentional behavior changes. Preserve them unless the user asks otherwise.

- Opening a table must not automatically execute `COUNT(*)`.
- Pagination is cursor-like:
  - fetch `limit + 1`,
  - infer `hasNextPage`,
  - do not rely on total row count by default.
- Manual row count exists as a dedicated action and may respect current filter.
- Table fetches should be single-flight:
  - at most one in-flight browse/count query per table,
  - extra requests while one is running should be ignored or coalesced, not stacked.

## Keybinding semantics to preserve

- `q` is context-sensitive:
  - if it closes the currently focused modal/viewer, it must be consumed there,
  - it must not accidentally propagate into app quit.
- Full app quit currently requires double `q` in main contexts.
- `Ctrl+W` is the immediate quit path.
- Tree navigation:
  - `g` should move both cursor and viewport to the top,
  - `G` should move both cursor and viewport to the bottom.
- Table navigation should autoload when crossing page boundaries:
  - `j` / `k`,
  - `Ctrl+D` / `Ctrl+U`,
  - explicit page navigation.

## Logging

- Runtime logs are only written if the user passes `--logfile`.
- Default logger level is controlled by `--loglevel`.
- When debugging TUI hangs, add narrow logs around:
  - key handler entry,
  - query start/end,
  - UI callback application.
- Remove or reduce noisy logs once the issue is understood.

## Config and docs

- If behavior changes are user-facing, update `README.md` when appropriate.
- Keep config additions small and explicit in `models.AppConfig` and `app/config.go`.
- Prefer feature flags only when behavior should remain optional. If the new behavior is clearly better and simpler, default it on.

## Editing guidance

- Use `apply_patch` for manual edits.
- Keep changes small and local when debugging TUI behavior.
- Avoid broad refactors unless needed to fix a correctness issue.
- If you change key behavior, verify all relevant contexts:
  - main screen,
  - tree,
  - table,
  - JSON viewer,
  - help/query history/query preview modals.

## Verification checklist

For TUI interaction changes, verify at least:

- `go test ./...`
- open a table
- next/prev page
- boundary autoload with `j`/`k`
- boundary autoload with `Ctrl+D`/`Ctrl+U`
- `q` behavior in overlays and app quit behavior
- tree `g` / `G`

## Suggested posture for future changes

- Favor correctness and predictable interaction over cleverness.
- In this repo, a small explicit state machine is usually better than implicit coupling through UI callbacks.
- If unsure whether a change belongs in UI thread or background work, keep DB work off-thread and UI work on-thread.
