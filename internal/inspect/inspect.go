package inspect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// SchemaEntry records a schema found in the source database and whether it was
// selected for migration.
type SchemaEntry struct {
	Name     string `yaml:"name"`
	Tables   int    `yaml:"tables"`
	Included bool   `yaml:"included"`
	Order    int    `yaml:"order,omitempty"`
}

type Schema struct {
	CapturedAt     string        `yaml:"captured_at"`
	Engine         string        `yaml:"engine"`
	Host           string        `yaml:"host"`
	Database       string        `yaml:"database"`
	Schemas        []SchemaEntry `yaml:"schemas"`
	ExcludedTables []string      `yaml:"excluded_tables,omitempty"`
	Tables         []Table       `yaml:"tables"`
}

// ResolveSchemas reports every non-system schema in the database, marking which
// ones `requested` selects and in what order. When `requested` is empty every
// schema is included alphabetically. Schemas named in `requested` but absent
// from the database are returned separately as `missing`.
func ResolveSchemas(discovered []adapters.SchemaInfo, requested []string) (entries []SchemaEntry, selected []string, missing []string) {
	found := make(map[string]int, len(discovered))
	for _, s := range discovered {
		found[s.Name] = s.Tables
	}

	if len(requested) == 0 {
		for _, s := range discovered {
			entries = append(entries, SchemaEntry{Name: s.Name, Tables: s.Tables, Included: true, Order: len(selected) + 1})
			selected = append(selected, s.Name)
		}
		return entries, selected, nil
	}

	order := make(map[string]int, len(requested))
	for _, name := range requested {
		if _, ok := found[name]; !ok {
			missing = append(missing, name)
			continue
		}
		if _, dup := order[name]; dup {
			continue
		}
		selected = append(selected, name)
		order[name] = len(selected)
	}

	for _, s := range discovered {
		entries = append(entries, SchemaEntry{
			Name:     s.Name,
			Tables:   s.Tables,
			Included: order[s.Name] > 0,
			Order:    order[s.Name],
		})
	}
	return entries, selected, missing
}

func Run(ctx context.Context, adapter adapters.DBAdapter, engine, host, database, outputDir string, excluded []string, schemas []string) (*Schema, error) {
	excludedSet := make(map[string]bool, len(excluded))
	for _, t := range excluded {
		excludedSet[t] = true
	}

	discovered, err := adapter.ListSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing schemas: %w", err)
	}
	entries, selected, missing := ResolveSchemas(discovered, schemas)
	if len(missing) > 0 {
		return nil, fmt.Errorf("source.schemas names schemas not present in the database: %s", strings.Join(missing, ", "))
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no schemas to migrate: the database has no non-system schemas")
	}

	allTables, err := adapter.ListTables(ctx, selected)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}

	schema := &Schema{
		CapturedAt:     time.Now().UTC().Format(time.RFC3339),
		Engine:         engine,
		Host:           host,
		Database:       database,
		Schemas:        entries,
		ExcludedTables: excluded,
	}

	for _, tableName := range allTables {
		// An ignored_tables entry matches either the qualified name
		// ("app.users") or the bare table name ("users") in any schema.
		bare := tableName
		if i := strings.IndexByte(tableName, '.'); i >= 0 {
			bare = tableName[i+1:]
		}
		if excludedSet[tableName] || excludedSet[bare] {
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

	// Save before the emptiness check so schema.yaml is still available to show
	// which schemas were discovered and why nothing was selected.
	if err := save(schema, outputDir); err != nil {
		return nil, err
	}

	if len(schema.Tables) == 0 {
		if len(allTables) > 0 {
			return nil, fmt.Errorf("no tables to migrate: all %d tables in %s were excluded by ignored_tables",
				len(allTables), strings.Join(selected, ", "))
		}
		return nil, fmt.Errorf("no tables to migrate: %s contain no tables",
			strings.Join(selected, ", "))
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
