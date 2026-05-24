package extract

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

type Summary struct {
	CreatedAt     string       `yaml:"created_at"`
	Engine        string       `yaml:"engine"`
	Host          string       `yaml:"host"`
	Database      string       `yaml:"database"`
	Tables        []TableEntry `yaml:"tables"`
	SkippedTables []string     `yaml:"skipped_tables,omitempty"`
}

func LoadSummary(dir string) (*Summary, error) {
	data, err := os.ReadFile(filepath.Join(dir, "extract-manifest.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reading extract-manifest.yaml: %w", err)
	}
	var manifest Summary
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing extract-manifest.yaml: %w", err)
	}
	return &manifest, nil
}

func Run(ctx context.Context, adapter adapters.DBAdapter, schema *inspect.Schema, outputDir string, onProgress func(table string, rows int, skipped bool)) (*Summary, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("creating output directory: %w", err)
	}

	manifest := &Summary{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Engine:    schema.Engine,
		Host:      schema.Host,
		Database:  schema.Database,
	}

	for _, table := range schema.Tables {
		rows, err := exportTable(ctx, adapter, table.Name, outputDir)
		if err != nil {
			return nil, fmt.Errorf("exporting table %s: %w", table.Name, err)
		}

		if rows == 0 {
			manifest.SkippedTables = append(manifest.SkippedTables, table.Name)
			if onProgress != nil {
				onProgress(table.Name, 0, true)
			}
			continue
		}

		manifest.Tables = append(manifest.Tables, TableEntry{
			Name: table.Name,
			Rows: rows,
			File: table.Name + ".ndjson",
		})

		if onProgress != nil {
			onProgress(table.Name, rows, false)
		}
	}

	if err := saveManifest(manifest, outputDir); err != nil {
		return nil, err
	}

	return manifest, nil
}

func exportTable(ctx context.Context, adapter adapters.DBAdapter, table, outputDir string) (int, error) {
	filePath := filepath.Join(outputDir, table+".ndjson")
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

func saveManifest(manifest *Summary, outputDir string) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}

	path := filepath.Join(outputDir, "extract-manifest.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing extract-manifest.yaml: %w", err)
	}

	return nil
}
