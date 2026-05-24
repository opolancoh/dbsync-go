# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`dbsync` is a Go CLI tool for migrating data between databases. It handles schema differences between source and target, making it suitable for data migrations where table/column names or types may have changed.

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

The four-step pipeline (`inspect → backup → analyze → transfer`) maps directly to four subcommands in `cmd/` and four packages in `internal/`:

```
cmd/          ← Cobra CLI wiring; one file per subcommand
internal/
  adapters/   ← DBAdapter interface + PostgreSQL implementation
  inspect/    ← Reads source schema → schema.yaml
  backup/     ← Exports table rows → *.ndjson + backup-manifest.yaml
  analyze/    ← Compares source schema vs target DB → mapping.yaml
  transfer/   ← Reads mapping + ndjson files, inserts into target DB
  config/     ← Loads config.yaml, reads env vars
```

Each pipeline step writes files into a backup directory (`{dbname}_{timestamp}/`). The next step reads those files as input — steps are decoupled by the filesystem, not function calls.

### Key data flow

- `inspect.Schema` → serialized to `schema.yaml`
- `backup.Summary` → serialized to `backup-manifest.yaml`; skipped tables (no rows) are recorded here so `analyze` can auto-exclude them
- `analyze.Mapping` → serialized to `mapping.yaml`; intended to be hand-edited before transfer (set `skip: true`, change `order`, remap `target` names)
- `transfer` reads `mapping.yaml` + `*.ndjson`, inserts via `DBAdapter.InsertRows` in configurable chunk batches

### DBAdapter interface

`internal/adapters/adapter.go` defines the `DBAdapter` interface. Only PostgreSQL (`adapters/postgres.go`) is implemented. New engines plug in by implementing this interface and adding a case in `cmd/inspect.go:newAdapterFromConn`.

### Configuration

`config.yaml` sets `source.engine`, `output.directory`, and `ignored_tables`. Connection strings are always passed via environment variables — never in the config file:

- `DBSYNC_SOURCE_CONN` — used by `inspect` and `backup`
- `DBSYNC_TARGET_CONN` — used by `analyze` and `transfer`

### Transfer field mapping

During transfer, only fields with `status: matched` in `mapping.yaml` are copied. The field map (`source → target`) is built per-table in `transfer.transferTable`. Unmatched fields are silently dropped.

### Scanner buffer

`transfer.transferTable` uses a `bufio.Scanner` with a 10 MB per-line buffer to handle large JSON rows. If rows grow larger, this limit needs adjusting.
