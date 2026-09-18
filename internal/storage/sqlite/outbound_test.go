package sqlite

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
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
	claimed := claimOutboundDeliveries(t, ctx, store, "example", 1)
	if len(claimed) != 1 || claimed[0].Delivery.ID != queued.Delivery.ID {
		t.Fatalf("unexpected claimed submissions: %#v", claimed)
	}

	sentAt := time.Date(2026, 9, 19, 4, 0, 0, 0, time.UTC)
	completed, err := service.CompleteSubmission(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        queued.Delivery.ID,
		LeaseToken:        claimed[0].Delivery.LeaseToken,
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
		LeaseToken:        claimed[0].Delivery.LeaseToken,
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
	claimed := claimOutboundDeliveries(t, ctx, store, "example", 2)
	if len(claimed) != 2 {
		t.Fatalf("claimed submissions = %d", len(claimed))
	}
	leaseTokens := map[int64]string{}
	for _, submission := range claimed {
		leaseTokens[submission.Delivery.ID] = submission.Delivery.LeaseToken
	}
	sentAt := time.Now().UTC()
	if _, err := service.CompleteSubmission(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        first.Delivery.ID,
		LeaseToken:        leaseTokens[first.Delivery.ID],
		ProviderMessageID: "duplicate-provider-id",
		SentAt:            sentAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteSubmission(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        second.Delivery.ID,
		LeaseToken:        leaseTokens[second.Delivery.ID],
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
	if status != mailbox.OutboundDeliverySending {
		t.Fatalf("second delivery status after rollback = %q", status)
	}
}

func TestOutboundLeaseClaimRetryRecoveryAndFailure(t *testing.T) {
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
	now := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	claim := func(providerName string, token string, at time.Time) []mailbox.ClaimedSubmission {
		t.Helper()
		claimed, err := store.ClaimOutboundDeliveries(ctx, mailbox.ClaimOutboundDeliveriesParams{
			Providers:      []string{providerName},
			Limit:          1,
			LeaseToken:     token,
			Now:            at,
			LeaseExpiresAt: at.Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		return claimed
	}

	if claimed := claim("unsupported", strings.Repeat("f", 64), now); len(claimed) != 0 {
		t.Fatalf("unsupported provider claim = %#v", claimed)
	}
	if _, err := store.database.ExecContext(
		ctx,
		"UPDATE provider_bindings SET send_enabled = 0, receive_enabled = 1 WHERE id = ?",
		binding.ID,
	); err != nil {
		t.Fatal(err)
	}
	if claimed := claim("example", strings.Repeat("f", 64), now); len(claimed) != 0 {
		t.Fatalf("disabled provider claim = %#v", claimed)
	}
	if _, err := store.database.ExecContext(
		ctx,
		"UPDATE provider_bindings SET send_enabled = 1 WHERE id = ?",
		binding.ID,
	); err != nil {
		t.Fatal(err)
	}
	firstToken := strings.Repeat("a", 64)
	first := claim("example", firstToken, now)
	if len(first) != 1 ||
		first[0].Delivery.ID != queued.Delivery.ID ||
		first[0].Delivery.Status != mailbox.OutboundDeliverySending ||
		first[0].Delivery.AttemptCount != 1 ||
		first[0].Delivery.LeaseToken != firstToken {
		t.Fatalf("first claim = %#v", first)
	}
	if claimed := claim("example", strings.Repeat("b", 64), now.Add(30*time.Second)); len(claimed) != 0 {
		t.Fatalf("claim before lease expiration = %#v", claimed)
	}

	secondToken := strings.Repeat("c", 64)
	second := claim("example", secondToken, now.Add(2*time.Minute))
	if len(second) != 1 || second[0].Delivery.AttemptCount != 2 ||
		second[0].Delivery.LeaseToken != secondToken {
		t.Fatalf("recovered claim = %#v", second)
	}
	if err := store.RetryOutboundDelivery(ctx, mailbox.RetryOutboundDeliveryParams{
		DeliveryID:    queued.Delivery.ID,
		LeaseToken:    firstToken,
		LastError:     "stale worker",
		NextAttemptAt: now.Add(4 * time.Minute),
		UpdatedAt:     now.Add(3 * time.Minute),
	}); !errors.Is(err, mailbox.ErrLeaseLost) {
		t.Fatalf("stale retry error = %v", err)
	}
	nextAttempt := now.Add(4 * time.Minute)
	if err := store.RetryOutboundDelivery(ctx, mailbox.RetryOutboundDeliveryParams{
		DeliveryID:    queued.Delivery.ID,
		LeaseToken:    secondToken,
		LastError:     "temporary failure",
		NextAttemptAt: nextAttempt,
		UpdatedAt:     now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if claimed := claim("example", strings.Repeat("d", 64), nextAttempt.Add(-time.Second)); len(claimed) != 0 {
		t.Fatalf("claim before retry time = %#v", claimed)
	}

	thirdToken := strings.Repeat("e", 64)
	third := claim("example", thirdToken, nextAttempt)
	if len(third) != 1 || third[0].Delivery.AttemptCount != 3 {
		t.Fatalf("retry claim = %#v", third)
	}
	if err := store.FailOutboundDelivery(ctx, mailbox.FailOutboundDeliveryParams{
		DeliveryID: queued.Delivery.ID,
		LeaseToken: thirdToken,
		LastError:  "permanent failure",
		UpdatedAt:  nextAttempt.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	var status mailbox.OutboundDeliveryStatus
	var attempts int
	var lastError string
	var leaseToken string
	var leaseExpiresAt any
	if err := store.database.QueryRowContext(
		ctx,
		`SELECT status, attempt_count, last_error, lease_token, lease_expires_at
         FROM outbound_deliveries WHERE id = ?`,
		queued.Delivery.ID,
	).Scan(&status, &attempts, &lastError, &leaseToken, &leaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	if status != mailbox.OutboundDeliveryFailed || attempts != 3 ||
		lastError != "permanent failure" || leaseToken != "" || leaseExpiresAt != nil {
		t.Fatalf(
			"failed delivery = status %q, attempts %d, error %q, token %q, expiration %#v",
			status,
			attempts,
			lastError,
			leaseToken,
			leaseExpiresAt,
		)
	}
}

func TestOutboundClaimIsAtomicAcrossWorkers(t *testing.T) {
	ctx := context.Background()
	store, service, binding, _ := newOutboundTestService(t, ctx)
	if _, err := service.QueueSubmission(ctx, mailbox.QueueSubmissionParams{
		ProviderBindingID:  binding.ID,
		EnvelopeFrom:       "sender@example.com",
		EnvelopeRecipients: []string{"recipient@example.net"},
	}, strings.NewReader("From: sender@example.com\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 7, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	results := make(chan []mailbox.ClaimedSubmission, 2)
	errorsByWorker := make(chan error, 2)
	var group sync.WaitGroup
	for _, tokenCharacter := range []string{"a", "b"} {
		tokenCharacter := tokenCharacter
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			claimed, err := store.ClaimOutboundDeliveries(ctx, mailbox.ClaimOutboundDeliveriesParams{
				Providers:      []string{"example"},
				Limit:          1,
				LeaseToken:     strings.Repeat(tokenCharacter, 64),
				Now:            now,
				LeaseExpiresAt: now.Add(time.Minute),
			})
			if err != nil {
				errorsByWorker <- err
				return
			}
			results <- claimed
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Fatal(err)
	}
	claimedCount := 0
	for claimed := range results {
		claimedCount += len(claimed)
	}
	if claimedCount != 1 {
		t.Fatalf("claimed deliveries = %d, want 1", claimedCount)
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

func claimOutboundDeliveries(
	t *testing.T,
	ctx context.Context,
	store *Store,
	providerName string,
	limit int,
) []mailbox.ClaimedSubmission {
	t.Helper()
	now := time.Date(2026, 9, 19, 3, 0, 0, 0, time.UTC)
	claimed, err := store.ClaimOutboundDeliveries(ctx, mailbox.ClaimOutboundDeliveriesParams{
		Providers:      []string{providerName},
		Limit:          limit,
		LeaseToken:     strings.Repeat("a", 64),
		Now:            now,
		LeaseExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}
