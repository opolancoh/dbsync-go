# dbsync

CLI tool to export data from a relational database and transfer it into a target database that may have a different schema.

---

## How it works

Four sequential commands, each producing output files that feed into the next step:

```
inspect  →  backup  →  analyze  →  transfer
```

| Step       | What it does                                                     | Output                                  |
|------------|------------------------------------------------------------------|-----------------------------------------|
| `inspect`  | Reads the source DB schema and saves a snapshot                  | `schema.yaml`                           |
| `backup`   | Exports all table data to NDJSON files                           | `backup-summary.yaml` + `.ndjson` files |
| `analyze`  | Compares source schema against target DB and produces a mapping  | `mapping.yaml`                          |
| `transfer` | Copies data into the target DB using the mapping                 | —                                       |

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

`ignored_tables` lists tables that are skipped in every step. This file contains no credentials and is safe to commit.

### Environment variables

Connection strings are sensitive and must be set as environment variables — never in `config.yaml`.

| Variable             | Required for              | Description                          |
|----------------------|---------------------------|--------------------------------------|
| `DBSYNC_SOURCE_CONN` | `inspect`, `backup`       | Connection string for the source DB  |
| `DBSYNC_TARGET_CONN` | `analyze`, `transfer`     | Connection string for the target DB  |

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

Connects to the source database, reads all table names and their column definitions (name, type, nullability), and saves the result as `schema.yaml`. Tables listed in `ignored_tables` are excluded.

This step is the starting point of every workflow. The output directory is named `{dbname}_{timestamp}` and is created inside the configured output directory.

```bash
DBSYNC_SOURCE_CONN="postgres://user:password@host:5432/dbname" \
./dbsync inspect --config ./config.yaml
```

**Output files:**

```
backups/
  spenbify_2026-03-19T02-00-00Z/
    schema.yaml
```

**Output printed to screen:**

```
Step 1 of 4: Inspect
─────────────────────────────────────────────────────

Parameters
─────────────────────────────────────────────────────
  Engine:           postgres
  Host:             192.168.1.100:5432
  Database:         spenbify
  Output dir:       ./backups
  Ignored tables:   audit_logs
─────────────────────────────────────────────────────

Results
─────────────────────────────────────────────────────
  Captured at:  2026-03-19T02:00:00Z
  Tables found: 15

  Tenants (4 fields)
    Id                             uuid                 not null
    Name                           text                 not null
    CreatedAt                      timestamp with time zone  not null
    UpdatedAt                      timestamp with time zone  nullable

  ...

─────────────────────────────────────────────────────
  Schema saved to: ./backups/spenbify_2026-03-19T02-00-00Z/schema.yaml
─────────────────────────────────────────────────────
```

Review `schema.yaml` to confirm all expected tables and fields are present before running `backup`.

---

## Step 2 — Backup

Reads `schema.yaml` from the inspect output and exports every table's data to a separate NDJSON file (one JSON object per line). Tables with no rows are skipped — no empty files are created. A `backup-summary.yaml` is saved with row counts and a list of skipped tables.

This step requires a prior `inspect` run. The `--backup` flag must point to the directory created by `inspect`.

```bash
DBSYNC_SOURCE_CONN="postgres://user:password@host:5432/dbname" \
./dbsync backup --config ./config.yaml --backup ./backups/spenbify_2026-03-19T02-00-00Z
```

**Output files:**

```
backups/
  spenbify_2026-03-19T02-00-00Z/
    schema.yaml
    backup-summary.yaml
    Tenants.ndjson
    AspNetUsers.ndjson
    Expenses.ndjson
    ...
```

**Output printed to screen:**

```
Step 2 of 4: Backup
─────────────────────────────────────────────────────

Parameters
─────────────────────────────────────────────────────
  Engine:     postgres
  Host:       192.168.1.100:5432
  Database:   spenbify
  Schema:     ./backups/spenbify_2026-03-19T02-00-00Z/schema.yaml
─────────────────────────────────────────────────────

Exporting tables...

  Exporting Tenants...
  ✓ Tenants — 2 rows

  Exporting Expenses...
  ✓ Expenses — 4821 rows

  Exporting AspNetRoleClaims...
  - AspNetRoleClaims — no rows, skipped


Backup Summary
─────────────────────────────────────────────────────
  Created at:   2026-03-19T02:05:00Z
  Tables:       13
  Total rows:   5103

    Tenants                                  2 rows
    AspNetUsers                              12 rows
    Expenses                                 4821 rows
    ...

  Skipped (no rows):
    AspNetRoleClaims
    AspNetUserClaims

─────────────────────────────────────────────────────
  Saved to: ./backups/spenbify_2026-03-19T02-00-00Z/backup-summary.yaml
─────────────────────────────────────────────────────
```

Review `backup-summary.yaml` to confirm all expected tables and row counts are present before proceeding.

---

## Step 3 — Analyze

Connects to the **target** database, reads its schema, and compares it against `schema.yaml` from the backup. For each source table and field, it checks whether a matching table/field exists in the target with the same name and type.

The result is saved as `mapping.yaml` inside the backup directory. This file is the input for the `transfer` step and is designed to be reviewed and edited before proceeding. Tables that had no rows (listed in `backup-summary.yaml`) are excluded from the mapping automatically.

This step requires `DBSYNC_TARGET_CONN` and a prior `backup` run.

