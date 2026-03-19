# dbsync

CLI tool to export data from a relational database and restore it into a target database that may have a different schema.

---

## How it works

Four sequential commands, each producing output files that feed into the next step:

```
inspect  →  backup  →  analyze  →  restore
```

| Step      | What it does                                                    | Output                                       |
|-----------|-----------------------------------------------------------------|----------------------------------------------|
| `inspect` | Reads the source DB schema and saves a snapshot                 | `schema.yaml`                                |
| `backup`  | Exports all table data to files                                 | `backup-summary.yaml` + `.ndjson` files      |
| `analyze` | Compares source schema against target DB and produces a mapping | `mapping.yaml`                               |
| `restore` | Migrates data into the target DB using the mapping              | `restore-summary.yaml`, `restore-errors.log` |

---

## Building

Requires [Go 1.21+](https://golang.org/dl/).

All commands below must be run from the root of the repository.

```bash
# Install dependencies
go mod tidy

# Compiles the source code into a single executable binary named "dbsync" inside bin/
# -o bin/dbsync: name and location of the output binary; ./cmd: the package that contains main.go (entry point)
go build -o bin/dbsync ./cmd

# Build for a specific platform (GOOS=target OS, GOARCH=target CPU architecture)
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

## Running on another machine

No source code or Go installation is needed. Copy the binary and a `config.yaml` to the same folder on the target machine:

```
some-folder/
  dbsync        ← binary built for the target platform
  config.yaml   ← tool configuration
```

Set the required environment variables and run:

```bash
# macOS / Linux
export DBSYNC_SOURCE_CONN="postgres://user:password@host:5432/dbname"
./dbsync inspect --config ./config.yaml

# Windows
set DBSYNC_SOURCE_CONN=postgres://user:password@host:5432/dbname
dbsync.exe inspect --config ./config.yaml
```

Make sure to build the binary for the correct platform before copying (see build commands above).

---

## Configuration

### `config.yaml`

Create a `config.yaml` from the provided example:

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

`ignored_tables` is the list of tables skipped in every step. This file contains no credentials and is safe to commit.

### Environment variables

Connection strings are sensitive and must be set as environment variables — never in `config.yaml`.

| Variable             | Required for                        | Description                         |
|----------------------|-------------------------------------|-------------------------------------|
| `DBSYNC_SOURCE_CONN` | `inspect`, `backup`, `analyze`      | Connection string for the source DB |
| `DBSYNC_TARGET_CONN` | `analyze`, `restore`                | Connection string for the target DB |

Set them in the system:

```bash
export DBSYNC_SOURCE_CONN="postgres://user:password@host:5432/dbname"
export DBSYNC_TARGET_CONN="postgres://user:password@target-host:5432/target-dbname"
```

Or inline per command:

```bash
DBSYNC_SOURCE_CONN="postgres://user:password@host:5432/dbname" ./dbsync inspect --config ./config.yaml
```

---

## Step 1 — Inspect

Connects to the source database, discovers all tables (excluding `ignored_tables`), and records the schema (table names, field names, field types).

```bash
dbsync inspect --config ./config.yaml
```

Creates a timestamped directory named `{dbname}_{timestamp}` inside the configured output directory and saves `schema.yaml` there.

**Output printed to screen:**

```
Parameters
─────────────────────────────────────────────────────
  Engine:         postgres
  Host:           192.168.1.100:5432
  Database:       spenbify_db
  Output dir:     ./backups
  Ignored tables: audit_logs
─────────────────────────────────────────────────────

Results
─────────────────────────────────────────────────────
  Captured at:  2026-03-19T02:00:00Z
  Tables found: 8

  expenses (5 fields)
    id                             uuid                 not null
    tenant_id                      uuid                 not null
    amount                         numeric              not null
    date                           timestamp            not null
    description                    text                 nullable

─────────────────────────────────────────────────────
  Schema saved to: ./backups/spenbify_db_2026-03-19T02-00-00Z/schema.yaml
─────────────────────────────────────────────────────
```

Review `schema.yaml` to confirm the tables and fields look correct before running `backup`.

---

## Step 2 — Backup

Exports every table listed in `schema.yaml` to NDJSON files. Requires a prior `inspect` run. The `--backup` flag must point to the directory created by `inspect`.

Tables with no rows are skipped — no empty files are created.

```bash
dbsync backup --config ./config.yaml --backup ./backups/spenbify_db_2026-03-19T02-00-00Z
```

**Output files:**

```
backups/
  spenbify_db_2026-03-19T02-00-00Z/
    schema.yaml
    backup-summary.yaml
    tenants.ndjson
    users.ndjson
    expenses.ndjson
    ...
```

**Output printed to screen:**

```
Parameters
─────────────────────────────────────────────────────
  Engine:         postgres
  Host:           192.168.1.100:5432
  Database:       spenbify_db
  Output dir:     ./backups
  Schema:         ./backups/spenbify_db_2026-03-19T02-00-00Z/schema.yaml
─────────────────────────────────────────────────────

Exporting tables...

  Exporting expenses...
  ✓ expenses — 4821 rows

  Exporting audit_sessions...
  - audit_sessions — no rows, skipped


Backup Summary
─────────────────────────────────────────────────────
  Created at:   2026-03-19T02:00:00Z
  Tables:       8
  Total rows:   5103

    expenses                                 4821 rows
    tenants                                  12 rows
    ...

  Skipped (no rows):
    audit_sessions

─────────────────────────────────────────────────────
  Saved to: ./backups/spenbify_db_2026-03-19T02-00-00Z/backup-summary.yaml
─────────────────────────────────────────────────────
```

Review `backup-summary.yaml` to confirm all expected tables and row counts are present before proceeding.

---

## Step 3 — Analyze

Connects to the **target** database, reads its schema, and compares it against `schema.yaml` from the backup. Produces `mapping.yaml` which drives the restore step.

```bash
dbsync analyze --config ./config.yaml --backup ./backups/spenbify_db_2026-03-19T02-00-00Z
```

The output is printed to screen and saved as `mapping.yaml` inside the backup directory. Entries are auto-populated where table and field names match exactly. Mismatches are flagged as `unmatched`.

**Example `mapping.yaml`:**

```yaml
analyzed_at: "2026-03-19T03:00:00Z"
tables:
  - source: expenses
    target: expenses
    status: matched
    fields:
      - { source: id,          target: id,     source_type: uuid,    target_type: uuid,    status: matched   }
      - { source: amount,      target: amount, source_type: numeric, target_type: numeric, status: matched   }
      - { source: cat_id,      target: null,   source_type: uuid,    target_type: null,    status: unmatched }
      - { source: description, target: notes,  source_type: text,    target_type: text,    status: unmatched }

  - source: monthly_configs
    target: null   # table removed in target schema, will be skipped
    status: unmatched
    fields: []
```

**Editing `mapping.yaml`:**

- For `unmatched` fields: set `target` to the correct field name in the target DB, or leave `null` to skip it
- For `unmatched` tables: set `target` to the correct table name, or leave `null` to skip the entire table
- You can add comments to document your decisions

---

## Step 4 — Restore

Reads `mapping.yaml` and the NDJSON files and inserts data into the target database. The target DB must already have migrations applied before running this step.

```bash
# Basic restore
dbsync restore --config ./config.yaml --backup ./backups/spenbify_db_2026-03-19T02-00-00Z --mapping ./backups/spenbify_db_2026-03-19T02-00-00Z/mapping.yaml

# Custom chunk size
dbsync restore --config ./config.yaml --backup ./backups/spenbify_db_2026-03-19T02-00-00Z --mapping ./mapping.yaml --chunk 1000

# Non-interrupted mode (do not pause on errors, for automation)
dbsync restore --config ./config.yaml --backup ./backups/spenbify_db_2026-03-19T02-00-00Z --mapping ./mapping.yaml --no-interrupt

# Delete existing rows in target tables before inserting
dbsync restore --config ./config.yaml --backup ./backups/spenbify_db_2026-03-19T02-00-00Z --mapping ./mapping.yaml --truncate
```

**Parameters:**

| Flag             | Default | Description                                                       |
|------------------|---------|-------------------------------------------------------------------|
| `--chunk`        | `500`   | Number of rows inserted per batch                                 |
| `--no-interrupt` | `false` | Do not pause on errors — log and continue                         |
| `--truncate`     | `false` | Delete all existing rows from each target table before inserting  |

**Before any data is touched**, the tool prints the active parameters and mapping, then asks for confirmation:

```
Parameters
─────────────────────────────────────────────────────
  Engine:         postgres
  Host:           192.168.1.100:5432
  Database:       spenbify_db
  Backup:         ./backups/spenbify_db_2026-03-19T02-00-00Z
  Mapping:        ./mapping.yaml
  Chunk size:     500 rows
  On error:       interrupted (pause and ask)
  Truncate:       no
─────────────────────────────────────────────────────

Table mapping:
  expenses        →  expenses         (8 fields mapped, 1 skipped)
  tenants         →  tenants          (5 fields mapped, 0 skipped)
  monthly_configs →  (skipped — no target mapped)
  users           →  app_users        (6 fields mapped, 2 skipped)

─────────────────────────────────────────────────────
Proceed? [y/N]
```

**Output files:**

```
backups/
  spenbify_db_2026-03-19T02-00-00Z/
    restore-summary.yaml      ← always created
    restore-errors.log        ← created only if errors occurred
```

---

## Scheduling

The tool has no built-in scheduler. Use cron, Task Scheduler, or a CI pipeline job to run `inspect` and `backup` on a schedule.

Example cron (daily at 2 AM):

```bash
0 2 * * * /usr/local/bin/dbsync inspect --config /etc/dbsync/config.yaml && /usr/local/bin/dbsync backup --config /etc/dbsync/config.yaml --backup $(ls -td /etc/dbsync/backups/*/ | head -1) >> /var/log/dbsync.log 2>&1
# assumes the binary was installed to /usr/local/bin via: mv bin/dbsync /usr/local/bin/dbsync
```

---

## Supported Databases

| Engine       | Status    |
|--------------|-----------|
| PostgreSQL   | Supported |
| SQL Server   | Planned   |
