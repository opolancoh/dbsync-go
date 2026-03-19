package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/restore"
	"github.com/spf13/cobra"
)

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore data from backup into target database using mapping.yaml",
	RunE:  runRestore,
}

var (
	restoreConfigPath   string
	restoreBackupDir    string
	restoreChunkSize    int
	restoreTruncate     bool
	restoreNoInterrupt  bool
)

func init() {
	restoreCmd.Flags().StringVar(&restoreConfigPath, "config", "", "path to config file (required)")
	restoreCmd.Flags().StringVar(&restoreBackupDir, "backup", "", "path to the backup directory (required)")
	restoreCmd.Flags().IntVar(&restoreChunkSize, "chunk", 500, "number of rows per insert batch")
	restoreCmd.Flags().BoolVar(&restoreTruncate, "truncate", false, "truncate target tables before inserting")
	restoreCmd.Flags().BoolVar(&restoreNoInterrupt, "no-interrupt", false, "continue restoring remaining tables if one fails")
	restoreCmd.MarkFlagRequired("config")
	restoreCmd.MarkFlagRequired("backup")
}

func runRestore(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(restoreConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(false, true); err != nil {
		return err
	}
	if err := validateDir(restoreBackupDir, "backup directory"); err != nil {
		return err
	}
	if err := validateFile(restoreBackupDir+"/mapping.yaml", "mapping.yaml"); err != nil {
		return err
	}

	mapping, err := restore.LoadMapping(restoreBackupDir)
	if err != nil {
		return fmt.Errorf("loading mapping: %w", err)
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
	fmt.Printf("  Backup:           %s\n", restoreBackupDir)
	fmt.Printf("  Chunk size:       %d\n", restoreChunkSize)
	fmt.Printf("  No-interrupt:     %v\n", restoreNoInterrupt)
	if restoreTruncate {
		fmt.Printf("  ⚠  Truncate:      target tables will be truncated before inserting\n")
	}
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()

	toRestore, toSkip := restore.SortedPlan(mapping)
	restore.PrintPlan(toRestore, toSkip)
	fmt.Println()

	fmt.Print("Continue? (yes/no): ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
		fmt.Println("Aborted.")
		return nil
	}
	fmt.Println()

	ctx := context.Background()

	targetAdapter, err := newAdapterFromConn(ctx, cfg.Source.Engine, cfg.TargetConn)
	if err != nil {
		return fmt.Errorf("connecting to target database: %w", err)
	}
	defer targetAdapter.Close(ctx)

	opts := restore.Options{
		ChunkSize:   restoreChunkSize,
		Truncate:    restoreTruncate,
		NoInterrupt: restoreNoInterrupt,
	}

	summary, err := restore.Run(ctx, targetAdapter, mapping, restoreBackupDir, opts)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	restore.Print(summary)

	return nil
}
