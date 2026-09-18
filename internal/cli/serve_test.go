package cli

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServeCreatesAndMigratesSQLiteDatabase(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "meilirize.db")
	blobPath := filepath.Join(directory, "blobs")
	configPath := filepath.Join(directory, "config.toml")
	contents := fmt.Sprintf(
		"[smtp]\nlisten_plain = \"127.0.0.1:0\"\n\n[database]\npath = %q\n\n[storage]\nblob_path = %q\n",
		databasePath,
		blobPath,
	)
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	output := newNotifyingWriter()
	done := make(chan error, 1)
	go func() {
		done <- Execute(ctx, []string{"serve", "--config", configPath}, strings.NewReader(""), output, output, testBuild)
	}()

	select {
	case <-output.written:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("serve did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "SMTP listening on") {
		t.Fatalf("unexpected output: %q", output.String())
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var migrationCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 8 {
		t.Fatalf("migration count = %d", migrationCount)
	}
	if info, err := os.Stat(filepath.Join(blobPath, ".tmp")); err != nil || !info.IsDir() {
		t.Fatalf("blob directory was not initialized: %v", err)
	}
}

type notifyingWriter struct {
	mutex   sync.Mutex
	buffer  bytes.Buffer
	written chan struct{}
	once    sync.Once
}

func newNotifyingWriter() *notifyingWriter {
	return &notifyingWriter{written: make(chan struct{})}
}

func (writer *notifyingWriter) Write(contents []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	written, err := writer.buffer.Write(contents)
	writer.once.Do(func() { close(writer.written) })
	return written, err
}

func (writer *notifyingWriter) String() string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.buffer.String()
}
