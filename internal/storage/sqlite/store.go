package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

const maxOpenConnections = 4

type Store struct {
	database *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	dataSourceName, err := makeDataSourceName(path)
	if err != nil {
		return nil, err
	}

	database, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	database.SetMaxOpenConns(maxOpenConnections)
	database.SetMaxIdleConns(maxOpenConnections)

	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to SQLite database: %w", err)
	}
	if err := migrate(ctx, database, migrationFiles); err != nil {
		_ = database.Close()
		return nil, err
	}
	return &Store{database: database}, nil
}

func (store *Store) Close() error {
	if err := store.database.Close(); err != nil {
		return fmt.Errorf("close SQLite database: %w", err)
	}
	return nil
}

func makeDataSourceName(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("SQLite database path must not be empty")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite database path %q: %w", path, err)
	}
	urlPath := filepath.ToSlash(absolutePath)
	if runtime.GOOS == "windows" && !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	databaseURL := &url.URL{Scheme: "file", Path: urlPath}
	query := databaseURL.Query()
	query.Set("_busy_timeout", "5000")
	query.Set("_foreign_keys", "1")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "NORMAL")
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String(), nil
}
