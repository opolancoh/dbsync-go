package adapters

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type PostgresAdapter struct {
	conn *pgx.Conn
}

func NewPostgresAdapter(ctx context.Context, connString string) (*PostgresAdapter, error) {
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	return &PostgresAdapter{conn: conn}, nil
}

func (a *PostgresAdapter) ListTables(ctx context.Context) ([]string, error) {
	rows, err := a.conn.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning table name: %w", err)
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func (a *PostgresAdapter) DescribeTable(ctx context.Context, table string) ([]Column, error) {
	rows, err := a.conn.Query(ctx, `
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = $1
		ORDER BY ordinal_position
	`, table)
	if err != nil {
		return nil, fmt.Errorf("describing table %s: %w", table, err)
	}
	defer rows.Close()

	var columns []Column
	for rows.Next() {
		var name, dataType, isNullable string
		if err := rows.Scan(&name, &dataType, &isNullable); err != nil {
			return nil, fmt.Errorf("scanning column: %w", err)
		}
		columns = append(columns, Column{
			Name:     name,
			Type:     dataType,
			Nullable: isNullable == "YES",
		})
	}
	return columns, rows.Err()
}

func (a *PostgresAdapter) ExportTable(ctx context.Context, table string, fn func(row map[string]any) error) error {
	rows, err := a.conn.Query(ctx, fmt.Sprintf("SELECT * FROM %q", table))
	if err != nil {
		return fmt.Errorf("querying table %s: %w", table, err)
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return fmt.Errorf("reading row: %w", err)
		}
		row := make(map[string]any, len(fields))
		for i, f := range fields {
			row[string(f.Name)] = normalizeValue(values[i])
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

func normalizeValue(v any) any {
	switch val := v.(type) {
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x", val[0:4], val[4:6], val[6:8], val[8:10], val[10:16])
	default:
		return v
	}
}

func normalizeInsertValue(v any) any {
	arr, ok := v.([]interface{})
	if !ok || len(arr) != 16 {
		return v
	}
	b := make([]byte, 16)
	for i, elem := range arr {
		f, ok := elem.(float64)
		if !ok || f < 0 || f > 255 {
			return v
		}
		b[i] = byte(f)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (a *PostgresAdapter) InsertRows(ctx context.Context, table string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}

	cols := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	colIdents := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, col := range cols {
		colIdents[i] = fmt.Sprintf("%q", col)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	sql := fmt.Sprintf("INSERT INTO %q (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		table,
		strings.Join(colIdents, ", "),
		strings.Join(placeholders, ", "))

	batch := &pgx.Batch{}
	for _, row := range rows {
		vals := make([]any, len(cols))
		for i, col := range cols {
			vals[i] = normalizeInsertValue(row[col])
		}
		batch.Queue(sql, vals...)
	}

	br := a.conn.SendBatch(ctx, batch)
	defer br.Close()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return br.Close()
}

func (a *PostgresAdapter) TruncateTable(ctx context.Context, table string) error {
	_, err := a.conn.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %q CASCADE", table))
	return err
}

func (a *PostgresAdapter) Close(ctx context.Context) error {
	return a.conn.Close(ctx)
}
