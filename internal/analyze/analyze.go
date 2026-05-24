package analyze

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
	Source string         `yaml:"source"`
	Target *string        `yaml:"target"`
	Status string         `yaml:"status"`
	Skip   bool           `yaml:"skip"`
	Order  int            `yaml:"order"`
	Fields []FieldMapping `yaml:"fields"`
}

type Mapping struct {
	AnalyzedAt string         `yaml:"analyzed_at"`
	Tables     []TableMapping `yaml:"tables"`
}

func Run(ctx context.Context, adapter adapters.DBAdapter, schema *inspect.Schema, backupDir string, skippedTables []string) (*Mapping, error) {
	skippedSet := make(map[string]bool, len(skippedTables))
	for _, t := range skippedTables {
		skippedSet[t] = true
	}
	targetTables, err := adapter.ListTables(ctx)
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
		AnalyzedAt: time.Now().UTC().Format(time.RFC3339),
	}

	order := 1
	for _, sourceTable := range schema.Tables {
		if skippedSet[sourceTable.Name] {
			continue
		}

		tableMapping := TableMapping{
			Source: sourceTable.Name,
			Order:  order,
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

	if err := save(mapping, backupDir); err != nil {
		return nil, err
	}

	return mapping, nil
}


func save(mapping *Mapping, backupDir string) error {
	data, err := yaml.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("marshaling mapping: %w", err)
	}

	path := filepath.Join(backupDir, "mapping.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing mapping.yaml: %w", err)
	}

	return nil
}
