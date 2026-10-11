package sqlite_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devusSs/netward/internal/adapter/sqlite"
)

func dbPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "netward.db")
}

func openStore(t *testing.T, path string, logger *slog.Logger) *sqlite.Store {
	t.Helper()

	store, err := sqlite.Open(t.Context(), path, logger)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() { _ = store.Close() })

	return store
}

func textLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		AddSource:   false,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	}))
}

func TestOpen(t *testing.T) {
	t.Parallel()

	path := dbPath(t)

	var logs bytes.Buffer

	store := openStore(t, path, textLogger(&logs))

	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file: %v", err)
	}

	for _, want := range []string{
		`msg="migration applied" component=sqlite version=1 file=00001_create_public_net_snapshots.sql`,
		`msg="database opened" component=sqlite path=` + path,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs = %s\nwant them to contain %s", logs.String(), want)
		}
	}

	var tables int

	err := store.DB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name LIKE 'public_net_%'`,
	).Scan(&tables)
	if err != nil || tables != 3 {
		t.Errorf("public_net tables = %d (%v), want 3", tables, err)
	}
}

func TestOpenPragmas(t *testing.T) {
	t.Parallel()

	db := openStore(t, dbPath(t), nil).DB()

	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"synchronous", "1"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
	}

	for _, tt := range tests {
		var got string

		err := db.QueryRowContext(t.Context(), "PRAGMA "+tt.pragma).Scan(&got)
		if err != nil || got != tt.want {
			t.Errorf("PRAGMA %s = %q (%v), want %q", tt.pragma, got, err, tt.want)
		}
	}
}

func TestOpenExistingDatabase(t *testing.T) {
	t.Parallel()

	path := dbPath(t)

	first, err := sqlite.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}

	if err = first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var logs bytes.Buffer

	openStore(t, path, textLogger(&logs))

	if strings.Contains(logs.String(), "migration applied") {
		t.Errorf("migrations applied again: %s", logs.String())
	}
}

func TestOpenErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    func(t *testing.T) string
		wantMsg string
	}{
		{
			name:    "empty path",
			path:    func(*testing.T) string { return "" },
			wantMsg: "database path is empty",
		},
		{
			name:    "missing directory",
			path:    func(t *testing.T) string { t.Helper(); return filepath.Join(t.TempDir(), "missing", "netward.db") },
			wantMsg: "open database",
		},
		{
			name: "not a database",
			path: func(t *testing.T) string {
				t.Helper()

				path := dbPath(t)
				if err := os.WriteFile(path, []byte(strings.Repeat("not a database ", 100)), 0o600); err != nil {
					t.Fatal(err)
				}

				return path
			},
			wantMsg: "open database",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := sqlite.Open(t.Context(), tt.path(t), nil)
			if store != nil {
				_ = store.Close()

				t.Error("Open() returned a store")
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Open() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestClose(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), dbPath(t), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err = store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err = store.DB().PingContext(t.Context()); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Errorf("Ping() after Close() error = %v, want a closed database error", err)
	}
}
