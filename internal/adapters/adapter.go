package adapters

import "context"

type Column struct {
	Name     string
	Type     string
	Nullable bool
}

type DBAdapter interface {
	ListTables(ctx context.Context) ([]string, error)
	DescribeTable(ctx context.Context, table string) ([]Column, error)
	GetPrimaryKey(ctx context.Context, table string) ([]string, error)
	ExportTable(ctx context.Context, table string, fn func(row map[string]any) error) error
	InsertRows(ctx context.Context, table string, rows []map[string]any) (int64, error)
	UpsertRows(ctx context.Context, table string, rows []map[string]any, pkCols []string) (int64, error)
	TruncateTable(ctx context.Context, table string) error
	Close(ctx context.Context) error
}
