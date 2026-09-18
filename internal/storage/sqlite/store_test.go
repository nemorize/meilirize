package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"meilirize/internal/blob"
	"meilirize/internal/blob/local"
	"meilirize/internal/mailbox"
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
	assertMigrationCount(t, store.database, 8)

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
	assertMigrationCount(t, reopened.database, 8)
}

func TestRecreatedInboxReceivesNewUIDValidity(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	firstAddress, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "recreated@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstInbox, err := store.MailboxByName(ctx, firstAddress.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.database.ExecContext(ctx, "DELETE FROM addresses WHERE id = ?", firstAddress.ID); err != nil {
		t.Fatal(err)
	}
	secondAddress, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "recreated@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondInbox, err := store.MailboxByName(ctx, secondAddress.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	if secondAddress.ID != firstAddress.ID {
		t.Fatalf("address ID was not reused: first=%d second=%d", firstAddress.ID, secondAddress.ID)
	}
	if secondInbox.UIDValidity <= firstInbox.UIDValidity {
		t.Fatalf(
			"UIDVALIDITY did not increase: first=%d second=%d",
			firstInbox.UIDValidity,
			secondInbox.UIDValidity,
		)
	}
}

func TestUIDValiditySequenceMigratesExistingMailboxes(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	if _, err := database.ExecContext(ctx, createMigrationsTable); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:3] {
		if err := applyMigration(ctx, database, migration); err != nil {
			t.Fatal(err)
		}
	}
	store := &Store{database: database}

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	firstAddress, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "upgrade@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstInbox, err := store.MailboxByName(ctx, firstAddress.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	if err := migrate(ctx, database, migrationFiles); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM addresses WHERE id = ?", firstAddress.ID); err != nil {
		t.Fatal(err)
	}
	secondAddress, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "upgrade@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondInbox, err := store.MailboxByName(ctx, secondAddress.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if secondInbox.UIDValidity <= firstInbox.UIDValidity {
		t.Fatalf(
			"UIDVALIDITY after migration did not increase: first=%d second=%d",
			firstInbox.UIDValidity,
			secondInbox.UIDValidity,
		)
	}
}

func TestOrphanMessageMigrationPrunesExistingRows(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	if _, err := database.ExecContext(ctx, createMigrationsTable); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:4] {
		if err := applyMigration(ctx, database, migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(
		ctx,
		`INSERT INTO messages
            (blob_key, blob_sha256, raw_size, received_at)
         VALUES (?, ?, 1, ?)`,
		"sha256/"+strings.Repeat("0", 64),
		strings.Repeat("0", 64),
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, database, "messages", 1)

	if err := migrate(ctx, database, migrationFiles); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, database, "messages", 0)
}

