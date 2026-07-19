package compare

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/opolancoh/dbsync/internal/adapters"
	"github.com/opolancoh/dbsync/internal/inspect"
	"gopkg.in/yaml.v3"
)

const (
	StatusMatched   = "matched"
	StatusUnmatched = "unmatched"
)

type FieldMapping struct {
	Source     string  `yaml:"source"`
	Target     *string `yaml:"target"`
	SourceType string  `yaml:"source_type"`
	TargetType *string `yaml:"target_type"`
	Status     string  `yaml:"status"`
}

type TableMapping struct {
	Source     string         `yaml:"source"`
	Target     *string        `yaml:"target"`
	Status     string         `yaml:"status"`
	Skip       bool           `yaml:"skip"`
	Order      int            `yaml:"order"`
	PrimaryKey []string       `yaml:"primary_key,omitempty"`
	Fields     []FieldMapping `yaml:"fields"`
}

type Mapping struct {
	ComparedAt string `yaml:"compared_at"`
	// OrderingCycles lists tables caught in a foreign-key cycle, whose order had
	// to be chosen arbitrarily. They may need manual reordering or a deferred
	// constraint.
	OrderingCycles []string       `yaml:"ordering_cycles,omitempty"`
	Tables         []TableMapping `yaml:"tables"`
}

// orderTables sorts tables so that a table referenced by a foreign key is
// transferred before the table referencing it. The incoming sequence (schema
// order from config, then table name) is preserved wherever foreign keys don't
// dictate otherwise, so config order still governs independent tables.
//
// Edges are keyed on target names because the target database is where the
// constraints are actually enforced at insert time.
func orderTables(tables []TableMapping, fks []adapters.ForeignKey) ([]TableMapping, []string) {
	n := len(tables)
	idx := make(map[string]int, n)
	for i, t := range tables {
		if t.Target != nil {
			idx[*t.Target] = i
		}
	}

	// parents[i] holds the tables that must be transferred before tables[i].
	parents := make([]map[int]bool, n)
	for i := range parents {
		parents[i] = make(map[int]bool)
	}
	for _, fk := range fks {
		child, childOK := idx[fk.Table]
		parent, parentOK := idx[fk.RefTable]
		if !childOK || !parentOK || child == parent {
			continue
		}
		parents[child][parent] = true
	}

	done := make([]bool, n)
	ordered := make([]TableMapping, 0, n)
	var cycles []string

	for len(ordered) < n {
		// Take the earliest table whose dependencies are already satisfied,
		// which keeps the original sequence as the tie-break.
		picked := -1
		for i := 0; i < n && picked < 0; i++ {
			if done[i] {
				continue
			}
			ready := true
			for p := range parents[i] {
				if !done[p] {
					ready = false
					break
				}
			}
			if ready {
				picked = i
			}
		}

		if picked < 0 {
			// Every remaining table is in a cycle; no order satisfies them, so
			// take the earliest and record it for the user to resolve.
			for i := 0; i < n && picked < 0; i++ {
				if !done[i] {
					picked = i
				}
			}
			cycles = append(cycles, tables[picked].Source)
		}

		done[picked] = true
		ordered = append(ordered, tables[picked])
	}

	for i := range ordered {
		ordered[i].Order = i + 1
	}
	return ordered, cycles
}

func Run(ctx context.Context, adapter adapters.DBAdapter, schema *inspect.Schema, outputDir string, skippedTables []string) (*Mapping, error) {
	skippedSet := make(map[string]bool, len(skippedTables))
	for _, t := range skippedTables {
		skippedSet[t] = true
	}
	// Look for source schemas by the same name on the target; a differing target
	// schema is expressed by hand-editing `target:` in mapping.yaml.
	targetSchemas := make([]string, 0, len(schema.Schemas))
	for _, s := range schema.Schemas {
		if s.Included {
			targetSchemas = append(targetSchemas, s.Name)
		}
	}
	targetTables, err := adapter.ListTables(ctx, targetSchemas)
	if err != nil {
		return nil, fmt.Errorf("listing target tables: %w", err)
	}

	targetTableSet := make(map[string]bool, len(targetTables))
	for _, t := range targetTables {
		targetTableSet[t] = true
	}

	targetColumns := make(map[string][]adapters.Column)
	for _, t := range targetTables {
		cols, err := adapter.DescribeTable(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("describing target table %s: %w", t, err)
		}
		targetColumns[t] = cols
	}

	mapping := &Mapping{
		ComparedAt: time.Now().UTC().Format(time.RFC3339),
	}

	order := 1
	for _, sourceTable := range schema.Tables {
		if skippedSet[sourceTable.Name] {
			continue
		}

		tableMapping := TableMapping{
			Source:     sourceTable.Name,
			Order:      order,
			PrimaryKey: sourceTable.PrimaryKey,
		}
		order++

		if !targetTableSet[sourceTable.Name] {
			tableMapping.Status = StatusUnmatched
			tableMapping.Target = nil
			mapping.Tables = append(mapping.Tables, tableMapping)
			continue
		}

		targetName := sourceTable.Name
		tableMapping.Target = &targetName
		tableMapping.Status = StatusMatched

		targetColMap := make(map[string]adapters.Column)
		for _, col := range targetColumns[sourceTable.Name] {
			targetColMap[col.Name] = col
		}

		for _, sourceField := range sourceTable.Fields {
			fieldMapping := FieldMapping{
				Source:     sourceField.Name,
				SourceType: sourceField.Type,
			}

			targetCol, found := targetColMap[sourceField.Name]
			if !found {
				fieldMapping.Status = StatusUnmatched
				fieldMapping.Target = nil
				fieldMapping.TargetType = nil
				tableMapping.Status = StatusUnmatched
			} else {
				fieldMapping.Target = &targetCol.Name
				fieldMapping.TargetType = &targetCol.Type
				if sourceField.Type == targetCol.Type {
					fieldMapping.Status = StatusMatched
				} else {
					fieldMapping.Status = StatusUnmatched
					tableMapping.Status = StatusUnmatched
				}
			}

			tableMapping.Fields = append(tableMapping.Fields, fieldMapping)
		}

		mapping.Tables = append(mapping.Tables, tableMapping)
	}

	fks, err := adapter.ListForeignKeys(ctx, targetSchemas)
	if err != nil {
		return nil, fmt.Errorf("listing target foreign keys: %w", err)
	}
	mapping.Tables, mapping.OrderingCycles = orderTables(mapping.Tables, fks)

	if err := save(mapping, outputDir); err != nil {
		return nil, err
	}

	if len(mapping.Tables) == 0 {
		return nil, fmt.Errorf("nothing to transfer: all %d source tables were empty at extract time", len(schema.Tables))
	}

	return mapping, nil
}

func Save(mapping *Mapping, outputDir string) error {
	return save(mapping, outputDir)
}

func save(mapping *Mapping, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	data, err := yaml.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("marshaling mapping: %w", err)
	}

	path := filepath.Join(outputDir, "mapping.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing mapping.yaml: %w", err)
	}

	return nil
}
