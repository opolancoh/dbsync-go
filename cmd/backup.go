package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/opolancoh/dbsync/internal/adapters"
	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/extract"
	"github.com/opolancoh/dbsync/internal/inspect"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Export all table data from the source database to NDJSON files",
	RunE:  runBackup,
}

var backupConfigPath string

func init() {
	backupCmd.Flags().StringVar(&backupConfigPath, "config", "./config.yaml", "Path to config.yaml")
}

func runBackup(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(backupConfigPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(true, false); err != nil {
		return err
	}

	ctx := context.Background()

	dbName := dbNameFromConnStr(cfg.SourceConn)
	host := hostFromConnStr(cfg.SourceConn)
	startedAt := time.Now().UTC()
	backupDir := filepath.Join(cfg.Output.Directory, startedAt.Format("2006-01-02T15-04-05Z")+"_"+dbName)

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("creating backup directory: %w", err)
	}

	logPath := filepath.Join(backupDir, "backup.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}
	defer logFile.Close()

	// write prints to both stdout and the log file simultaneously
	write := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		fmt.Print(msg)
		fmt.Fprint(logFile, msg)
	}

	write("Backup Log\n")
	write("==========\n")
	write("Started at:  %s\n", startedAt.Format(time.RFC3339))
	write("Source:      %s @ %s\n", dbName, host)
	write("Output:      %s\n\n", backupDir)

	adapter, err := adapters.NewPostgresAdapter(ctx, cfg.SourceConn)
	if err != nil {
		write("ERROR connecting to source: %s\n", err)
		return fmt.Errorf("connecting to source: %w", err)
	}
	defer adapter.Close(ctx)

	write("INSPECT\n")
	schema, err := inspect.Run(ctx, adapter, cfg.Source.Engine, host, dbName, filepath.Join(backupDir, "01-inspect"), cfg.IgnoredTables, cfg.Source.Schemas)
	if err != nil {
		write("  ERROR: %s\n", err)
		return fmt.Errorf("inspect: %w", err)
	}

	write("  Schemas found in %s:\n", dbName)
	skippedSchemas := 0
	for _, s := range schema.Schemas {
		switch {
		case s.Included:
			write("    %-20s %3d tables   will migrate (order %d)\n", s.Name, s.Tables, s.Order)
		case s.Tables == 0:
			// Nothing is being left behind, so this is not a skip worth acting on.
			write("    %-20s %3d tables   empty, nothing to migrate\n", s.Name, s.Tables)
		default:
			skippedSchemas++
			write("    %-20s %3d tables   WILL BE SKIPPED (not in source.schemas)\n", s.Name, s.Tables)
		}
	}
	if skippedSchemas > 0 {
		write("  %d schema(s) will be skipped; add them to source.schemas in config.yaml to include them\n", skippedSchemas)
	}
	if len(cfg.Source.Schemas) == 0 {
		write("  (all schemas included; set source.schemas in config.yaml to choose and order them)\n")
	}
	write("  %d tables found\n", len(schema.Tables))
	if len(cfg.IgnoredTables) > 0 {
		write("  Ignored: %s\n", strings.Join(cfg.IgnoredTables, ", "))
	}
	write("\n")

	write("EXTRACT\n")
	manifest, err := extract.Run(ctx, adapter, schema, filepath.Join(backupDir, "02-extract"), func(table string, rows int, skipped bool) {
		if skipped {
			write("  - %-40s no rows, skipped\n", table)
		} else {
			write("  ✓ %-40s %d rows\n", table, rows)
		}
	})
	if err != nil {
		write("  ERROR: %s\n", err)
		return fmt.Errorf("extract: %w", err)
	}

	totalRows := 0
	for _, t := range manifest.Tables {
		totalRows += t.Rows
	}

	write("\nSUMMARY\n")
	write("  Tables exported: %d (%d rows)\n", len(manifest.Tables), totalRows)
	if len(manifest.SkippedTables) > 0 {
		write("  Tables skipped:  %d (no rows)\n", len(manifest.SkippedTables))
	}
	write("  Completed at:    %s\n", time.Now().UTC().Format(time.RFC3339))
	write("  Log:             %s\n", logPath)

	return nil
}

func dbNameFromConnStr(connString string) string {
	u, err := url.Parse(connString)
	if err != nil {
		return "unknown"
	}
	return strings.TrimPrefix(u.Path, "/")
}

func hostFromConnStr(connString string) string {
	u, err := url.Parse(connString)
	if err != nil {
		return "unknown"
	}
	return u.Host
}
