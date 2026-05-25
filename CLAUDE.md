# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`dbsync` is a Go CLI tool for migrating data between databases. It exposes a single TUI command (`dbsync tui`) that walks the user through a four-step pipeline: inspect → extract → compare → transfer. It handles schema differences between source and target, making it suitable for data migrations where table/column names or types may have changed.

## Build commands

```bash
# Install dependencies
go mod tidy

# Build binary (output to bin/)
go build -o bin/dbsync ./cmd

# Cross-compile
GOOS=linux   GOARCH=amd64 go build -o bin/dbsync-linux  ./cmd
GOOS=darwin  GOARCH=arm64 go build -o bin/dbsync-mac    ./cmd
GOOS=windows GOARCH=amd64 go build -o bin/dbsync.exe    ./cmd
```

There are no tests in this project currently.

## Architecture

The four-step pipeline (`inspect → extract → compare → transfer`) maps to four packages in `internal/`. The only user-facing entry point is the TUI:

```
cmd/
  main.go     ← registers tuiCmd and backupCmd
  tui.go      ← runs tui.New() with tea.WithAltScreen()
  backup.go   ← headless inspect+extract for scheduled backups
internal/
  tui/        ← Bubble Tea model; drives the full pipeline interactively
  adapters/   ← DBAdapter interface + PostgreSQL implementation
  inspect/    ← Reads source schema → schema.yaml
  extract/    ← Exports table rows → *.ndjson + extract-manifest.yaml
  compare/    ← Compares source schema vs target DB → mapping.yaml
  transfer/   ← Reads mapping + ndjson files, inserts into target DB
  config/     ← Loads config.yaml
```

Each pipeline step writes files into a timestamped directory (`{timestamp}_{dbname}/`) inside the configured output directory. Per-step subfolders keep outputs organized:

```
{timestamp}_{dbname}/
  01-inspect/   schema.yaml
  02-extract/   extract-manifest.yaml + *.ndjson
  03-compare/   mapping.yaml
  04-transfer/  transfer.log [+ transfer-errors.log if row errors occurred]
```

### backup command (cmd/backup.go)

Non-interactive command that runs inspect + extract sequentially and prints progress to stdout. Uses `DBSYNC_SOURCE_CONN` env var for the connection. Exits non-zero on any error, making it suitable for cron jobs. Reuses the same `inspect.Run` and `extract.Run` functions as the TUI. Writes all output to a `backup.log` file alongside the output directory simultaneously.

### Key data flow

- `inspect.Schema` → serialized to `01-inspect/schema.yaml`; includes `PrimaryKey []string` per table, captured via `adapter.GetPrimaryKey`
- `extract.Summary` → serialized to `02-extract/extract-manifest.yaml`; skipped tables (no rows) are recorded here so `compare` auto-excludes them
- `compare.Mapping` → serialized to `03-compare/mapping.yaml`; `PrimaryKey` propagated from source schema; the TUI reorder (`+`/`-` keys) updates `Order` fields and calls `compare.Save()` before transfer starts; intended to be hand-edited (set `skip: true`, remap `target` names)
- `transfer` reads `mapping.yaml` + `*.ndjson`, inserts via `DBAdapter` using the selected conflict strategy (skip / upsert / truncate); writes `04-transfer/transfer.log`; if any rows fail, writes `04-transfer/transfer-errors.log` with per-row JSON records and error details

### TUI (internal/tui/model.go)

Bubble Tea model driving the full workflow. Steps: `stepConfig → stepInspect → stepBackup → stepAnalyze → stepTransfer → stepDone`.

Key behaviors:

- Connection strings are entered in the Config screen (not env vars); source and target must be different databases
- Streaming progress uses a buffered `chan tea.Msg` + chained `listenForProgress` tea.Cmd
- Each step creates and owns its own DB adapter, closed via `defer` when the step goroutine exits — no shared adapter state between steps
- The transfer adapter is created fresh inside the transfer goroutine (never reused from compare)
- `m.transferProgress map[string]transferProgressMsg` tracks per-table live status during transfer; `conflictRows` field counts silently skipped rows (shown as `⚠ X silently skipped`)
- `m.conflictStrategy transfer.ConflictStrategy` — cycled with `[c]` on the Compare screen; passed to `transfer.Options.ConflictStrategy` when transfer starts
- `rowErrorEntry` structs collect per-row failures during transfer; written to `transfer-errors.log` by `writeErrorLog`
- Retry (`r` key on done screen) returns to `stepAnalyze` with `m.transferProgress`, `m.errorLogPath`, and `m.summary` cleared

### DBAdapter interface

`internal/adapters/adapter.go` defines the `DBAdapter` interface. Only PostgreSQL (`adapters/postgres.go`) is implemented. New engines plug in by implementing this interface and wiring a new connection factory in the TUI's `cmdInspect` / `cmdAnalyze` / transfer goroutine.

Key methods:
- `InsertRows` returns `(int64, error)` — `int64` is `RowsAffected()` from the pgx batch, enabling silent skip detection for `ON CONFLICT DO NOTHING`
- `GetPrimaryKey(ctx, table)` — queries `information_schema` to return ordered PK column names; called during `inspect.Run`
- `UpsertRows(ctx, table, rows, pkCols)` — builds `ON CONFLICT (pkCols) DO UPDATE SET col = EXCLUDED.col` for all non-PK columns
- `TruncateTable(ctx, table)` — executes `TRUNCATE TABLE … CASCADE` before insert in truncate strategy

### Configuration

`config.yaml` sets `source.engine`, `output.directory`, and `ignored_tables`. Connection strings are entered directly in the TUI and are never stored in config files.

### Transfer conflict strategies

`transfer.ConflictStrategy` is an int enum (`ConflictSkip=0`, `ConflictUpsert=1`, `ConflictTruncate=2`) stored in `transfer.Options`. The TUI cycles it with `[c]` on the Compare screen and passes it to `transfer.Run`.

- `ConflictSkip` — `InsertRows` with `ON CONFLICT DO NOTHING`; actual inserts vs. skips tracked via `RowsAffected()`
- `ConflictUpsert` — `UpsertRows` with `ON CONFLICT (pkCols) DO UPDATE SET ...`; requires PK columns from `TableMapping.PrimaryKey`
- `ConflictTruncate` — calls `TruncateTable` before calling `InsertRows`; irreversible, shown with a warning in the TUI

After a batch insert failure, `identifyRowErrors` retries each row individually to identify the specific failing record and fires `opts.OnRowError`.

### Transfer field mapping

During transfer, only fields with `status: matched` in `mapping.yaml` are copied. The field map (`source → target`) is built per-table in `transfer.transferTable`. Unmatched fields are silently dropped.

### Scanner buffer

`transfer.transferTable` uses a `bufio.Scanner` with a 10 MB per-line buffer to handle large JSON rows. If rows grow larger, this limit needs adjusting.
