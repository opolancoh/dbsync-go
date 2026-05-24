package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opolancoh/dbsync/internal/compare"
	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/extract"
	"github.com/opolancoh/dbsync/internal/inspect"
	"github.com/spf13/cobra"
)

var compareCmd = &cobra.Command{
	Use:   "compare",
	Short: "Compare source schema against target DB and produce mapping.yaml",
	RunE:  runCompare,
}

var (
	compareConfigPath string
	compareRootDir    string
)

func init() {
	compareCmd.Flags().StringVar(&compareConfigPath, "config", "", "path to config file (required)")
	compareCmd.Flags().StringVar(&compareRootDir, "dir", "", "path to the root backup directory (required)")
	compareCmd.MarkFlagRequired("config")
	compareCmd.MarkFlagRequired("dir")
}

func runCompare(cmd *cobra.Command, args []string) error {
	printStep(3)

	cfg, err := config.Load(compareConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(false, true); err != nil {
		return err
	}
	if err := validateDir(compareRootDir, "backup directory"); err != nil {
		return err
	}
	if err := validateFile(filepath.Join(compareRootDir, "01-inspect", "schema.yaml"), "schema.yaml"); err != nil {
		return err
	}

	schema, err := inspect.LoadSchema(filepath.Join(compareRootDir, "01-inspect"))
	if err != nil {
		return fmt.Errorf("loading schema: %w", err)
	}

	targetHost, err := hostFromConn(cfg.TargetConn)
	if err != nil {
		return fmt.Errorf("parsing target connection string: %w", err)
	}
	targetDB, err := dbNameFromConn(cfg.TargetConn)
	if err != nil {
		return fmt.Errorf("parsing target connection string: %w", err)
	}

	fmt.Println("Parameters")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Target engine:    %s\n", cfg.Source.Engine)
	fmt.Printf("  Target host:      %s\n", targetHost)
	fmt.Printf("  Target database:  %s\n", targetDB)
	fmt.Printf("  Schema:           %s\n", filepath.Join(compareRootDir, "01-inspect", "schema.yaml"))
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()

	ctx := context.Background()

	targetAdapter, err := newAdapterFromConn(ctx, cfg.Source.Engine, cfg.TargetConn)
	if err != nil {
		return fmt.Errorf("connecting to target database: %w", err)
	}
	defer targetAdapter.Close(ctx)

	var skippedTables []string
	manifestPath := filepath.Join(compareRootDir, "02-extract", "extract-manifest.yaml")
	if _, err := os.Stat(manifestPath); err == nil {
		manifest, err := extract.LoadSummary(filepath.Join(compareRootDir, "02-extract"))
		if err != nil {
			return fmt.Errorf("loading extract-manifest.yaml: %w", err)
		}
		skippedTables = manifest.SkippedTables
	}

	compareDir := filepath.Join(compareRootDir, "03-compare")
	mapping, err := compare.Run(ctx, targetAdapter, schema, compareDir, skippedTables)
	if err != nil {
		return fmt.Errorf("compare failed: %w", err)
	}

	matched, unmatched := 0, 0
	for _, t := range mapping.Tables {
		if t.Status == compare.StatusMatched {
			matched++
		} else {
			unmatched++
		}
	}
	fmt.Println()
	fmt.Println("Compare Summary")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Compared at:       %s\n", mapping.ComparedAt)
	fmt.Printf("  Tables matched:    %d\n", matched)
	fmt.Printf("  Tables unmatched:  %d\n\n", unmatched)
	for _, t := range mapping.Tables {
		targetName := "(unmatched)"
		if t.Target != nil {
			targetName = *t.Target
		}
		status := "✓"
		if t.Status == compare.StatusUnmatched {
			status = "✗"
		}
		fmt.Printf("  %s [%-2d] %-30s → %s\n", status, t.Order, t.Source, targetName)
		for _, f := range t.Fields {
			if f.Status == compare.StatusUnmatched {
				targetField := "not found in target"
				if f.Target != nil {
					targetField = fmt.Sprintf("type mismatch: %s → %s", f.SourceType, *f.TargetType)
				}
				fmt.Printf("      ✗ %-28s %s\n", f.Source, targetField)
			}
		}
	}
	fmt.Println()
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Mapping saved to: %s\n", filepath.Join(compareRootDir, "03-compare", "mapping.yaml"))
	fmt.Println("─────────────────────────────────────────────────────")

	return nil
}
