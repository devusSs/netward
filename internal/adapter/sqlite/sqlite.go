// Package sqlite stores netward's data in a SQLite database.
//
// It uses the pure-Go [modernc.org/sqlite] driver, applies its embedded
// [goose] migrations when the database is opened, and runs queries generated
// by [sqlc] from the queries directory. After changing a query or migration,
// run "sqlc generate" in the repository root.
//
// The schema stores timestamps as Unix milliseconds and durations as
// nanoseconds.
//
// [goose]: https://github.com/pressly/goose
// [sqlc]: https://sqlc.dev
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // Registers the "sqlite" database/sql driver.

	"github.com/devusSs/netward/internal/adapter/sqlite/sqlcgen"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is a SQLite-backed repository. It is safe for concurrent use.
type Store struct {
	db      *sql.DB
	queries *sqlcgen.Queries
	logger  *slog.Logger
}

// Open opens the database at path, creating it if needed, and applies pending
// migrations. A nil logger discards logs.
//
// Each connection runs in WAL mode with foreign keys enforced and waits up to
// five seconds for locks. The pool holds a single connection, as SQLite allows
// only one writer at a time.
func Open(ctx context.Context, path string, logger *slog.Logger) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}

	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	logger = logger.With(slog.String("component", "sqlite"))

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	err = db.PingContext(ctx)
	if err == nil {
		err = migrate(ctx, db, logger)
	}

	if err != nil {
		return nil, errors.Join(fmt.Errorf("open database %s: %w", path, err), db.Close())
	}

	logger.InfoContext(ctx, "database opened", slog.String("path", path))

	return &Store{db: db, queries: sqlcgen.New(db), logger: logger}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	err := s.db.Close()
	if err != nil {
		return fmt.Errorf("close database: %w", err)
	}

	return nil
}

func dsn(path string) string {
	query := url.Values{
		"_pragma": {
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
			"foreign_keys(ON)",
			"busy_timeout(5000)",
		},
		// Write transactions take the write lock up front, so two
		// transactions never deadlock upgrading from read to write.
		"_txlock": {"immediate"},
	}

	return "file:" + path + "?" + query.Encode()
}

func migrate(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, fsys, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	for _, result := range results {
		logger.InfoContext(ctx, "migration applied",
			slog.Int64("version", result.Source.Version),
			slog.String("file", result.Source.Path),
			slog.Duration("duration", result.Duration),
		)
	}

	return nil
}
