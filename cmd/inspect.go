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
	cfg.Print(host, dbName)

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

	inspect.Print(schema, inspect.OutputPath(outputDir))

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
	switch cfg.Source.Engine {
	case "postgres":
		adapter, err := adapters.NewPostgresAdapter(ctx, cfg.SourceConn)
		if err != nil {
			return nil, fmt.Errorf("connecting to database: %w", err)
		}
		return adapter, nil
	default:
		fmt.Fprintf(os.Stderr, "unsupported engine: %s\n", cfg.Source.Engine)
		os.Exit(1)
		return nil, nil
	}
}
