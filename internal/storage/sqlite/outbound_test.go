package sqlite

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meilirize/internal/blob/local"
	"meilirize/internal/mailbox"
)

func TestSentMailboxMigrationCoversExistingAndNewAddresses(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	if _, err := database.ExecContext(ctx, createMigrationsTable); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:6] {
		if err := applyMigration(ctx, database, migration); err != nil {
			t.Fatal(err)
		}
	}
	store := &Store{database: database}
	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	existingAddress, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "existing@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MailboxByName(ctx, existingAddress.ID, "Sent"); !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatalf("Sent mailbox before migration error = %v", err)
	}

	if err := migrate(ctx, database, migrationFiles); err != nil {
		t.Fatal(err)
	}
	existingInbox, err := store.MailboxByName(ctx, existingAddress.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	existingSent, err := store.MailboxByName(ctx, existingAddress.ID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	if existingSent.SpecialUse != mailbox.SpecialUseSent ||
		existingSent.UIDValidity == existingInbox.UIDValidity {
		t.Fatalf("unexpected migrated Sent mailbox: %#v", existingSent)
	}

	newAddress, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "new@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	newInbox, err := store.MailboxByName(ctx, newAddress.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	newSent, err := store.MailboxByName(ctx, newAddress.ID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	if newSent.SpecialUse != mailbox.SpecialUseSent || newSent.UIDValidity == newInbox.UIDValidity {
		t.Fatalf("unexpected new Sent mailbox: %#v", newSent)
	}
	seen := map[uint32]struct{}{
		existingInbox.UIDValidity: {},
		existingSent.UIDValidity:  {},
		newInbox.UIDValidity:      {},
		newSent.UIDValidity:       {},
	}
	if len(seen) != 4 {
		t.Fatalf("UIDVALIDITY values are not unique: %#v", seen)
	}
}

