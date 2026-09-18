package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestOpenConfiguresDatabaseAndRunsMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "mailbox #1.db")

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	assertPragma(t, store.database, "foreign_keys", "1")
	assertPragma(t, store.database, "busy_timeout", "5000")
	assertPragma(t, store.database, "journal_mode", "wal")
	assertPragma(t, store.database, "synchronous", "1")
	assertMigrationCount(t, store.database, 1)

	var name string
	if err := store.database.QueryRowContext(
		ctx,
		"SELECT name FROM schema_migrations WHERE version = 1",
	).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "0001_initialize.sql" {
		t.Fatalf("migration name = %q", name)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("database mode = %s", info.Mode())
	}

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertMigrationCount(t, reopened.database, 1)
}

func TestMigrateAppliesInOrderAndDetectsChanges(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	migrations := fstest.MapFS{
		"migrations/0001_create_notes.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE notes (body TEXT NOT NULL);"),
		},
		"migrations/0002_add_note.sql": &fstest.MapFile{
			Data: []byte("INSERT INTO notes (body) VALUES ('hello');"),
		},
	}

	if err := migrate(ctx, database, migrations); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, database, migrations); err != nil {
		t.Fatal(err)
	}
	assertMigrationCount(t, database, 2)
	var noteCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM notes").Scan(&noteCount); err != nil {
		t.Fatal(err)
	}
	if noteCount != 1 {
		t.Fatalf("note count = %d", noteCount)
	}

	migrations["migrations/0001_create_notes.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE notes (body TEXT);"),
	}
	if err := migrate(ctx, database, migrations); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	database := openTestDatabase(t)
	migrations := fstest.MapFS{
		"migrations/0001_broken.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE temporary_data (value TEXT); INVALID SQL;"),
		},
	}

	err := migrate(context.Background(), database, migrations)
	if err == nil || !strings.Contains(err.Error(), "apply SQLite migration") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertMigrationCount(t, database, 0)
	var tableCount int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'temporary_data'",
	).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatalf("temporary table count = %d", tableCount)
	}
}

func TestLoadMigrationsRejectsVersionGaps(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{
		"migrations/0002_late.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	})
	if err == nil || !strings.Contains(err.Error(), "expected 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dataSourceName, err := makeDataSourceName(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Ping(); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func assertPragma(t *testing.T, database *sql.DB, name, want string) {
	t.Helper()
	var value string
	if err := database.QueryRow("PRAGMA " + name).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != want {
		t.Fatalf("PRAGMA %s = %q, want %q", name, value, want)
	}
}

func assertMigrationCount(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("migration count = %d, want %d", count, want)
	}
}
