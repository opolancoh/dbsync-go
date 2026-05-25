package transfer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/opolancoh/dbsync/internal/adapters"
	"github.com/opolancoh/dbsync/internal/compare"
	"gopkg.in/yaml.v3"
)

type ConflictStrategy int

const (
	ConflictSkip     ConflictStrategy = iota // ON CONFLICT DO NOTHING
	ConflictUpsert                           // ON CONFLICT (pk) DO UPDATE SET ...
	ConflictTruncate                         // DELETE all rows before inserting
)

type Options struct {
	ConflictStrategy ConflictStrategy
	ChunkSize        int
	NoInterrupt      bool
	OnProgress       func(table string, rows int, conflictRows int, skipped bool, err error)
	OnRowError       func(table string, row map[string]any, err error)
}

type TableResult struct {
	Name         string
	Rows         int
	ConflictRows int
	Skipped      bool
	Reason       string
	Err          error
}

type Summary struct {
	TransferredAt    string
	ConflictStrategy string
	Tables           []TableResult
}

func LoadMapping(backupDir string) (*compare.Mapping, error) {
	data, err := os.ReadFile(filepath.Join(backupDir, "mapping.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reading mapping.yaml: %w", err)
	}
	var mapping compare.Mapping
	if err := yaml.Unmarshal(data, &mapping); err != nil {
		return nil, fmt.Errorf("parsing mapping.yaml: %w", err)
	}
	return &mapping, nil
}

func SortedPlan(mapping *compare.Mapping) (toTransfer, toSkip []compare.TableMapping) {
	tables := make([]compare.TableMapping, len(mapping.Tables))
	copy(tables, mapping.Tables)
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].Order != tables[j].Order {
			return tables[i].Order < tables[j].Order
		}
		return tables[i].Source < tables[j].Source
	})

	for _, t := range tables {
		if t.Skip || t.Status == compare.StatusUnmatched {
			toSkip = append(toSkip, t)
		} else {
			toTransfer = append(toTransfer, t)
		}
	}
	return
}


func Run(ctx context.Context, adapter adapters.DBAdapter, mapping *compare.Mapping, backupDir string, opts Options) (*Summary, error) {
	strategyName := [...]string{"skip", "upsert", "truncate"}[opts.ConflictStrategy]
	summary := &Summary{
		TransferredAt:    time.Now().UTC().Format(time.RFC3339),
		ConflictStrategy: strategyName,
	}

	toTransfer, toSkip := SortedPlan(mapping)

	for _, t := range toSkip {
		reason := "unmatched"
		if t.Skip {
			reason = "skipped"
		}
		summary.Tables = append(summary.Tables, TableResult{
			Name:    t.Source,
			Skipped: true,
			Reason:  reason,
		})
		if opts.OnProgress != nil {
			opts.OnProgress(t.Source, 0, 0, true, nil)
		}
	}

	for _, tableMapping := range toTransfer {
		ndjsonPath := filepath.Join(backupDir, tableMapping.Source+".ndjson")
		if _, err := os.Stat(ndjsonPath); os.IsNotExist(err) {
			summary.Tables = append(summary.Tables, TableResult{
				Name:    tableMapping.Source,
				Skipped: true,
				Reason:  "no data (empty table)",
			})
			if opts.OnProgress != nil {
				opts.OnProgress(tableMapping.Source, 0, 0, true, nil)
			}
			continue
		}

		if opts.ConflictStrategy == ConflictTruncate {
			if err := adapter.TruncateTable(ctx, *tableMapping.Target); err != nil {
				if !opts.NoInterrupt {
					return nil, fmt.Errorf("truncating table %s: %w", tableMapping.Source, err)
				}
				summary.Tables = append(summary.Tables, TableResult{Name: *tableMapping.Target, Err: err})
				if opts.OnProgress != nil {
					opts.OnProgress(tableMapping.Source, 0, 0, false, err)
				}
				continue
			}
		}

		onRowErr := func(row map[string]any, err error) {
			if opts.OnRowError != nil {
				opts.OnRowError(tableMapping.Source, row, err)
			}
		}
		rows, conflictRows, err := transferTable(ctx, adapter, tableMapping, ndjsonPath, opts.ChunkSize, opts.ConflictStrategy, onRowErr)
		if err != nil {
			if !opts.NoInterrupt {
				return nil, fmt.Errorf("transferring table %s: %w", tableMapping.Source, err)
			}
			summary.Tables = append(summary.Tables, TableResult{Name: *tableMapping.Target, Err: err})
			if opts.OnProgress != nil {
				opts.OnProgress(tableMapping.Source, 0, 0, false, err)
			}
			continue
		}

		summary.Tables = append(summary.Tables, TableResult{
			Name:         *tableMapping.Target,
			Rows:         rows,
			ConflictRows: conflictRows,
		})
		if opts.OnProgress != nil {
			opts.OnProgress(tableMapping.Source, rows, conflictRows, false, nil)
		}
	}

	return summary, nil
}

