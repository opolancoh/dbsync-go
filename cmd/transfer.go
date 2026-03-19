package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/transfer"
	"github.com/spf13/cobra"
)

var transferCmd = &cobra.Command{
	Use:   "transfer",
	Short: "Transfer data from backup into target database using mapping.yaml",
	RunE:  runTransfer,
}

var (
	transferConfigPath  string
	transferBackupDir   string
	transferChunkSize   int
	transferTruncate    bool
	transferNoInterrupt bool
)

func init() {
	transferCmd.Flags().StringVar(&transferConfigPath, "config", "", "path to config file (required)")
	transferCmd.Flags().StringVar(&transferBackupDir, "backup", "", "path to the backup directory (required)")
	transferCmd.Flags().IntVar(&transferChunkSize, "chunk", 500, "number of rows per insert batch")
	transferCmd.Flags().BoolVar(&transferTruncate, "truncate", false, "truncate target tables before inserting")
	transferCmd.Flags().BoolVar(&transferNoInterrupt, "no-interrupt", false, "continue transferring remaining tables if one fails")
	transferCmd.MarkFlagRequired("config")
	transferCmd.MarkFlagRequired("backup")
}

func runTransfer(cmd *cobra.Command, args []string) error {
	printStep(4)

	cfg, err := config.Load(transferConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(false, true); err != nil {
		return err
	}
	if err := validateDir(transferBackupDir, "backup directory"); err != nil {
		return err
	}
	if err := validateFile(transferBackupDir+"/mapping.yaml", "mapping.yaml"); err != nil {
		return err
	}

	mapping, err := transfer.LoadMapping(transferBackupDir)
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
	fmt.Printf("  Backup:           %s\n", transferBackupDir)
	fmt.Printf("  Chunk size:       %d\n", transferChunkSize)
	fmt.Printf("  No-interrupt:     %v\n", transferNoInterrupt)
	if transferTruncate {
		fmt.Printf("  ⚠  Truncate:      target tables will be truncated before inserting\n")
	}
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()

	toTransfer, toSkip := transfer.SortedPlan(mapping)
	transfer.PrintPlan(toTransfer, toSkip)
	fmt.Println()

	fmt.Print("Continue? (yes/no): ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "yes" && answer != "y" {
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

	opts := transfer.Options{
		ChunkSize:   transferChunkSize,
		Truncate:    transferTruncate,
		NoInterrupt: transferNoInterrupt,
	}

	fmt.Println("Transferring tables...")
	fmt.Println()

	summary, err := transfer.Run(ctx, targetAdapter, mapping, transferBackupDir, opts)
	if err != nil {
		return fmt.Errorf("transfer failed: %w", err)
	}

	transfer.Print(summary)

	return nil
}
