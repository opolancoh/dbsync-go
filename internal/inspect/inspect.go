package inspect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/opolancoh/dbsync/internal/adapters"
	"gopkg.in/yaml.v3"
)

type Field struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`
	Nullable bool   `yaml:"nullable"`
}

type Table struct {
	Name       string   `yaml:"name"`
	PrimaryKey []string `yaml:"primary_key,omitempty"`
	Fields     []Field  `yaml:"fields"`
}

type Schema struct {
	CapturedAt     string   `yaml:"captured_at"`
	Engine         string   `yaml:"engine"`
	Host           string   `yaml:"host"`
	Database       string   `yaml:"database"`
	ExcludedTables []string `yaml:"excluded_tables,omitempty"`
	Tables         []Table  `yaml:"tables"`
}

func Run(ctx context.Context, adapter adapters.DBAdapter, engine, host, database, outputDir string, excluded []string) (*Schema, error) {
	excludedSet := make(map[string]bool, len(excluded))
	for _, t := range excluded {
		excludedSet[t] = true
	}

	allTables, err := adapter.ListTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}

	schema := &Schema{
		CapturedAt:     time.Now().UTC().Format(time.RFC3339),
		Engine:         engine,
		Host:           host,
		Database:       database,
		ExcludedTables: excluded,
	}

	for _, tableName := range allTables {
		if excludedSet[tableName] {
			continue
		}

		columns, err := adapter.DescribeTable(ctx, tableName)
		if err != nil {
			return nil, fmt.Errorf("describing table %s: %w", tableName, err)
		}

		pk, err := adapter.GetPrimaryKey(ctx, tableName)
		if err != nil {
			return nil, fmt.Errorf("getting primary key for %s: %w", tableName, err)
		}

		table := Table{Name: tableName, PrimaryKey: pk}
		for _, col := range columns {
			table.Fields = append(table.Fields, Field{
				Name:     col.Name,
				Type:     col.Type,
				Nullable: col.Nullable,
			})
		}
		schema.Tables = append(schema.Tables, table)
	}

	if err := save(schema, outputDir); err != nil {
		return nil, err
	}

	return schema, nil
}


func save(schema *Schema, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	data, err := yaml.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshaling schema: %w", err)
	}

	path := filepath.Join(outputDir, "schema.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing schema.yaml: %w", err)
	}

	return nil
}

func OutputPath(outputDir string) string {
	return filepath.Join(outputDir, "schema.yaml")
}

func LoadSchema(backupDir string) (*Schema, error) {
	data, err := os.ReadFile(filepath.Join(backupDir, "schema.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reading schema.yaml: %w", err)
	}

	var schema Schema
	if err := yaml.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("parsing schema.yaml: %w", err)
	}

	return &schema, nil
}
