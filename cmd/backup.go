package main

import (
	"context"
	"fmt"

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

	cfg.Print(schema.Host, schema.Database, "Schema", backupDir+"/schema.yaml")

	ctx := context.Background()

	adapter, err := newAdapter(ctx, cfg)
	if err != nil {
		return err
	}
	defer adapter.Close(ctx)

	fmt.Println("Exporting tables...")
	fmt.Println()

	manifest, err := backup.Run(ctx, adapter, schema, backupDir)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	backup.Print(manifest, backupDir)

	return nil
}