func transferTable(ctx context.Context, adapter adapters.DBAdapter, tableMapping compare.TableMapping, ndjsonPath string, chunkSize int, strategy ConflictStrategy, onRowError func(row map[string]any, err error)) (inserted int, conflict int, err error) {
	fieldMap := make(map[string]string)
	for _, f := range tableMapping.Fields {
		if f.Status == compare.StatusMatched && f.Target != nil {
			fieldMap[f.Source] = *f.Target
		}
	}

	// Build target PK columns for upsert (map source PK names → target names via fieldMap)
	var targetPKCols []string
	if strategy == ConflictUpsert {
		pkSet := make(map[string]bool, len(tableMapping.PrimaryKey))
		for _, pk := range tableMapping.PrimaryKey {
			pkSet[pk] = true
		}
		for _, f := range tableMapping.Fields {
			if f.Status == compare.StatusMatched && f.Target != nil && pkSet[f.Source] {
				targetPKCols = append(targetPKCols, *f.Target)
			}
		}
	}

	f, err := os.Open(ndjsonPath)
	if err != nil {
		return 0, 0, fmt.Errorf("opening %s: %w", ndjsonPath, err)
	}
	defer f.Close()

	var chunk []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	insertChunk := func(c []map[string]any) error {
		var n int64
		var err error
		if strategy == ConflictUpsert && len(targetPKCols) > 0 {
			n, err = adapter.UpsertRows(ctx, *tableMapping.Target, c, targetPKCols)
		} else {
			n, err = adapter.InsertRows(ctx, *tableMapping.Target, c)
		}
		if err != nil {
			identifyRowErrors(ctx, adapter, *tableMapping.Target, c, onRowError)
			return err
		}
		inserted += int(n)
		if strategy != ConflictUpsert {
			conflict += len(c) - int(n)
		}
		return nil
	}

	for scanner.Scan() {
		var sourceRow map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &sourceRow); err != nil {
			return inserted, conflict, fmt.Errorf("parsing row: %w", err)
		}

		targetRow := make(map[string]any, len(fieldMap))
		for srcField, tgtField := range fieldMap {
			if val, ok := sourceRow[srcField]; ok {
				targetRow[tgtField] = val
			}
		}

		chunk = append(chunk, targetRow)
		if len(chunk) >= chunkSize {
			if err := insertChunk(chunk); err != nil {
				return inserted, conflict, err
			}
			chunk = chunk[:0]
		}
	}

	if err := scanner.Err(); err != nil {
		return inserted, conflict, fmt.Errorf("reading ndjson: %w", err)
	}

	if len(chunk) > 0 {
		if err := insertChunk(chunk); err != nil {
			return inserted, conflict, err
		}
	}

	return inserted, conflict, nil
}

// identifyRowErrors retries each row individually after a batch failure to find which ones cause errors.
func identifyRowErrors(ctx context.Context, adapter adapters.DBAdapter, table string, chunk []map[string]any, onRowError func(map[string]any, error)) {
	if onRowError == nil {
		return
	}
	for _, row := range chunk {
		if _, err := adapter.InsertRows(ctx, table, []map[string]any{row}); err != nil {
			onRowError(row, err)
		}
	}
}

