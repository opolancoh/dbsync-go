# dbsync

CLI tool to export data from a database and transfer it into a target database. Designed to handle schema differences between source and target, making it suitable for data migrations.

---

## How it works

dbsync copies **records** from tables in a source database into a target database through a guided TUI (terminal user interface).

The workflow is four sequential steps, each producing output files that feed into the next:

```
inspect  →  extract  →  compare  →  transfer
```

| Step       | What it does                                                    | Output                                           |
|------------|-----------------------------------------------------------------|--------------------------------------------------|
| `inspect`  | Reads table names, column names, types and nullability          | `01-inspect/schema.yaml`                         |
| `extract`  | Exports all rows from each table into a dedicated NDJSON file   | `02-extract/extract-manifest.yaml` + `.ndjson` files |
| `compare`  | Compares source schema against target DB and produces a mapping | `03-compare/mapping.yaml`                        |
| `transfer` | Copies rows into the target DB tables using the mapping         | `04-transfer/transfer.log`                       |

All steps run inside the TUI — there are no individual subcommands.

---

## Prerequisites

### 1. Get the binary

Download the binary for your platform from the releases page, or ask a developer to provide one. Place it in any folder:

```
some-folder/
  dbsync        ← binary compiled for the target platform
  config.yaml   ← tool configuration (see below)
```