func TestIngestStoresMessageMetadataAndOriginalBytes(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobs, err := local.New(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	service := mailbox.NewService(store, blobs)

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := store.MailboxByName(ctx, address.ID, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Name != "INBOX" || inbox.SpecialUse != mailbox.SpecialUseInbox || inbox.UIDNext != 1 {
		t.Fatalf("unexpected inbox: %#v", inbox)
	}

	raw := strings.Join([]string{
		"Message-ID: <message-1@example.com>",
		"Date: Fri, 19 Sep 2026 10:30:00 +0900",
		"From: Alice Sender <sender@Example.COM>",
		"To: Bob Recipient <bob@example.net>",
		"Subject: =?UTF-8?B?7JWI64WV7ZWY7IS47JqU?=",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"원본 본문입니다.",
		"",
	}, "\r\n")
	internalDate := time.Date(2026, 9, 19, 1, 45, 0, 0, time.UTC)
	stored, err := service.Ingest(ctx, mailbox.IngestParams{
		MailboxID:          inbox.ID,
		EnvelopeFrom:       "bounce@Example.COM",
		EnvelopeRecipients: []string{"Alice@Example.COM"},
		InternalDate:       internalDate,
		Flags:              mailbox.MessageFlags{Seen: true, Flagged: true},
	}, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	message := stored.Message
	if message.Subject != "안녕하세요" || message.HeaderMessageID != "<message-1@example.com>" {
		t.Fatalf("unexpected message headers: %#v", message)
	}
	if message.EnvelopeFrom != "bounce@example.com" {
		t.Fatalf("envelope from = %q", message.EnvelopeFrom)
	}
	if len(message.EnvelopeRecipients) != 1 || message.EnvelopeRecipients[0] != "Alice@example.com" {
		t.Fatalf("envelope recipients = %#v", message.EnvelopeRecipients)
	}
	if message.RawSize != int64(len(raw)) || message.BlobSHA256 == "" || message.BlobKey == "" {
		t.Fatalf("unexpected blob metadata: %#v", message)
	}
	if !hasParticipant(message.Participants, mailbox.ParticipantFrom, "sender@example.com", "Alice Sender") {
		t.Fatalf("missing sender participant: %#v", message.Participants)
	}
	if !hasParticipant(message.Participants, mailbox.ParticipantTo, "bob@example.net", "Bob Recipient") {
		t.Fatalf("missing recipient participant: %#v", message.Participants)
	}
	membership := stored.MailboxMessage
	if membership.UID != 1 || membership.InternalDate != internalDate || !membership.Flags.Seen || !membership.Flags.Flagged {
		t.Fatalf("unexpected mailbox message: %#v", membership)
	}

	reader, err := service.OpenRaw(ctx, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if closeError := reader.Close(); err == nil {
		err = closeError
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != raw {
		t.Fatalf("raw message changed:\n%s", got)
	}

	second, err := service.Ingest(ctx, mailbox.IngestParams{MailboxID: inbox.ID}, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if second.MailboxMessage.UID != 2 {
		t.Fatalf("second UID = %d", second.MailboxMessage.UID)
	}
	if second.Message.BlobKey != message.BlobKey {
		t.Fatalf("duplicate message blob keys differ: %q != %q", second.Message.BlobKey, message.BlobKey)
	}
	updatedInbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if updatedInbox.UIDNext != 3 {
		t.Fatalf("UIDNEXT = %d", updatedInbox.UIDNext)
	}
}

func TestOpenRawDetectsCorruptBlob(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobPath := filepath.Join(t.TempDir(), "blobs")
	blobStore, err := local.New(blobPath)
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
		Address:     "integrity@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: sender@example.com\r\n\r\noriginal body"
	stored, err := service.Ingest(
		ctx,
		mailbox.IngestParams{MailboxID: inbox.ID},
		strings.NewReader(raw),
	)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimPrefix(stored.Message.BlobKey, "sha256/")
	path := filepath.Join(blobPath, "sha256", hash[:2], hash[2:])
	if err := os.WriteFile(path, []byte(strings.Repeat("x", len(raw))), 0o600); err != nil {
		t.Fatal(err)
	}

	reader, err := service.OpenRaw(ctx, stored.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, readError := io.ReadAll(reader)
	closeError := reader.Close()
	if !errors.Is(errors.Join(readError, closeError), blob.ErrCorrupt) {
		t.Fatalf("read corrupt raw message error = %v", errors.Join(readError, closeError))
	}
}

func TestCreateMessageRollsBackMetadataAndUIDOnFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "rollback@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = store.CreateMessage(ctx, mailbox.CreateMessageParams{
		MailboxID:    inbox.ID,
		BlobKey:      "sha256/" + strings.Repeat("0", 64),
		BlobSHA256:   strings.Repeat("0", 64),
		ReceivedAt:   now,
		InternalDate: now,
		Participants: []mailbox.MessageParticipant{
			{Kind: "invalid", Address: "sender@example.com"},
		},
	})
	if !errors.Is(err, mailbox.ErrInvalid) {
		t.Fatalf("CreateMessage error = %v", err)
	}

	var messageCount int
	if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 {
		t.Fatalf("message count = %d", messageCount)
	}
	unchangedInbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if unchangedInbox.UIDNext != 1 {
		t.Fatalf("UIDNEXT after rollback = %d", unchangedInbox.UIDNext)
	}
}

func TestCreateMessageRollsBackWhenResultCannotBeAssembled(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "result-failure@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		CREATE TRIGGER corrupt_created_message
		AFTER INSERT ON messages
		BEGIN
			UPDATE messages SET created_at = 'invalid' WHERE id = NEW.id;
		END
	`); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	_, err = store.CreateMessage(ctx, mailbox.CreateMessageParams{
		MailboxID:    inbox.ID,
		BlobKey:      "sha256/" + strings.Repeat("0", 64),
		BlobSHA256:   strings.Repeat("0", 64),
		ReceivedAt:   now,
		InternalDate: now,
	})
	if err == nil || !strings.Contains(err.Error(), "parse SQLite timestamp") {
		t.Fatalf("CreateMessage error = %v", err)
	}

	assertTableCount(t, store.database, "messages", 0)
	assertTableCount(t, store.database, "mailbox_messages", 0)
	unchangedInbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if unchangedInbox.UIDNext != 1 {
		t.Fatalf("UIDNEXT after result assembly failure = %d", unchangedInbox.UIDNext)
	}
}

func TestStoreEnforcesBlobReferenceRelationship(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "blob-reference@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = store.CreateMessage(ctx, mailbox.CreateMessageParams{
		MailboxID:    inbox.ID,
		BlobKey:      "sha256/" + strings.Repeat("0", 64),
		BlobSHA256:   strings.Repeat("1", 64),
		ReceivedAt:   now,
		InternalDate: now,
	})
	if !errors.Is(err, mailbox.ErrInvalid) {
		t.Fatalf("mismatched application blob reference error = %v", err)
	}

	validHash := strings.Repeat("0", 64)
	result, err := store.database.ExecContext(
		ctx,
		`INSERT INTO messages (blob_key, blob_sha256, raw_size, received_at)
         VALUES (?, ?, 0, ?)`,
		"sha256/"+validHash,
		validHash,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(
		ctx,
		"UPDATE messages SET blob_key = ? WHERE id = ?",
		"sha256/"+strings.Repeat("1", 64),
		messageID,
	); err == nil {
		t.Fatal("database accepted mismatched blob key and SHA-256")
	}
	invalidHash := strings.Repeat("g", 64)
	if _, err := store.database.ExecContext(
		ctx,
		`INSERT INTO messages (blob_key, blob_sha256, raw_size, received_at)
         VALUES (?, ?, 0, ?)`,
		"sha256/"+invalidHash,
		invalidHash,
		now.Format(time.RFC3339Nano),
	); err == nil {
		t.Fatal("database accepted a non-hexadecimal blob SHA-256")
	}
}

func TestFailedIngestBlobsAreCollected(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobStore, err := local.New(filepath.Join(t.TempDir(), "blobs"))
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
		Address:     "failure@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Ingest(ctx, mailbox.IngestParams{
		MailboxID: inbox.ID,
	}, strings.NewReader("malformed header\r\n\r\nbody")); err == nil {
		t.Fatal("expected MIME parsing error")
	}
	waitForBlobAge()
	result, err := service.CollectGarbage(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("garbage collected after parse failure = %#v", result)
	}

	validRaw := "From: sender@example.com\r\nTo: failure@example.com\r\n\r\nbody"
	if _, err := service.Ingest(ctx, mailbox.IngestParams{
		MailboxID: 999,
	}, strings.NewReader(validRaw)); !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatalf("missing mailbox error = %v", err)
	}
	waitForBlobAge()
	result, err = service.CollectGarbage(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("garbage collected after database failure = %#v", result)
	}
}

func TestDeletingAddressPrunesMessageAndCollectsBlob(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobStore, err := local.New(filepath.Join(t.TempDir(), "blobs"))
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
		Address:     "delete@example.com",
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
		strings.NewReader("From: sender@example.com\r\nTo: delete@example.com\r\n\r\nbody"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.database.ExecContext(ctx, "DELETE FROM addresses WHERE id = ?", address.ID); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, store.database, "messages", 0)
	waitForBlobAge()
	result, err := service.CollectGarbage(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("garbage collection result = %#v", result)
	}
	if _, err := blobStore.Open(ctx, blob.Ref{
		Key:    stored.Message.BlobKey,
		SHA256: stored.Message.BlobSHA256,
		Size:   stored.Message.RawSize,
	}); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("deleted blob open error = %v", err)
	}
}

func TestSharedBlobSurvivesUntilLastMessageIsRemoved(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobStore, err := local.New(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	service := mailbox.NewService(store, blobStore)

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	addresses := make([]mailbox.Address, 0, 2)
	mailboxes := make([]mailbox.Mailbox, 0, 2)
	for _, email := range []string{"first@example.com", "second@example.com"} {
		address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
			OwnerUserID: user.ID,
			Address:     email,
		})
		if err != nil {
			t.Fatal(err)
		}
		inbox, err := store.MailboxByName(ctx, address.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		addresses = append(addresses, address)
		mailboxes = append(mailboxes, inbox)
	}
	raw := "From: sender@example.com\r\nTo: both@example.com\r\n\r\nshared"
	first, err := service.Ingest(ctx, mailbox.IngestParams{MailboxID: mailboxes[0].ID}, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Ingest(ctx, mailbox.IngestParams{MailboxID: mailboxes[1].ID}, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if first.Message.BlobKey != second.Message.BlobKey {
		t.Fatalf("shared blob keys differ: %q != %q", first.Message.BlobKey, second.Message.BlobKey)
	}

	if _, err := store.database.ExecContext(ctx, "DELETE FROM addresses WHERE id = ?", addresses[0].ID); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, store.database, "messages", 1)
	waitForBlobAge()
	result, err := service.CollectGarbage(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 0 {
		t.Fatalf("live shared blob was collected: %#v", result)
	}

	if _, err := store.database.ExecContext(ctx, "DELETE FROM addresses WHERE id = ?", addresses[1].ID); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, store.database, "messages", 0)
	result, err = service.CollectGarbage(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("unreferenced shared blob was not collected: %#v", result)
	}
}

func hasParticipant(
	participants []mailbox.MessageParticipant,
	kind mailbox.ParticipantKind,
	address string,
	displayName string,
) bool {
	for _, participant := range participants {
		if participant.Kind == kind && participant.Address == address && participant.DisplayName == displayName {
			return true
		}
	}
	return false
}

func TestStorePersistsMailboxOwnershipAndProviderBindings(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{DisplayName: " Alice "})
	if err != nil {
		t.Fatal(err)
	}
	if user.ID <= 0 || user.DisplayName != "Alice" || user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() {
		t.Fatalf("unexpected user: %#v", user)
	}

	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "Alice@Example.COM",
		DisplayName: " Personal ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if address.OwnerUserID != user.ID || address.Address != "Alice@example.com" || address.DisplayName != "Personal" {
		t.Fatalf("unexpected address: %#v", address)
	}

	lookedUp, err := store.AddressByEmail(ctx, "alice@EXAMPLE.com")
	if err != nil {
		t.Fatal(err)
	}
	if lookedUp.ID != address.ID {
		t.Fatalf("address ID = %d, want %d", lookedUp.ID, address.ID)
	}

	binding, err := store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:      address.ID,
		Provider:       " Mailgun ",
		SendEnabled:    true,
		ReceiveEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Provider != "mailgun" || !binding.SendEnabled || !binding.ReceiveEnabled {
		t.Fatalf("unexpected provider binding: %#v", binding)
	}

	bindings, err := store.ProviderBindings(ctx, address.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].ID != binding.ID {
		t.Fatalf("provider bindings = %#v", bindings)
	}
}

func TestIdentityCreatesRollBackWhenResultsCannotBeAssembled(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.database.ExecContext(ctx, `
		CREATE TRIGGER corrupt_created_user
		AFTER INSERT ON users
		BEGIN
			UPDATE users SET created_at = 'invalid' WHERE id = NEW.id;
		END
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(ctx, mailbox.CreateUserParams{}); err == nil ||
		!strings.Contains(err.Error(), "parse SQLite timestamp") {
		t.Fatalf("CreateUser error = %v", err)
	}
	assertTableCount(t, store.database, "users", 0)
	if _, err := store.database.ExecContext(ctx, "DROP TRIGGER corrupt_created_user"); err != nil {
		t.Fatal(err)
	}

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		CREATE TRIGGER corrupt_created_address
		AFTER INSERT ON addresses
		BEGIN
			UPDATE addresses SET created_at = 'invalid' WHERE id = NEW.id;
		END
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "broken@example.com",
	}); err == nil || !strings.Contains(err.Error(), "parse SQLite timestamp") {
		t.Fatalf("CreateAddress error = %v", err)
	}
	assertTableCount(t, store.database, "addresses", 0)
	assertTableCount(t, store.database, "mailboxes", 0)
	if _, err := store.database.ExecContext(ctx, "DROP TRIGGER corrupt_created_address"); err != nil {
		t.Fatal(err)
	}

	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "working@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		CREATE TRIGGER corrupt_created_provider_binding
		AFTER INSERT ON provider_bindings
		BEGIN
			UPDATE provider_bindings SET created_at = 'invalid' WHERE id = NEW.id;
		END
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:   address.ID,
		Provider:    "example",
		SendEnabled: true,
	}); err == nil || !strings.Contains(err.Error(), "parse SQLite timestamp") {
		t.Fatalf("BindProvider error = %v", err)
	}
	assertTableCount(t, store.database, "provider_bindings", 0)
}

