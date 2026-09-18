package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL UNIQUE,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
)
`

type migration struct {
	version  int
	name     string
	checksum string
	contents string
}

type appliedMigration struct {
	name     string
	checksum string
}

func migrate(ctx context.Context, database *sql.DB, files fs.FS) error {
	migrations, err := loadMigrations(files)
	if err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("create SQLite migration table: %w", err)
	}

	applied, err := readAppliedMigrations(ctx, database)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, migrations); err != nil {
		return err
	}
	for _, migration := range migrations[len(applied):] {
		if err := applyMigration(ctx, database, migration); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read SQLite migrations: %w", err)
	}

	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		nameParts := strings.SplitN(strings.TrimSuffix(entry.Name(), ".sql"), "_", 2)
		if len(nameParts) != 2 || len(nameParts[0]) != 4 || nameParts[1] == "" {
			return nil, fmt.Errorf("invalid SQLite migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(nameParts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid SQLite migration version in %q", entry.Name())
		}
		contents, err := fs.ReadFile(files, path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read SQLite migration %q: %w", entry.Name(), err)
		}
		checksum := sha256.Sum256(contents)
		migrations = append(migrations, migration{
			version:  version,
			name:     entry.Name(),
			checksum: hex.EncodeToString(checksum[:]),
			contents: string(contents),
		})
	}

	sort.Slice(migrations, func(left, right int) bool {
		return migrations[left].version < migrations[right].version
	})
	for index, migration := range migrations {
		expectedVersion := index + 1
		if migration.version != expectedVersion {
			return nil, fmt.Errorf(
				"SQLite migration %q has version %d, expected %d",
				migration.name,
				migration.version,
				expectedVersion,
			)
		}
	}
	return migrations, nil
}

func readAppliedMigrations(ctx context.Context, database *sql.DB) (map[int]appliedMigration, error) {
	rows, err := database.QueryContext(
		ctx,
		"SELECT version, name, checksum FROM schema_migrations ORDER BY version",
	)
	if err != nil {
		return nil, fmt.Errorf("read applied SQLite migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var migration appliedMigration
		if err := rows.Scan(&version, &migration.name, &migration.checksum); err != nil {
			return nil, fmt.Errorf("scan applied SQLite migration: %w", err)
		}
		applied[version] = migration
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied SQLite migrations: %w", err)
	}
	return applied, nil
}

func validateAppliedMigrations(applied map[int]appliedMigration, migrations []migration) error {
	if len(applied) > len(migrations) {
		return fmt.Errorf("SQLite database migration version is newer than this build")
	}
	for version := 1; version <= len(applied); version++ {
		actual, ok := applied[version]
		if !ok {
			return fmt.Errorf("SQLite migration history is missing version %d", version)
		}
		expected := migrations[version-1]
		if actual.name != expected.name {
			return fmt.Errorf(
				"SQLite migration %d name changed from %q to %q",
				version,
				actual.name,
				expected.name,
			)
		}
		if actual.checksum != expected.checksum {
			return fmt.Errorf("SQLite migration %q checksum does not match", expected.name)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, database *sql.DB, migration migration) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite migration %q: %w", migration.name, err)
	}
	defer transaction.Rollback()

	if _, err := transaction.ExecContext(ctx, migration.contents); err != nil {
		return fmt.Errorf("apply SQLite migration %q: %w", migration.name, err)
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO schema_migrations (version, name, checksum) VALUES (?, ?, ?)",
		migration.version,
		migration.name,
		migration.checksum,
	); err != nil {
		return fmt.Errorf("record SQLite migration %q: %w", migration.name, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit SQLite migration %q: %w", migration.name, err)
	}
	return nil
}
