package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/extract"
	"github.com/opolancoh/dbsync/internal/inspect"
	"github.com/spf13/cobra"
)

var extractCmd = &cobra.Command{
	Use:   "extract",
	Short: "Export table data to NDJSON files",
	RunE:  runExtract,
}

var (
	extractConfigPath string
	extractDir        string
)

func init() {
	extractCmd.Flags().StringVar(&extractConfigPath, "config", "", "path to config file (required)")
	extractCmd.Flags().StringVar(&extractDir, "dir", "", "path to the root backup directory produced by inspect (required)")
	extractCmd.MarkFlagRequired("config")
	extractCmd.MarkFlagRequired("dir")
}

func runExtract(cmd *cobra.Command, args []string) error {
	printStep(2)

	cfg, err := config.Load(extractConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(true, false); err != nil {
		return err
	}
	if err := validateDir(extractDir, "backup directory"); err != nil {
		return err
	}
	if err := validateFile(filepath.Join(extractDir, "01-inspect", "schema.yaml"), "schema.yaml"); err != nil {
		return err
	}

	schema, err := inspect.LoadSchema(filepath.Join(extractDir, "01-inspect"))
	if err != nil {
		return fmt.Errorf("loading schema: %w", err)
	}

	fmt.Println("Parameters")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Engine:         %s\n", cfg.Source.Engine)
	fmt.Printf("  Host:           %s\n", schema.Host)
	fmt.Printf("  Database:       %s\n", schema.Database)
	fmt.Printf("  Output dir:     %s\n", cfg.Output.Directory)
	fmt.Printf("  Schema:         %s\n", filepath.Join(extractDir, "01-inspect", "schema.yaml"))
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()

	ctx := context.Background()

	adapter, err := newAdapter(ctx, cfg)
	if err != nil {
		return err
	}
	defer adapter.Close(ctx)

	outputDir := filepath.Join(extractDir, "02-extract")

	fmt.Println("Exporting tables...")
	fmt.Println()

	manifest, err := extract.Run(ctx, adapter, schema, outputDir, func(table string, rows int, skipped bool) {
		if skipped {
			fmt.Printf("  - %-40s no rows, skipped\n", table)
		} else {
			fmt.Printf("  ✓ %-40s %d rows\n", table, rows)
		}
	})
	if err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}

	totalRows := 0
	for _, t := range manifest.Tables {
		totalRows += t.Rows
	}
	fmt.Println()
	fmt.Println("Extract Summary")
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
	fmt.Printf("  Saved to: %s\n", filepath.Join(outputDir, "extract-manifest.yaml"))
	fmt.Println("─────────────────────────────────────────────────────")

	return nil
}
