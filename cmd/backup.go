package main

import (
	"context"
	"fmt"
	"net/url"
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

	adapter, err := adapters.NewPostgresAdapter(ctx, cfg.SourceConn)
	if err != nil {
		return fmt.Errorf("connecting to source: %w", err)
	}
	defer adapter.Close(ctx)

	dbName := dbNameFromConnStr(cfg.SourceConn)
	host := hostFromConnStr(cfg.SourceConn)
	timestamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	backupDir := filepath.Join(cfg.Output.Directory, timestamp+"_"+dbName)

	fmt.Printf("Source:  %s @ %s\n", dbName, host)
	fmt.Printf("Output:  %s\n\n", backupDir)

	fmt.Println("Inspecting schema...")
	schema, err := inspect.Run(ctx, adapter, cfg.Source.Engine, host, dbName, filepath.Join(backupDir, "01-inspect"), cfg.IgnoredTables)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	fmt.Printf("  %d tables found\n\n", len(schema.Tables))

	fmt.Println("Extracting data...")
	manifest, err := extract.Run(ctx, adapter, schema, filepath.Join(backupDir, "02-extract"), func(table string, rows int, skipped bool) {
		if skipped {
			fmt.Printf("  - %-40s no rows, skipped\n", table)
		} else {
			fmt.Printf("  ✓ %-40s %d rows\n", table, rows)
		}
	})
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	totalRows := 0
	for _, t := range manifest.Tables {
		totalRows += t.Rows
	}

	fmt.Printf("\nDone.\n")
	fmt.Printf("  Tables exported: %d (%d rows)\n", len(manifest.Tables), totalRows)
	if len(manifest.SkippedTables) > 0 {
		fmt.Printf("  Tables skipped:  %d (no rows)\n", len(manifest.SkippedTables))
	}
	fmt.Printf("  Output:          %s\n", backupDir)

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
