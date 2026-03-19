package adapters

import (
	"context"
	"fmt"

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
			row[string(f.Name)] = values[i]
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (a *PostgresAdapter) InsertRows(ctx context.Context, table string, rows []map[string]any) error {
	// implemented in restore step
	return fmt.Errorf("not implemented")
}

func (a *PostgresAdapter) Close(ctx context.Context) error {
	return a.conn.Close(ctx)
}
