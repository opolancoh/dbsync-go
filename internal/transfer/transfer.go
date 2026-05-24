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

type Options struct {
	ChunkSize   int
	Truncate    bool
	NoInterrupt bool
	OnProgress  func(table string, rows int, skipped bool, err error)
}

type TableResult struct {
	Name    string
	Rows    int
	Skipped bool
	Reason  string
	Err     error
}

type Summary struct {
	TransferredAt string
	Tables        []TableResult
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
	summary := &Summary{
		TransferredAt: time.Now().UTC().Format(time.RFC3339),
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
			opts.OnProgress(t.Source, 0, true, nil)
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
				opts.OnProgress(tableMapping.Source, 0, true, nil)
			}
			continue
		}

		if opts.Truncate {
			if err := adapter.TruncateTable(ctx, *tableMapping.Target); err != nil {
				if !opts.NoInterrupt {
					return nil, fmt.Errorf("truncating table %s: %w", tableMapping.Source, err)
				}
				summary.Tables = append(summary.Tables, TableResult{Name: *tableMapping.Target, Err: err})
				if opts.OnProgress != nil {
					opts.OnProgress(tableMapping.Source, 0, false, err)
				}
				continue
			}
		}

		rows, err := transferTable(ctx, adapter, tableMapping, ndjsonPath, opts.ChunkSize)
		if err != nil {
			if !opts.NoInterrupt {
				return nil, fmt.Errorf("transferring table %s: %w", tableMapping.Source, err)
			}
			summary.Tables = append(summary.Tables, TableResult{Name: *tableMapping.Target, Err: err})
			if opts.OnProgress != nil {
				opts.OnProgress(tableMapping.Source, 0, false, err)
			}
			continue
		}

		summary.Tables = append(summary.Tables, TableResult{
			Name: *tableMapping.Target,
			Rows: rows,
		})
		if opts.OnProgress != nil {
			opts.OnProgress(tableMapping.Source, rows, false, nil)
		}
	}

	return summary, nil
}

func transferTable(ctx context.Context, adapter adapters.DBAdapter, tableMapping compare.TableMapping, ndjsonPath string, chunkSize int) (int, error) {
	fieldMap := make(map[string]string)
	for _, f := range tableMapping.Fields {
		if f.Status == compare.StatusMatched && f.Target != nil {
			fieldMap[f.Source] = *f.Target
		}
	}

	f, err := os.Open(ndjsonPath)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", ndjsonPath, err)
	}
	defer f.Close()

	var chunk []map[string]any
	totalRows := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		var sourceRow map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &sourceRow); err != nil {
			return 0, fmt.Errorf("parsing row: %w", err)
		}

		targetRow := make(map[string]any, len(fieldMap))
		for srcField, tgtField := range fieldMap {
			if val, ok := sourceRow[srcField]; ok {
				targetRow[tgtField] = val
			}
		}

		chunk = append(chunk, targetRow)
		if len(chunk) >= chunkSize {
			if err := adapter.InsertRows(ctx, *tableMapping.Target, chunk); err != nil {
				return 0, err
			}
			totalRows += len(chunk)
			chunk = chunk[:0]
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("reading ndjson: %w", err)
	}

	if len(chunk) > 0 {
		if err := adapter.InsertRows(ctx, *tableMapping.Target, chunk); err != nil {
			return 0, err
		}
		totalRows += len(chunk)
	}

	return totalRows, nil
}