To build from source, see the [Building](#building) section.

### 2. Create `config.yaml`

Copy the provided example and edit it:

```bash
cp config.yaml.example config.yaml
```

```yaml
source:
  engine: postgres

output:
  directory: ./backups

ignored_tables:
  - audit_logs
```

`ignored_tables` lists tables excluded during `inspect`. Since they never appear in `schema.yaml`, they are absent from all subsequent steps automatically.

### 3. Run

```bash
./dbsync tui
```

---

## TUI walkthrough

### Config screen

Enter the config file path, source connection string, and target connection string. The tool tests both connections before proceeding. Source and target must be different databases.

```
  Config file path
  ./config.yaml

  Source connection
  postgres://user:password@source-host:5432/dbname

  Target connection
  postgres://user:password@target-host:5432/target-dbname
```

Keys:

```
  [tab] next field
  [enter] confirm field / start
  [ctrl+c] quit
```

### Inspect screen

Connects to the source database and reads all table and column definitions. Output goes into `01-inspect/schema.yaml` inside a timestamped folder named `{timestamp}_{dbname}`.

Keys:

```
  [enter] start extract
  [q] quit
```

### Extract screen

Exports every table's rows to a separate NDJSON file (one JSON object per line). Tables with no rows are skipped — no empty files are created. Results are recorded in `02-extract/extract-manifest.yaml`.

Keys:

```
  [enter] start compare
  [q] quit
```

### Compare screen

Connects to the target database and compares its schema against the source. Shows each table and its match status. Unmatched tables and fields are highlighted and excluded from transfer automatically.

**Reordering tables** — use `+` / `-` to move the selected table up or down before starting the transfer. This controls the transfer order, which matters when tables have foreign key dependencies. The order is saved back to `mapping.yaml` automatically when transfer starts.

**Conflict strategy** — press `c` to cycle through how the transfer handles rows that already exist in the target:

| Strategy  | Behavior                                                                 |
|-----------|--------------------------------------------------------------------------|
| `skip`    | `ON CONFLICT DO NOTHING` — existing rows are silently skipped (default)  |
| `upsert`  | `ON CONFLICT (pk) DO UPDATE SET ...` — existing rows are overwritten     |
| `truncate`| `TRUNCATE TABLE … CASCADE` before insert — all existing rows are deleted |

The `truncate` strategy shows a warning because it deletes all rows in the target tables before inserting. Use it only when a full replacement is intended.

Keys:

```
  [↑↓] select
  [+/-] reorder
  [c] cycle conflict strategy
  [enter] start transfer
  [q] quit
```

### Transfer screen

Inserts rows into the target database table by table, using the selected conflict strategy. Each table shows live status (pending → row count or error). When using `skip`, rows that already exist in the target are silently skipped and counted separately (shown as `⚠ X silently skipped`).

### Done screen

Shows per-table results and a summary: source DB, target DB, tables transferred, rows transferred, tables skipped, and tables failed. A full log is written to `04-transfer/transfer.log`.

If any rows failed (e.g. foreign key violations), the path to `transfer-errors.log` is shown in red. That file contains each failing row's timestamp, table name, error message, and the full JSON record.

Keys:

```
  [r] retry transfer (returns to Compare screen)
  [enter] / [q] exit
```

---

## Output structure

Each run creates a timestamped directory inside the configured output directory:

```
backups/
  2026-05-24T18-00-00Z_mydb/
    01-inspect/
      schema.yaml
    02-extract/
      extract-manifest.yaml
      Tenants.ndjson
      AspNetUsers.ndjson
      Expenses.ndjson
      ...
    03-compare/
      mapping.yaml
    04-transfer/
      transfer.log
      transfer-errors.log   ← only written when row-level errors occur
```

### Editing `mapping.yaml`

The compare step writes `mapping.yaml`, which the TUI reads at transfer time. You can edit it manually while on the Compare screen before pressing enter. Supported edits:

- **Skip a table** — set `skip: true` to exclude it from the transfer
- **Remap a table** — change `target` to a different table name in the target DB
- **Remap a field** — change a field's `target` to a different column name in the target DB
- **Control transfer order** — set `order` directly (the TUI `+`/`-` keys update this automatically)

```yaml
compared_at: "2026-05-24T18-00-00Z"
tables:
  - source: Tenants
    target: Tenants
    status: matched
    skip: false
    order: 1
    primary_key:
      - Id
    fields:
      - source: Id
        target: Id
        source_type: uuid
        target_type: uuid
        status: matched

  - source: UserSessions
    target: UserSessions
    status: matched
    skip: true        # exclude this table from transfer
    order: 5
    primary_key:
      - Id
    fields: ...
```

`primary_key` lists the source table's primary key columns. It is used by the `upsert` conflict strategy to build the `ON CONFLICT (pk) DO UPDATE SET ...` clause.

---

## Backup (scheduled / automated)

The `backup` command runs inspect + extract non-interactively, printing progress to stdout and exiting non-zero on failure. It is designed to be called from a cron job or script — no TUI, no prompts.

### Usage

```bash
DBSYNC_SOURCE_CONN="postgres://user:password@host:5432/dbname" \
./dbsync backup --config ./config.yaml
```

Output is printed to stdout:

```
Source:  mydb @ host:5432

Inspecting schema...
  42 tables found

Extracting data...
  ✓ Tenants                                  12 rows
  ✓ AspNetUsers                              84 rows
  - AuditLogs                                no rows, skipped
  ...

Done.
  Tables exported: 38 (21504 rows)
  Tables skipped:  4 (no rows)
  Output:          ./backups/2026-05-25T02-00-00Z_mydb
```

Each run creates a new timestamped directory with the same structure as the TUI workflow, and a `backup.log` file alongside it:

```
backups/
  2026-05-25T02-00-00Z_mydb/
    01-inspect/
      schema.yaml
    02-extract/
      extract-manifest.yaml
      Tenants.ndjson
      AspNetUsers.ndjson
      ...
  2026-05-25T02-00-00Z_mydb_backup.log
```

`backup.log` records the full run output (same text as stdout), timestamped. These files can be used directly as input for the TUI's compare and transfer steps.

---

### Running as a cron job on Linux

**1. Place the binary and config in a fixed location**

```
/opt/dbsync/
  dbsync        ← the binary
  config.yaml   ← tool configuration
  backups/      ← backup output (set output.directory to this path in config.yaml)
```

**2. Create a wrapper script `/opt/dbsync/backup.sh`**

```bash
#!/bin/bash
set -euo pipefail

export DBSYNC_SOURCE_CONN="postgres://user:password@host:5432/dbname"

/opt/dbsync/dbsync backup --config /opt/dbsync/config.yaml
```

```bash
chmod +x /opt/dbsync/backup.sh
```

**3. Schedule it with cron**

Open the crontab editor:

```bash
crontab -e
```

Add a line to run the backup daily at 2:00 AM and append all output to a log file:

```cron
0 2 * * * /opt/dbsync/backup.sh >> /var/log/dbsync-backup.log 2>&1
```

The job exits non-zero on failure. Most monitoring systems (cron mail, Healthchecks.io, Grafana alerts) can detect this and send a notification.

**4. Rotate old backups**

To keep only the last 30 days of backups, add a cleanup line to `backup.sh` before the dbsync call:

```bash
find /opt/dbsync/backups -maxdepth 1 -mindepth 1 -type d -mtime +30 -exec rm -rf {} +
```

---

## Building

Requires [Go 1.21+](https://golang.org/dl/).

All commands below must be run from the root of the repository.

```bash
# Install dependencies
go mod tidy

# Build the binary
go build -o bin/dbsync ./cmd

# Build for a specific platform
GOOS=linux   GOARCH=amd64 go build -o bin/dbsync-linux  ./cmd  # Linux 64-bit
GOOS=darwin  GOARCH=arm64 go build -o bin/dbsync-mac    ./cmd  # macOS Apple Silicon
GOOS=windows GOARCH=amd64 go build -o bin/dbsync.exe    ./cmd  # Windows 64-bit
```

The binary is output to `bin/`, which is gitignored — binaries are never committed to the repo.

To make the binary available system-wide:

```bash
mv bin/dbsync /usr/local/bin/dbsync
```

---

## Supported Databases

| Engine     | Status    |
|------------|-----------|
| PostgreSQL | Supported |
| SQL Server | Planned   |