```bash
DBSYNC_TARGET_CONN="postgres://user:password@target-host:5432/target-dbname" \
./dbsync analyze --config ./config.yaml --backup ./backups/spenbify_2026-03-19T02-00-00Z
```

**Output printed to screen:**

```
Step 3 of 4: Analyze
─────────────────────────────────────────────────────

Parameters
─────────────────────────────────────────────────────
  Target engine:    postgres
  Target host:      192.168.1.100:5432
  Target database:  spenbify_v2
  Schema:           ./backups/spenbify_2026-03-19T02-00-00Z/schema.yaml
─────────────────────────────────────────────────────

Analysis Summary
─────────────────────────────────────────────────────
  Analyzed at:       2026-03-19T03:00:00Z
  Tables matched:    13
  Tables unmatched:  0

  ✓ [ 1] Tenants                        → Tenants
  ✓ [ 2] AspNetRoles                    → AspNetRoles
  ✓ [ 3] AspNetUsers                    → AspNetUsers
  ...

─────────────────────────────────────────────────────
  Mapping saved to: ./backups/spenbify_2026-03-19T02-00-00Z/mapping.yaml
─────────────────────────────────────────────────────
```

**Editing `mapping.yaml` before transfer:**

The number in brackets is the transfer order. Tables with the same order number are sorted alphabetically. You can edit this file to:

- **Control transfer order** — set `order` to define which tables are transferred first (useful for foreign key dependencies)
- **Skip a table** — set `skip: true` to exclude it from the transfer
- **Remap a table** — change `target` to a different table name in the target DB
- **Remap a field** — change a field's `target` to a different column name

```yaml
analyzed_at: "2026-03-19T03:00:00Z"
tables:
  - source: Tenants
    target: Tenants
    status: matched
    skip: false
    order: 1
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
    fields: ...
```

---

## Step 4 — Transfer

Reads `mapping.yaml` and the NDJSON files and copies data into the target database. Tables are transferred in the order defined by the `order` field in `mapping.yaml`. Tables with the same order value are sorted alphabetically.

The target DB must already have the schema (migrations) applied before running this step. Before any data is written, the tool prints the parameters and the transfer plan and asks for confirmation.

```bash
# Basic transfer
DBSYNC_TARGET_CONN="postgres://user:password@target-host:5432/target-dbname" \
./dbsync transfer --config ./config.yaml --backup ./backups/spenbify_2026-03-19T02-00-00Z

# Custom chunk size (rows per insert batch)
DBSYNC_TARGET_CONN="..." \
./dbsync transfer --config ./config.yaml --backup ./backups/spenbify_2026-03-19T02-00-00Z --chunk 1000

# Continue on error instead of stopping
DBSYNC_TARGET_CONN="..." \
./dbsync transfer --config ./config.yaml --backup ./backups/spenbify_2026-03-19T02-00-00Z --no-interrupt

# Truncate target tables before inserting
DBSYNC_TARGET_CONN="..." \
./dbsync transfer --config ./config.yaml --backup ./backups/spenbify_2026-03-19T02-00-00Z --truncate
```

**Flags:**

| Flag             | Default | Description                                                      |
|------------------|---------|------------------------------------------------------------------|
| `--chunk`        | `500`   | Number of rows inserted per batch                                |
| `--no-interrupt` | `false` | Continue transferring remaining tables if one fails              |
| `--truncate`     | `false` | Delete all existing rows from each target table before inserting |

**Output printed to screen:**

```
Step 4 of 4: Transfer
─────────────────────────────────────────────────────

Parameters
─────────────────────────────────────────────────────
  Target engine:    postgres
  Target host:      192.168.1.100:5432
  Target database:  spenbify_v2
  Backup:           ./backups/spenbify_2026-03-19T02-00-00Z
  Chunk size:       500
  No-interrupt:     false
─────────────────────────────────────────────────────

Transfer Plan
─────────────────────────────────────────────────────
  Tables to transfer (12):
    [ 1] Tenants
    [ 2] AspNetRoles
    [ 3] AspNetUsers
    [ 4] Expenses
    ...

  Tables skipped (1):
    - UserSessions (skipped)
─────────────────────────────────────────────────────

Continue? (yes/no): yes

Transferring tables...

  ✓ Tenants                           2 rows
  ✓ AspNetRoles                       3 rows
  ✓ AspNetUsers                       12 rows
  ✓ Expenses                          4821 rows
  - UserSessions                      skipped (skipped)
  ...

Transfer Summary
─────────────────────────────────────────────────────
  Transferred at:     2026-03-19T04:00:00Z
  Tables transferred: 12
  Tables skipped:     1
  Total rows:         5103
─────────────────────────────────────────────────────
```

---

## Scheduling

The tool has no built-in scheduler. Use cron, Task Scheduler, or a CI pipeline job to run `inspect` and `backup` on a schedule.

Example cron (daily at 2 AM):

```bash
0 2 * * * DBSYNC_SOURCE_CONN="..." /usr/local/bin/dbsync inspect --config /etc/dbsync/config.yaml && \
          DBSYNC_SOURCE_CONN="..." /usr/local/bin/dbsync backup --config /etc/dbsync/config.yaml \
          --backup $(ls -td /etc/dbsync/backups/*/ | head -1) >> /var/log/dbsync.log 2>&1
```

---

## Supported Databases

| Engine     | Status    |
|------------|-----------|
| PostgreSQL | Supported |
| SQL Server | Planned   |
