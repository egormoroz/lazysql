# pgviewer

Clean, read-only PostgreSQL database viewer TUI. Built with Bubble Tea + pgx.

## Build & Check

```sh
make build    # go build -o pgviewer .
make lint     # golangci-lint run
make check    # go vet + lint
make test     # go test ./...
```

Always run `make check` before considering work done.

## Architecture

Single Go package. Each file owns one concern:

- `main.go` — App model, entry point, CLI flags, panel layout
- `db.go` — All database queries (read-only). Keyset pagination via row-value comparison
- `ui_tree.go` — Schema/table tree sidebar (filtering, expand/collapse)
- `ui_table.go` — Paginated data table (scrolling, cursor, column sizing)
- `config.go` — YAML config loading with env var expansion
- `format.go` — Cell formatting, truncation, column width calculation
- `log.go` — slog JSON logger to file

## Conventions

- **Strict linting**: golangci-lint v2 with `funlen` (80 lines / 50 statements), `gocyclo` + `cyclop` (complexity 15), `gocognit` (30), `errcheck`, `staticcheck`, `unused`, `ineffassign`. Do not disable linters or raise thresholds — refactor to comply.
- **Error handling**: Always check errors. Wrap with `fmt.Errorf("context: %w", err)`. Log with `slog.Error(...)`.
- **Context**: Pass `context.Context` through all DB calls for cancellation support.
- **No tests yet** but the Makefile target exists — add tests as features grow.
- **Comments**: Only where logic isn't obvious. No doc-comment boilerplate on every function.
- **Keep functions short**: If a function is getting long, split it. The linter will catch you at 80 lines.
- **Single package**: No sub-packages. Organize by file, not directory.
- **Read-only**: This tool does not modify the database. No INSERT/UPDATE/DELETE.

## Config

`config.yaml` is gitignored (contains credentials). See `config.example.yaml` for the format.
Connection priority: CLI flag `-url` > config file > env vars (`PGVIEWER_URL` / `DATABASE_URL`).
