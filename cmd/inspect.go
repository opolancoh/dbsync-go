package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opolancoh/dbsync/internal/adapters"
	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/inspect"
	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Inspect source database schema and save schema.yaml",
	RunE:  runInspect,
}

var inspectConfigPath string

func init() {
	inspectCmd.Flags().StringVar(&inspectConfigPath, "config", "", "path to config file (required)")
	inspectCmd.MarkFlagRequired("config")
}

func runInspect(cmd *cobra.Command, args []string) error {
	printStep(1)

	cfg, err := config.Load(inspectConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(true, false); err != nil {
		return err
	}

	dbName, err := dbNameFromConn(cfg.SourceConn)
	if err != nil {
		return fmt.Errorf("parsing connection string: %w", err)
	}
	host, err := hostFromConn(cfg.SourceConn)
	if err != nil {
		return fmt.Errorf("parsing connection string: %w", err)
	}
	fmt.Println("Parameters")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Engine:         %s\n", cfg.Source.Engine)
	fmt.Printf("  Host:           %s\n", host)
	fmt.Printf("  Database:       %s\n", dbName)
	fmt.Printf("  Output dir:     %s\n", cfg.Output.Directory)
	if len(cfg.IgnoredTables) > 0 {
		fmt.Printf("  Ignored tables: %s\n", strings.Join(cfg.IgnoredTables, ", "))
	}
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()

	ctx := context.Background()

	adapter, err := newAdapter(ctx, cfg)
	if err != nil {
		return err
	}
	defer adapter.Close(ctx)

	outputDir := filepath.Join(cfg.Output.Directory, dbName+"_"+time.Now().UTC().Format("2006-01-02T15-04-05Z"))

	schema, err := inspect.Run(ctx, adapter, cfg.Source.Engine, host, dbName, outputDir, cfg.IgnoredTables)
	if err != nil {
		return fmt.Errorf("inspect failed: %w", err)
	}

	fmt.Println("Results")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Captured at:  %s\n", schema.CapturedAt)
	fmt.Printf("  Tables found: %d\n", len(schema.Tables))
	fmt.Println()
	for _, t := range schema.Tables {
		fmt.Printf("  %s (%d fields)\n", t.Name, len(t.Fields))
		for _, f := range t.Fields {
			nullable := "not null"
			if f.Nullable {
				nullable = "nullable"
			}
			fmt.Printf("    %-30s %-20s %s\n", f.Name, f.Type, nullable)
		}
		fmt.Println()
	}
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Schema saved to: %s\n", inspect.OutputPath(outputDir))
	fmt.Println("─────────────────────────────────────────────────────")

	return nil
}

func dbNameFromConn(connString string) (string, error) {
	u, err := url.Parse(connString)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(u.Path, "/"), nil
}

func hostFromConn(connString string) (string, error) {
	u, err := url.Parse(connString)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

func newAdapter(ctx context.Context, cfg *config.Config) (adapters.DBAdapter, error) {
	return newAdapterFromConn(ctx, cfg.Source.Engine, cfg.SourceConn)
}

func newAdapterFromConn(ctx context.Context, engine, connString string) (adapters.DBAdapter, error) {
	switch engine {
	case "postgres":
		adapter, err := adapters.NewPostgresAdapter(ctx, connString)
		if err != nil {
			return nil, fmt.Errorf("connecting to database: %w", err)
		}
		return adapter, nil
	default:
		fmt.Fprintf(os.Stderr, "unsupported engine: %s\n", engine)
		os.Exit(1)
		return nil, nil
	}
}
