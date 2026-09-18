package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meilirize/internal/blob"
	bloblocal "meilirize/internal/blob/local"
	"meilirize/internal/buildinfo"
	"meilirize/internal/config"
	"meilirize/internal/mailbox"
	"meilirize/internal/storage/sqlite"
)

var testBuild = buildinfo.Info{
	Version: "1.2.3",
	Commit:  "abc1234",
	Date:    "2026-09-18T00:00:00Z",
}

func TestRootHelpListsCommandGroups(t *testing.T) {
	output, err := executeForTest("--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"MEILIRIZE",
		"meilirize <command> [flags]",
		"Runtime",
		"serve",
		"doctor",
		"Utilities",
		"version",
		"completion",
		"--config string",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("help does not contain %q:\n%s", expected, output)
		}
	}
	for _, unwanted := range []string{
		"init", "start", "stop", "restart", "status", "daemon", "migrate",
		"config", "user", "address", "provider",
	} {
		if strings.Contains(output, "  "+unwanted+" ") {
			t.Errorf("help unexpectedly contains %q:\n%s", unwanted, output)
		}
	}
}

func TestServeHelp(t *testing.T) {
	output, err := executeForTest("serve", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "meilirize serve") || !strings.Contains(output, "Usage") {
		t.Fatalf("unexpected output:\n%s", output)
	}
}

func TestDoctorValidatesConfigurationWithoutShowingValues(t *testing.T) {
	dataRoot := t.TempDir()
	databasePath := filepath.Join(dataRoot, "mailbox.db")
	blobPath := filepath.Join(dataRoot, "blobs")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(
		configPath,
		[]byte(fmt.Sprintf(`[smtp]
listen_plain = "127.0.0.1:2525"
hostname = "mail.example"

[database]
path = %q

[storage]
blob_path = %q
`, databasePath, blobPath)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	output, err := executeForTest("doctor", "--config", configPath)
	if err != nil {
		t.Fatal(err)
	}
	if output != "Configuration: OK\nDatabase: OK\nBlobs: OK (0 checked)\n" {
		t.Fatalf("unexpected output: %q", output)
	}
	for _, secret := range []string{configPath, databasePath, blobPath, "smtp", "2525", "mail.example"} {
		if strings.Contains(output, secret) {
			t.Errorf("doctor output exposes %q: %s", secret, output)
		}
	}
}

func TestDoctorRejectsCorruptReferencedBlob(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	databasePath := filepath.Join(dataRoot, "mailbox.db")
	blobPath := filepath.Join(dataRoot, "blobs")
	configPath := filepath.Join(dataRoot, "config.toml")
	if err := os.WriteFile(
		configPath,
		[]byte(fmt.Sprintf(`[database]
path = %q

[storage]
blob_path = %q
`, databasePath, blobPath)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	blobStore, err := bloblocal.New(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	service := mailbox.NewService(store, blobStore)
	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "doctor@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := service.Ingest(
		ctx,
		mailbox.IngestParams{MailboxID: inbox.ID},
		strings.NewReader("From: sender@example.com\r\n\r\nbody"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimPrefix(stored.Message.BlobKey, "sha256/")
	path := filepath.Join(blobPath, "sha256", hash[:2], hash[2:])
	if err := os.WriteFile(path, []byte(strings.Repeat("x", int(stored.Message.RawSize))), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := executeForTest("doctor", "--config", configPath)
	if !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("doctor error = %v", err)
	}
	if output != "" {
		t.Fatalf("doctor output on corruption = %q", output)
	}
}

func TestDoctorRejectsMalformedConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("broken = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := executeForTest("doctor", "--config", configPath)
	if err == nil || !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExplicitConfigFileDoesNotInitializeAutomaticDiscovery(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	resolverCalled := false
	factory := func() (config.Resolver, error) {
		resolverCalled = true
		return config.Resolver{}, nil
	}

	sources, err := resolveConfigSources(configPath, factory)
	if err != nil {
		t.Fatal(err)
	}
	if resolverCalled {
		t.Fatal("automatic discovery was initialized for an explicit selector")
	}
	if sources.FilePath != configPath || sources.FileSelection != config.FileSelectedByFlag {
		t.Fatalf("unexpected sources: %+v", sources)
	}
}

func TestVersionCommand(t *testing.T) {
	output, err := executeForTest("version")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"meilirize 1.2.3", "commit: abc1234", "built: 2026-09-18T00:00:00Z"} {
		if !strings.Contains(output, expected) {
			t.Errorf("version output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestErrorOutputIsPlainForNonTerminal(t *testing.T) {
	var output bytes.Buffer
	PrintError(&output, context.Canceled)
	if got := output.String(); got != "Error: context canceled\n" {
		t.Fatalf("error output = %q", got)
	}
}

func executeForTest(args ...string) (string, error) {
	var output bytes.Buffer
	err := Execute(context.Background(), args, strings.NewReader(""), &output, &output, testBuild)
	return output.String(), err
}
