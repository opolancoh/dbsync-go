package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/opolancoh/dbsync/internal/backup"
	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/inspect"
	"github.com/spf13/cobra"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Export table data to NDJSON files",
	RunE:  runBackup,
}

var (
	backupConfigPath string
	backupDir        string
)

func init() {
	backupCmd.Flags().StringVar(&backupConfigPath, "config", "", "path to config file (required)")
	backupCmd.Flags().StringVar(&backupDir, "backup", "", "path to the backup directory produced by inspect (required)")
	backupCmd.MarkFlagRequired("config")
	backupCmd.MarkFlagRequired("backup")
}

func runBackup(cmd *cobra.Command, args []string) error {
	printStep(2)

	cfg, err := config.Load(backupConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(true, false); err != nil {
		return err
	}
	if err := validateDir(backupDir, "backup directory"); err != nil {
		return err
	}

	schema, err := inspect.LoadSchema(backupDir)
	if err != nil {
		return fmt.Errorf("loading schema: %w", err)
	}

	fmt.Println("Parameters")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Engine:         %s\n", cfg.Source.Engine)
	fmt.Printf("  Host:           %s\n", schema.Host)
	fmt.Printf("  Database:       %s\n", schema.Database)
	fmt.Printf("  Output dir:     %s\n", cfg.Output.Directory)
	fmt.Printf("  Schema:         %s\n", backupDir+"/schema.yaml")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()

	ctx := context.Background()

	adapter, err := newAdapter(ctx, cfg)
	if err != nil {
		return err
	}
	defer adapter.Close(ctx)

	fmt.Println("Exporting tables...")
	fmt.Println()

	manifest, err := backup.Run(ctx, adapter, schema, backupDir, func(table string, rows int, skipped bool) {
		if skipped {
			fmt.Printf("  - %-40s no rows, skipped\n", table)
		} else {
			fmt.Printf("  ✓ %-40s %d rows\n", table, rows)
		}
	})
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	totalRows := 0
	for _, t := range manifest.Tables {
		totalRows += t.Rows
	}
	fmt.Println()
	fmt.Println("Backup Summary")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Created at:   %s\n", manifest.CreatedAt)
	fmt.Printf("  Tables:       %d\n", len(manifest.Tables))
	fmt.Printf("  Total rows:   %d\n\n", totalRows)
	for _, t := range manifest.Tables {
		fmt.Printf("    %-40s %d rows\n", t.Name, t.Rows)
	}
	if len(manifest.SkippedTables) > 0 {
		fmt.Println()
		fmt.Printf("  Skipped (no rows):\n")
		for _, name := range manifest.SkippedTables {
			fmt.Printf("    %s\n", name)
		}
	}
	fmt.Println()
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Saved to: %s\n", filepath.Join(backupDir, "backup-manifest.yaml"))
	fmt.Println("─────────────────────────────────────────────────────")

	return nil
}
