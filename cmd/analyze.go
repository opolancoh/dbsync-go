package main

import (
	"context"
	"fmt"

	"github.com/opolancoh/dbsync/internal/analyze"
	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/inspect"
	"github.com/spf13/cobra"
)

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Compare source schema against target DB and produce mapping.yaml",
	RunE:  runAnalyze,
}

var (
	analyzeConfigPath string
	analyzeBackupDir  string
)

func init() {
	analyzeCmd.Flags().StringVar(&analyzeConfigPath, "config", "", "path to config file (required)")
	analyzeCmd.Flags().StringVar(&analyzeBackupDir, "backup", "", "path to the backup directory produced by inspect (required)")
	analyzeCmd.MarkFlagRequired("config")
	analyzeCmd.MarkFlagRequired("backup")
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(analyzeConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(false, true); err != nil {
		return err
	}
	if err := validateDir(analyzeBackupDir, "backup directory"); err != nil {
		return err
	}
	if err := validateFile(analyzeBackupDir+"/schema.yaml", "schema.yaml"); err != nil {
		return err
	}

	schema, err := inspect.LoadSchema(analyzeBackupDir)
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
	fmt.Printf("  Schema:           %s/schema.yaml\n", analyzeBackupDir)
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()

	ctx := context.Background()

	targetAdapter, err := newAdapterFromConn(ctx, cfg.Source.Engine, cfg.TargetConn)
	if err != nil {
		return fmt.Errorf("connecting to target database: %w", err)
	}
	defer targetAdapter.Close(ctx)

	mapping, err := analyze.Run(ctx, targetAdapter, schema, analyzeBackupDir)
	if err != nil {
		return fmt.Errorf("analyze failed: %w", err)
	}

	analyze.Print(mapping, analyzeBackupDir)

	return nil
}
