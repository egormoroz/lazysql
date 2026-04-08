package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool and provides read-only queries.
type DB struct {
	pool *pgxpool.Pool
}

// SchemaTable identifies a table within a schema.
type SchemaTable struct {
	Schema string
	Table  string
}

func (st SchemaTable) String() string { return st.Schema + "." + st.Table }

// TableColumn describes a single column.
type TableColumn struct {
	Name     string
	DataType string
	Nullable bool
	IsPK     bool
}

// KeysetPage holds a page of rows plus navigation cursors.
type KeysetPage struct {
	Columns []TableColumn
	Rows    [][]any      // len <= pageSize
	PKCols  []string     // primary key column names (ordered)
	HasNext bool
	HasPrev bool
	// Cursor values for the first and last row's PK, used for navigation.
	FirstCursor []any
	LastCursor  []any
}

// Direction indicates keyset fetch direction.
type Direction int

const (
	DirForward  Direction = iota
	DirBackward           // fetch page before cursor
	DirFirst              // fetch first page (no cursor)
)

func NewDB(ctx context.Context, connURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, connURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

// Schemas returns all non-system schema names.
func (db *DB) Schemas(ctx context.Context) ([]string, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'pg_catalog', 'pg_toast')
		ORDER BY schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		schemas = append(schemas, s)
	}
	return schemas, rows.Err()
}

// Tables returns all table names in a schema.
func (db *DB) Tables(ctx context.Context, schema string) ([]string, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = $1
		  AND table_type = 'BASE TABLE'
		ORDER BY table_name`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

// Columns returns column metadata for a table.
func (db *DB) Columns(ctx context.Context, schema, table string) ([]TableColumn, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT
			c.column_name,
			c.data_type,
			c.is_nullable = 'YES' AS nullable,
			COALESCE(pk.is_pk, false) AS is_pk
		FROM information_schema.columns c
		LEFT JOIN (
			SELECT kcu.column_name, true AS is_pk
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
			  ON tc.constraint_name = kcu.constraint_name
			 AND tc.table_schema = kcu.table_schema
			WHERE tc.constraint_type = 'PRIMARY KEY'
			  AND tc.table_schema = $1
			  AND tc.table_name = $2
		) pk ON pk.column_name = c.column_name
		WHERE c.table_schema = $1
		  AND c.table_name = $2
		ORDER BY c.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []TableColumn
	for rows.Next() {
		var col TableColumn
		if err := rows.Scan(&col.Name, &col.DataType, &col.Nullable, &col.IsPK); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

// pkColumns returns the ordered PK column names for a table.
// Falls back to a unique index if no PK exists.
func (db *DB) pkColumns(ctx context.Context, schema, table string) ([]string, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(cols) > 0 {
		return cols, nil
	}

	// Fall back to the first unique index.
	return db.firstUniqueIndex(ctx, schema, table)
}

func (db *DB) firstUniqueIndex(ctx context.Context, schema, table string) ([]string, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND i.indisunique
		  AND NOT i.indisprimary
		ORDER BY i.indexrelid, array_position(i.indkey, a.attnum)
		LIMIT 10`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

// FetchPage retrieves a page of rows using keyset pagination.
//
// cursor holds the PK values of the boundary row (last row for forward,
// first row for backward). Pass nil for the first page.
//
// The query always fetches pageSize+1 rows; the extra row is used to
// detect whether another page exists in the given direction, then dropped.
func (db *DB) FetchPage(
	ctx context.Context,
	schema, table string,
	pageSize int,
	dir Direction,
	cursor []any,
) (*KeysetPage, error) {
	pkCols, err := db.pkColumns(ctx, schema, table)
	if err != nil {
		return nil, fmt.Errorf("pk columns: %w", err)
	}
	if len(pkCols) == 0 {
		return nil, fmt.Errorf("table %s.%s has no primary key or unique index", schema, table)
	}

	cols, err := db.Columns(ctx, schema, table)
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	fqTable := pgx.Identifier{schema, table}.Sanitize()
	orderCols := make([]string, len(pkCols))
	for i, c := range pkCols {
		orderCols[i] = pgx.Identifier{c}.Sanitize()
	}

	// Build query.
	var qb strings.Builder
	qb.WriteString("SELECT * FROM ")
	qb.WriteString(fqTable)

	args := make([]any, 0, len(cursor))
	ascending := dir != DirBackward

	if dir != DirFirst && len(cursor) == len(pkCols) {
		// Keyset WHERE clause using row-value comparison.
		qb.WriteString(" WHERE (")
		qb.WriteString(strings.Join(orderCols, ", "))
		qb.WriteString(")")
		if ascending {
			qb.WriteString(" > (")
		} else {
			qb.WriteString(" < (")
		}
		for i := range cursor {
			if i > 0 {
				qb.WriteString(", ")
			}
			args = append(args, cursor[i])
			fmt.Fprintf(&qb, "$%d", i+1)
		}
		qb.WriteString(")")
	}

	qb.WriteString(" ORDER BY ")
	for i, c := range orderCols {
		if i > 0 {
			qb.WriteString(", ")
		}
		qb.WriteString(c)
		if ascending {
			qb.WriteString(" ASC")
		} else {
			qb.WriteString(" DESC")
		}
	}

	fmt.Fprintf(&qb, " LIMIT %d", pageSize+1)

	rows, err := db.pool.Query(ctx, qb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	descs := rows.FieldDescriptions()
	var allRows [][]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		allRows = append(allRows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Detect overflow (has more pages in this direction).
	hasMore := len(allRows) > pageSize
	if hasMore {
		allRows = allRows[:pageSize]
	}

	// Backward fetch returns rows in reverse order — flip them.
	if !ascending {
		for i, j := 0, len(allRows)-1; i < j; i, j = i+1, j-1 {
			allRows[i], allRows[j] = allRows[j], allRows[i]
		}
	}

	page := &KeysetPage{
		Columns: cols,
		Rows:    allRows,
		PKCols:  pkCols,
	}

	// Determine has-next/has-prev based on direction and overflow.
	switch dir {
	case DirFirst:
		page.HasPrev = false
		page.HasNext = hasMore
	case DirForward:
		page.HasPrev = true // we came from somewhere
		page.HasNext = hasMore
	case DirBackward:
		page.HasPrev = hasMore
		page.HasNext = true // we came from somewhere
	}

	// Extract cursors from first and last rows.
	if len(allRows) > 0 {
		page.FirstCursor = extractCursor(allRows[0], descs, pkCols)
		page.LastCursor = extractCursor(allRows[len(allRows)-1], descs, pkCols)
	}

	return page, nil
}

// extractCursor pulls PK column values from a row by matching column names.
func extractCursor(row []any, descs []pgconn.FieldDescription, pkCols []string) []any {
	colIndex := make(map[string]int, len(descs))
	for i, d := range descs {
		colIndex[d.Name] = i
	}
	cursor := make([]any, len(pkCols))
	for i, pk := range pkCols {
		if idx, ok := colIndex[pk]; ok {
			cursor[i] = row[idx]
		}
	}
	return cursor
}