func TestQueueAndCompleteSubmissionStoresOneMessageInSent(t *testing.T) {
	ctx := context.Background()
	store, service, binding, sentMailbox := newOutboundTestService(t, ctx)
	raw := strings.Join([]string{
		"Message-ID: <outbound-1@example.com>",
		"Date: Sat, 19 Sep 2026 12:30:00 +0900",
		"From: Sender <sender@example.com>",
		"To: Recipient <recipient@example.net>",
		"Subject: queued message",
		"",
		"body",
	}, "\r\n")

	queued, err := service.QueueSubmission(ctx, mailbox.QueueSubmissionParams{
		ProviderBindingID:  binding.ID,
		EnvelopeFrom:       "sender@EXAMPLE.COM",
		EnvelopeRecipients: []string{"recipient@EXAMPLE.NET"},
	}, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if queued.Delivery.Status != mailbox.OutboundDeliveryQueued ||
		queued.Delivery.MessageID != queued.Message.ID ||
		queued.Delivery.ProviderBindingID != binding.ID ||
		len(queued.Delivery.IdempotencyKey) != 64 ||
		queued.Delivery.AttemptCount != 0 {
		t.Fatalf("unexpected queued delivery: %#v", queued.Delivery)
	}
	if queued.Message.Subject != "queued message" ||
		queued.Message.EnvelopeFrom != "sender@example.com" ||
		len(queued.Message.EnvelopeRecipients) != 1 ||
		queued.Message.EnvelopeRecipients[0] != "recipient@example.net" {
		t.Fatalf("unexpected queued message: %#v", queued.Message)
	}
	assertTableCount(t, store.database, "messages", 1)
	assertTableCount(t, store.database, "outbound_deliveries", 1)
	assertTableCount(t, store.database, "mailbox_messages", 0)

	reader, err := service.OpenRaw(ctx, queued.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRaw, err := io.ReadAll(reader)
	if closeError := reader.Close(); err == nil {
		err = closeError
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(storedRaw) != raw {
		t.Fatalf("stored submission changed:\n%s", storedRaw)
	}

	sentAt := time.Date(2026, 9, 19, 4, 0, 0, 0, time.UTC)
	completed, err := service.CompleteSubmission(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        queued.Delivery.ID,
		ProviderMessageID: "provider-message-1",
		SentAt:            sentAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Message.ID != queued.Message.ID ||
		completed.Delivery.Status != mailbox.OutboundDeliverySent ||
		completed.Delivery.AttemptCount != 1 ||
		completed.Delivery.ProviderMessageID != "provider-message-1" ||
		!completed.Delivery.SentAt.Equal(sentAt) {
		t.Fatalf("unexpected completed submission: %#v", completed)
	}
	if completed.MailboxMessage.MailboxID != sentMailbox.ID ||
		completed.MailboxMessage.MessageID != queued.Message.ID ||
		completed.MailboxMessage.UID != 1 ||
		!completed.MailboxMessage.Flags.Seen ||
		!completed.MailboxMessage.InternalDate.Equal(sentAt) {
		t.Fatalf("unexpected Sent membership: %#v", completed.MailboxMessage)
	}

	again, err := service.CompleteSubmission(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        queued.Delivery.ID,
		ProviderMessageID: "provider-message-1",
		SentAt:            sentAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.MailboxMessage.UID != completed.MailboxMessage.UID || again.Delivery.AttemptCount != 1 {
		t.Fatalf("completion was not idempotent: %#v", again)
	}
	updatedSent, err := store.MailboxByName(ctx, sentMailbox.AddressID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	if updatedSent.UIDNext != 2 {
		t.Fatalf("Sent UIDNEXT after repeated completion = %d", updatedSent.UIDNext)
	}
}

func TestCompleteSubmissionRollsBackSentUIDOnProviderIDConflict(t *testing.T) {
	ctx := context.Background()
	store, service, binding, sentMailbox := newOutboundTestService(t, ctx)
	queue := func(subject string) mailbox.QueuedSubmission {
		t.Helper()
		queued, err := service.QueueSubmission(ctx, mailbox.QueueSubmissionParams{
			ProviderBindingID:  binding.ID,
			EnvelopeFrom:       "sender@example.com",
			EnvelopeRecipients: []string{"recipient@example.net"},
		}, strings.NewReader("From: sender@example.com\r\nSubject: "+subject+"\r\n\r\nbody"))
		if err != nil {
			t.Fatal(err)
		}
		return queued
	}
	first := queue("first")
	second := queue("second")
	sentAt := time.Now().UTC()
	if _, err := service.CompleteSubmission(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        first.Delivery.ID,
		ProviderMessageID: "duplicate-provider-id",
		SentAt:            sentAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteSubmission(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        second.Delivery.ID,
		ProviderMessageID: "duplicate-provider-id",
		SentAt:            sentAt,
	}); !errors.Is(err, mailbox.ErrConflict) {
		t.Fatalf("duplicate provider message error = %v", err)
	}

	updatedSent, err := store.MailboxByName(ctx, sentMailbox.AddressID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	if updatedSent.UIDNext != 2 {
		t.Fatalf("Sent UIDNEXT after rolled-back completion = %d", updatedSent.UIDNext)
	}
	assertTableCount(t, store.database, "mailbox_messages", 1)
	var status mailbox.OutboundDeliveryStatus
	if err := store.database.QueryRowContext(
		ctx,
		"SELECT status FROM outbound_deliveries WHERE id = ?",
		second.Delivery.ID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != mailbox.OutboundDeliveryQueued {
		t.Fatalf("second delivery status after rollback = %q", status)
	}
}

func TestQueueSubmissionRequiresOwnedSendEnabledBinding(t *testing.T) {
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
		Address:     "receive-only@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:      address.ID,
		Provider:       "example",
		ReceiveEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.QueueSubmission(ctx, mailbox.QueueSubmissionParams{
		ProviderBindingID:  binding.ID,
		EnvelopeFrom:       address.Address,
		EnvelopeRecipients: []string{"recipient@example.net"},
	}, strings.NewReader("From: receive-only@example.com\r\n\r\nbody"))
	if !errors.Is(err, mailbox.ErrInvalid) {
		t.Fatalf("receive-only provider error = %v", err)
	}
	sendBinding, err := store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:   address.ID,
		Provider:    "send-example",
		SendEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.QueueSubmission(ctx, mailbox.QueueSubmissionParams{
		ProviderBindingID:  sendBinding.ID,
		EnvelopeFrom:       "another@example.com",
		EnvelopeRecipients: []string{"recipient@example.net"},
	}, strings.NewReader("From: another@example.com\r\n\r\nbody"))
	if !errors.Is(err, mailbox.ErrInvalid) {
		t.Fatalf("unowned envelope sender error = %v", err)
	}
	assertTableCount(t, store.database, "messages", 0)
	assertTableCount(t, store.database, "outbound_deliveries", 0)
}

func TestDeletingAddressPrunesQueuedSubmission(t *testing.T) {
	ctx := context.Background()
	store, service, binding, _ := newOutboundTestService(t, ctx)
	queued, err := service.QueueSubmission(ctx, mailbox.QueueSubmissionParams{
		ProviderBindingID:  binding.ID,
		EnvelopeFrom:       "sender@example.com",
		EnvelopeRecipients: []string{"recipient@example.net"},
	}, strings.NewReader("From: sender@example.com\r\n\r\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	var addressID int64
	if err := store.database.QueryRowContext(
		ctx,
		"SELECT address_id FROM provider_bindings WHERE id = ?",
		binding.ID,
	).Scan(&addressID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, "DELETE FROM addresses WHERE id = ?", addressID); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, store.database, "outbound_deliveries", 0)
	assertTableCount(t, store.database, "messages", 0)
	if _, err := store.Message(ctx, queued.Message.ID); !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatalf("queued message after deleting address error = %v", err)
	}
}

func newOutboundTestService(
	t *testing.T,
	ctx context.Context,
) (*Store, *mailbox.Service, mailbox.ProviderBinding, mailbox.Mailbox) {
	t.Helper()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobStore, err := local.New(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, mailbox.CreateUserParams{})
	if err != nil {
		t.Fatal(err)
	}
	address, err := store.CreateAddress(ctx, mailbox.CreateAddressParams{
		OwnerUserID: user.ID,
		Address:     "sender@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.BindProvider(ctx, mailbox.BindProviderParams{
		AddressID:   address.ID,
		Provider:    "example",
		SendEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sentMailbox, err := store.MailboxByName(ctx, address.ID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	return store, mailbox.NewService(store, blobStore), binding, sentMailbox
}
