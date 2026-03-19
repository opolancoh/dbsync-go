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
	"github.com/opolancoh/dbsync/internal/analyze"
	"gopkg.in/yaml.v3"
)

type Options struct {
	ChunkSize   int
	Truncate    bool
	NoInterrupt bool
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

func LoadMapping(backupDir string) (*analyze.Mapping, error) {
	data, err := os.ReadFile(filepath.Join(backupDir, "mapping.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reading mapping.yaml: %w", err)
	}
	var mapping analyze.Mapping
	if err := yaml.Unmarshal(data, &mapping); err != nil {
		return nil, fmt.Errorf("parsing mapping.yaml: %w", err)
	}
	return &mapping, nil
}

func SortedPlan(mapping *analyze.Mapping) (toTransfer, toSkip []analyze.TableMapping) {
	tables := make([]analyze.TableMapping, len(mapping.Tables))
	copy(tables, mapping.Tables)
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].Order != tables[j].Order {
			return tables[i].Order < tables[j].Order
		}
		return tables[i].Source < tables[j].Source
	})

	for _, t := range tables {
		if t.Skip || t.Status == analyze.StatusUnmatched {
			toSkip = append(toSkip, t)
		} else {
			toTransfer = append(toTransfer, t)
		}
	}
	return
}

func PrintPlan(toTransfer, toSkip []analyze.TableMapping) {
	fmt.Println("Transfer Plan")
	fmt.Println("─────────────────────────────────────────────────────")

	fmt.Printf("  Tables to transfer (%d):\n", len(toTransfer))
	for _, t := range toTransfer {
		fmt.Printf("    [%-2d] %s\n", t.Order, t.Source)
	}

	if len(toSkip) > 0 {
		fmt.Printf("\n  Tables skipped (%d):\n", len(toSkip))
		for _, t := range toSkip {
			reason := "unmatched"
			if t.Skip {
				reason = "skipped"
			}
			fmt.Printf("    - %s (%s)\n", t.Source, reason)
		}
	}

	fmt.Println("─────────────────────────────────────────────────────")
}

func Run(ctx context.Context, adapter adapters.DBAdapter, mapping *analyze.Mapping, backupDir string, opts Options) (*Summary, error) {
	summary := &Summary{
		TransferredAt: time.Now().UTC().Format(time.RFC3339),
	}

	toTransfer, toSkip := SortedPlan(mapping)

	for _, t := range toSkip {
		reason := "unmatched"
		if t.Skip {
			reason = "skipped"
		}
		fmt.Printf("  - %-35s skipped (%s)\n", t.Source, reason)
		summary.Tables = append(summary.Tables, TableResult{
			Name:    t.Source,
			Skipped: true,
			Reason:  reason,
		})
	}

	for _, tableMapping := range toTransfer {
		ndjsonPath := filepath.Join(backupDir, tableMapping.Source+".ndjson")
		if _, err := os.Stat(ndjsonPath); os.IsNotExist(err) {
			fmt.Printf("  - %-35s skipped (no data)\n", tableMapping.Source)
			summary.Tables = append(summary.Tables, TableResult{
				Name:    tableMapping.Source,
				Skipped: true,
				Reason:  "no data (empty table)",
			})
			continue
		}

		fmt.Printf("  → Transferring %-25s", tableMapping.Source+"...")

		if opts.Truncate {
			if err := adapter.TruncateTable(ctx, *tableMapping.Target); err != nil {
				fmt.Printf("\r  ✗ %-35s failed: %s\n", tableMapping.Source, err)
				if !opts.NoInterrupt {
					return nil, fmt.Errorf("truncating table %s: %w", tableMapping.Source, err)
				}
				summary.Tables = append(summary.Tables, TableResult{Name: *tableMapping.Target, Err: err})
				continue
			}
		}

		rows, err := transferTable(ctx, adapter, tableMapping, ndjsonPath, opts.ChunkSize)
		if err != nil {
			fmt.Printf("\r  ✗ %-35s failed: %s\n", tableMapping.Source, err)
			if !opts.NoInterrupt {
				return nil, fmt.Errorf("transferring table %s: %w", tableMapping.Source, err)
			}
			summary.Tables = append(summary.Tables, TableResult{Name: *tableMapping.Target, Err: err})
			continue
		}

		fmt.Printf("\r  ✓ %-35s %d rows\n", tableMapping.Source, rows)
		summary.Tables = append(summary.Tables, TableResult{
			Name: *tableMapping.Target,
			Rows: rows,
		})
	}

	return summary, nil
}

func transferTable(ctx context.Context, adapter adapters.DBAdapter, tableMapping analyze.TableMapping, ndjsonPath string, chunkSize int) (int, error) {
	fieldMap := make(map[string]string)
	for _, f := range tableMapping.Fields {
		if f.Status == analyze.StatusMatched && f.Target != nil {
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

func Print(summary *Summary) {
	totalRows := 0
	transferred := 0
	skipped := 0
	failed := 0
	for _, t := range summary.Tables {
		if t.Skipped {
			skipped++
		} else if t.Err != nil {
			failed++
		} else {
			transferred++
			totalRows += t.Rows
		}
	}

	fmt.Println()
	fmt.Println("Transfer Summary")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Transferred at:    %s\n", summary.TransferredAt)
	fmt.Printf("  Tables transferred: %d\n", transferred)
	fmt.Printf("  Tables skipped:    %d\n", skipped)
	if failed > 0 {
		fmt.Printf("  Tables failed:     %d\n", failed)
	}
	fmt.Printf("  Total rows:        %d\n", totalRows)
	fmt.Println("─────────────────────────────────────────────────────")
}
