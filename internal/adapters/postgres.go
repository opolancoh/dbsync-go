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

// splitQualified splits "app.users" into its schema and table parts. A name
// without a dot is assumed to live in the public schema.
func splitQualified(name string) (schema, table string) {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "public", name
}

// quoteQualified renders "app.users" as "app"."users". Applying %q to the whole
// string instead would yield a single identifier literally named `app.users`.
func quoteQualified(name string) string {
	schema, table := splitQualified(name)
	return fmt.Sprintf("%q.%q", schema, table)
}

func (a *PostgresAdapter) ListSchemas(ctx context.Context) ([]SchemaInfo, error) {
	// Driven from pg_namespace, not information_schema.tables, so that schemas
	// holding no tables (an untouched `public`, say) are still reported rather
	// than silently absent. The count comes from information_schema so it stays
	// consistent with ListTables, which is privilege-filtered the same way.
	rows, err := a.conn.Query(ctx, `
		SELECT n.nspname, count(t.table_name)
		FROM pg_namespace n
		LEFT JOIN information_schema.tables t
		       ON t.table_schema = n.nspname
		      AND t.table_type   = 'BASE TABLE'
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_toast%'
		  AND n.nspname NOT LIKE 'pg_temp%'
		  AND n.nspname NOT LIKE 'pg_toast_temp%'
		GROUP BY n.nspname
		ORDER BY n.nspname
	`)
	if err != nil {
		return nil, fmt.Errorf("listing schemas: %w", err)
	}
	defer rows.Close()

	var schemas []SchemaInfo
	for rows.Next() {
		var s SchemaInfo
		if err := rows.Scan(&s.Name, &s.Tables); err != nil {
			return nil, fmt.Errorf("scanning schema: %w", err)
		}
		schemas = append(schemas, s)
	}
	return schemas, rows.Err()
}

// ListTables returns schema-qualified names, ordered by the position of each
// schema in the schemas argument so callers control migration order.
func (a *PostgresAdapter) ListTables(ctx context.Context, schemas []string) ([]string, error) {
	if len(schemas) == 0 {
		return nil, nil
	}
	rows, err := a.conn.Query(ctx, `
		SELECT table_schema, table_name
		FROM information_schema.tables
		WHERE table_schema = ANY($1)
		  AND table_type = 'BASE TABLE'
		ORDER BY array_position($1, table_schema), table_name
	`, schemas)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return nil, fmt.Errorf("scanning table name: %w", err)
		}
		tables = append(tables, schema+"."+name)
	}
	return tables, rows.Err()
}

// ListForeignKeys returns one edge per foreign key between tables in the given
// schemas. Self-references are omitted: they constrain row order within a table,
// which table ordering cannot fix, and would otherwise register as a cycle.
func (a *PostgresAdapter) ListForeignKeys(ctx context.Context, schemas []string) ([]ForeignKey, error) {
	if len(schemas) == 0 {
		return nil, nil
	}
	rows, err := a.conn.Query(ctx, `
		SELECT cn.nspname || '.' || c.relname,
		       pn.nspname || '.' || p.relname
		FROM pg_constraint con
		JOIN pg_class     c  ON c.oid  = con.conrelid
		JOIN pg_namespace cn ON cn.oid = c.relnamespace
		JOIN pg_class     p  ON p.oid  = con.confrelid
		JOIN pg_namespace pn ON pn.oid = p.relnamespace
		WHERE con.contype = 'f'
		  AND cn.nspname = ANY($1)
		  AND pn.nspname = ANY($1)
		  AND c.oid <> p.oid
	`, schemas)
	if err != nil {
		return nil, fmt.Errorf("listing foreign keys: %w", err)
	}
	defer rows.Close()

	var fks []ForeignKey
	for rows.Next() {
		var fk ForeignKey
		if err := rows.Scan(&fk.Table, &fk.RefTable); err != nil {
			return nil, fmt.Errorf("scanning foreign key: %w", err)
		}
		fks = append(fks, fk)
	}
	return fks, rows.Err()
}

func (a *PostgresAdapter) DescribeTable(ctx context.Context, table string) ([]Column, error) {
	schemaName, tableName := splitQualified(table)
	rows, err := a.conn.Query(ctx, `
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = $1
		  AND table_name = $2
		ORDER BY ordinal_position
	`, schemaName, tableName)
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
	rows, err := a.conn.Query(ctx, fmt.Sprintf("SELECT * FROM %s", quoteQualified(table)))
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

func (a *PostgresAdapter) InsertRows(ctx context.Context, table string, rows []map[string]any) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
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
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		quoteQualified(table),
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
	var inserted int64
	for range rows {
		tag, err := br.Exec()
		if err != nil {
			return inserted, err
		}
		inserted += tag.RowsAffected()
	}
	return inserted, br.Close()
}

func (a *PostgresAdapter) GetPrimaryKey(ctx context.Context, table string) ([]string, error) {
	schemaName, tableName := splitQualified(table)
	rows, err := a.conn.Query(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		    ON tc.constraint_name = kcu.constraint_name
		   AND tc.table_schema    = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema    = $1
		  AND tc.table_name      = $2
		ORDER BY kcu.ordinal_position
	`, schemaName, tableName)
	if err != nil {
		return nil, fmt.Errorf("querying primary key for %s: %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, fmt.Errorf("scanning primary key column: %w", err)
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

func (a *PostgresAdapter) UpsertRows(ctx context.Context, table string, rows []map[string]any, pkCols []string) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	cols := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	pkSet := make(map[string]bool, len(pkCols))
	for _, pk := range pkCols {
		pkSet[pk] = true
	}

	colIdents := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, col := range cols {
		colIdents[i] = fmt.Sprintf("%q", col)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	pkIdents := make([]string, len(pkCols))
	for i, pk := range pkCols {
		pkIdents[i] = fmt.Sprintf("%q", pk)
	}

	var updateClauses []string
	for _, col := range cols {
		if !pkSet[col] {
			updateClauses = append(updateClauses, fmt.Sprintf("%q = EXCLUDED.%q", col, col))
		}
	}

	sql := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s",
		quoteQualified(table),
		strings.Join(colIdents, ", "),
		strings.Join(placeholders, ", "),
		strings.Join(pkIdents, ", "),
		strings.Join(updateClauses, ", "),
	)

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
	var affected int64
	for range rows {
		tag, err := br.Exec()
		if err != nil {
			return affected, err
		}
		affected += tag.RowsAffected()
	}
	return affected, br.Close()
}

func (a *PostgresAdapter) TruncateTable(ctx context.Context, table string) error {
	_, err := a.conn.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s CASCADE", quoteQualified(table)))
	return err
}

func (a *PostgresAdapter) Close(ctx context.Context) error {
	return a.conn.Close(ctx)
}
