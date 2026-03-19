package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/opolancoh/dbsync/internal/adapters"
	"github.com/opolancoh/dbsync/internal/inspect"
	"gopkg.in/yaml.v3"
)

type TableEntry struct {
	Name string `yaml:"name"`
	Rows int    `yaml:"rows"`
	File string `yaml:"file"`
}

type Manifest struct {
	CreatedAt     string       `yaml:"created_at"`
	Engine        string       `yaml:"engine"`
	Host          string       `yaml:"host"`
	Database      string       `yaml:"database"`
	Tables        []TableEntry `yaml:"tables"`
	SkippedTables []string     `yaml:"skipped_tables,omitempty"`
}

func Run(ctx context.Context, adapter adapters.DBAdapter, schema *inspect.Schema, backupDir string) (*Manifest, error) {
	manifest := &Manifest{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Engine:    schema.Engine,
		Host:      schema.Host,
		Database:  schema.Database,
	}

	for _, table := range schema.Tables {
		fmt.Printf("  Exporting %s...\n", table.Name)

		rows, err := exportTable(ctx, adapter, table.Name, backupDir)
		if err != nil {
			return nil, fmt.Errorf("exporting table %s: %w", table.Name, err)
		}

		if rows == 0 {
			manifest.SkippedTables = append(manifest.SkippedTables, table.Name)
			fmt.Printf("  - %s — no rows, skipped\n\n", table.Name)
			continue
		}

		manifest.Tables = append(manifest.Tables, TableEntry{
			Name: table.Name,
			Rows: rows,
			File: table.Name + ".ndjson",
		})

		fmt.Printf("  ✓ %s — %d rows\n\n", table.Name, rows)
	}

	if err := saveManifest(manifest, backupDir); err != nil {
		return nil, err
	}

	return manifest, nil
}

func exportTable(ctx context.Context, adapter adapters.DBAdapter, table, backupDir string) (int, error) {
	filePath := filepath.Join(backupDir, table+".ndjson")
	f, err := os.Create(filePath)
	if err != nil {
		return 0, fmt.Errorf("creating file %s: %w", filePath, err)
	}

	count := 0
	err = adapter.ExportTable(ctx, table, func(row map[string]any) error {
		line, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("marshaling row: %w", err)
		}
		if _, err := fmt.Fprintln(f, string(line)); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
		count++
		return nil
	})

	f.Close()

	if count == 0 {
		os.Remove(filePath)
	}

	return count, err
}

func saveManifest(manifest *Manifest, backupDir string) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}

	path := filepath.Join(backupDir, "backup-summary.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing backup-summary.yaml: %w", err)
	}

	return nil
}

func Print(manifest *Manifest, backupDir string) {
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
	fmt.Printf("  Saved to: %s\n", filepath.Join(backupDir, "backup-summary.yaml"))
	fmt.Println("─────────────────────────────────────────────────────")
}