func TestStoreEnforcesMailboxRelationships(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: 999,
		Address:     "missing-owner@example.com",
	})
	if !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("missing owner error = %v", err)
	}

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "OWNER@example.com",
	})
	if !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("duplicate address error = %v", err)
	}

	_, err = store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:   address.ID,
		Provider:    "mailgun",
		SendEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:      address.ID,
		Provider:       "MAILGUN",
		ReceiveEnabled: true,
	})
	if !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("duplicate provider error = %v", err)
	}

	if _, err := store.User(ctx, 999); !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatalf("missing user error = %v", err)
	}
	if _, err := store.AddressByEmail(ctx, "missing@example.com"); !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatalf("missing address error = %v", err)
	}
}

func TestDeletingUserCascadesMailboxRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "cascade@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:   address.ID,
		Provider:    "example",
		SendEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.database.ExecContext(ctx, "DELETE FROM users WHERE id = ?", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Address(ctx, address.ID); !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatalf("address after deleting user: %v", err)
	}
	bindings, err := store.ProviderBindings(ctx, address.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 {
		t.Fatalf("provider bindings after deleting user: %#v", bindings)
	}
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

func assertTableCount(t *testing.T, database *sql.DB, table string, want int) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", table, count, want)
	}
}

func waitForBlobAge() {
	time.Sleep(10 * time.Millisecond)
}
