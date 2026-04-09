package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool and provides read-only queries.
type DB struct {
	pool *pgxpool.Pool
}

// TableColumn describes a single column.
type TableColumn struct {
	Name     string
	DataType string
	Nullable bool
	IsPK     bool
}

// Page holds a page of rows with offset-based pagination metadata.
type Page struct {
	Columns []TableColumn
	Rows    [][]any
	HasNext bool
	HasPrev bool
}

// TableQuery describes a paginated SELECT query with optional filter and sort.
type TableQuery struct {
	Schema   string
	Table    string
	Where    string // raw SQL predicate, e.g. "status = 'active'"
	OrderBy  string // raw SQL order clause, e.g. "created_at DESC"
	PageSize int
	Offset   int
}

func NewDB(ctx context.Context, connURL string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(connURL)
	if err != nil {
		slog.Error("invalid connection URL", "error", err)
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cc := cfg.ConnConfig
	slog.Info("connecting to database",
		"host", cc.Host, "port", cc.Port,
		"database", cc.Database, "user", cc.User)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		slog.Error("connection failed", "error", err,
			"host", cc.Host, "port", cc.Port)
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		slog.Error("ping failed", "error", err,
			"host", cc.Host, "port", cc.Port)
		return nil, fmt.Errorf("ping: %w", err)
	}
	slog.Info("connected to database",
		"host", cc.Host, "port", cc.Port,
		"database", cc.Database, "user", cc.User)
	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

// Schemas returns all non-system schema names.
func (db *DB) Schemas(ctx context.Context) ([]string, error) {
	slog.Debug("loading schemas")
	rows, err := db.pool.Query(ctx, `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'pg_catalog', 'pg_toast')
		  AND schema_name NOT LIKE 'pg\_temp\_%'
		  AND schema_name NOT LIKE 'pg\_toast\_temp\_%'
		ORDER BY schema_name`)
	if err != nil {
		slog.Error("schemas query failed", "error", err)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slog.Info("loaded schemas", "count", len(schemas))
	return schemas, nil
}

// Tables returns all table names in a schema.
func (db *DB) Tables(ctx context.Context, schema string) ([]string, error) {
	slog.Debug("loading tables", "schema", schema)
	rows, err := db.pool.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = $1
		  AND table_type = 'BASE TABLE'
		ORDER BY table_name`, schema)
	if err != nil {
		slog.Error("tables query failed", "schema", schema, "error", err)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slog.Info("loaded tables", "schema", schema, "count", len(tables))
	return tables, nil
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

// FetchPage runs a TableQuery and returns the result page.
func (db *DB) FetchPage(ctx context.Context, q TableQuery) (*Page, error) {
	start := time.Now()
	slog.Debug("fetch page",
		"schema", q.Schema, "table", q.Table,
		"where", q.Where, "orderBy", q.OrderBy,
		"offset", q.Offset, "pageSize", q.PageSize)

	cols, err := db.Columns(ctx, q.Schema, q.Table)
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	// Default sort by PK columns when no custom order is specified.
	if q.OrderBy == "" {
		q.OrderBy = defaultOrder(cols)
	}

	query := q.Build()
	slog.Debug("executing query", "sql", query)

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		slog.Error("query failed", "sql", query, "error", err)
		return nil, fmt.Errorf("query: %w", err)
	}
	allRows, err := collectRows(rows)
	if err != nil {
		return nil, err
	}

	// We fetched pageSize+1; the extra row tells us if there's a next page.
	hasNext := len(allRows) > q.PageSize
	if hasNext {
		allRows = allRows[:q.PageSize]
	}

	page := &Page{
		Columns: cols,
		Rows:    allRows,
		HasPrev: q.Offset > 0,
		HasNext: hasNext,
	}

	slog.Info("page fetched",
		"schema", q.Schema, "table", q.Table,
		"rows", len(allRows),
		"duration", time.Since(start),
	)
	return page, nil
}

func collectRows(rows pgx.Rows) ([][]any, error) {
	defer rows.Close()
	var result [][]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		result = append(result, vals)
	}
	return result, rows.Err()
}

// defaultOrder returns an ORDER BY clause from PK columns, or empty string.
func defaultOrder(cols []TableColumn) string {
	var pks []string
	for _, c := range cols {
		if c.IsPK {
			pks = append(pks, pgx.Identifier{c.Name}.Sanitize())
		}
	}
	return strings.Join(pks, ", ")
}

// Build returns a SELECT query that fetches pageSize+1 rows (the extra
// row is used to detect whether a next page exists).
func (q TableQuery) Build() string {
	fqTable := pgx.Identifier{q.Schema, q.Table}.Sanitize()

	where := ""
	if q.Where != "" {
		where = " WHERE " + q.Where
	}

	orderBy := ""
	if q.OrderBy != "" {
		orderBy = " ORDER BY " + q.OrderBy
	}

	return fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d",
		fqTable, where, orderBy, q.PageSize+1, q.Offset)
}
