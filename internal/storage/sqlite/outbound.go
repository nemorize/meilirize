package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"meilirize/internal/mailbox"
)

func (store *Store) CreateOutboundDelivery(
	ctx context.Context,
	params mailbox.CreateOutboundDeliveryParams,
) (mailbox.QueuedSubmission, error) {
	if err := validateOutboundDeliveryParams(params); err != nil {
		return mailbox.QueuedSubmission{}, err
	}

	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return mailbox.QueuedSubmission{}, fmt.Errorf("begin outbound delivery transaction: %w", err)
	}
	defer transaction.Rollback()

	if err := validateOutboundProviderBinding(
		ctx,
		transaction,
		params.ProviderBindingID,
		params.EnvelopeFrom,
	); err != nil {
		return mailbox.QueuedSubmission{}, err
	}
	messageParams := mailbox.CreateMessageParams{
		BlobKey:            params.BlobKey,
		BlobSHA256:         params.BlobSHA256,
		RawSize:            params.RawSize,
		HeaderMessageID:    params.HeaderMessageID,
		Subject:            params.Subject,
		EnvelopeFrom:       params.EnvelopeFrom,
		EnvelopeRecipients: params.EnvelopeRecipients,
		Participants:       params.Participants,
		SentAt:             params.HeaderSentAt,
		ReceivedAt:         params.SubmittedAt,
	}
	messageID, err := insertMessage(ctx, transaction, messageParams)
	if err != nil {
		return mailbox.QueuedSubmission{}, err
	}
	if err := insertEnvelopeRecipients(
		ctx,
		transaction,
		messageID,
		params.EnvelopeRecipients,
	); err != nil {
		return mailbox.QueuedSubmission{}, err
	}
	if err := insertParticipants(ctx, transaction, messageID, params.Participants); err != nil {
		return mailbox.QueuedSubmission{}, err
	}
	deliveryID, err := insertOutboundDelivery(ctx, transaction, messageID, params)
	if err != nil {
		return mailbox.QueuedSubmission{}, err
	}
	message, err := messageByID(ctx, transaction, messageID)
	if err != nil {
		return mailbox.QueuedSubmission{}, err
	}
	delivery, err := outboundDeliveryByID(ctx, transaction, deliveryID)
	if err != nil {
		return mailbox.QueuedSubmission{}, err
	}
	if err := transaction.Commit(); err != nil {
		return mailbox.QueuedSubmission{}, fmt.Errorf("commit outbound delivery transaction: %w", err)
	}
	return mailbox.QueuedSubmission{Message: message, Delivery: delivery}, nil
}

func (store *Store) ClaimOutboundDeliveries(
	ctx context.Context,
	params mailbox.ClaimOutboundDeliveriesParams,
) ([]mailbox.ClaimedSubmission, error) {
	providers, err := validateOutboundClaimParams(params)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return []mailbox.ClaimedSubmission{}, nil
	}

	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbound claim transaction: %w", err)
	}
	defer transaction.Rollback()

	providerPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(providers)), ",")
	query := fmt.Sprintf(
		`UPDATE outbound_deliveries
         SET status = 'sending',
             attempt_count = attempt_count + 1,
             next_attempt_at = NULL,
             lease_token = ?,
             lease_expires_at = ?,
             updated_at = ?
         WHERE id IN (
             SELECT outbound_deliveries.id
             FROM outbound_deliveries
             JOIN provider_bindings
               ON provider_bindings.id = outbound_deliveries.provider_binding_id
             JOIN addresses
               ON addresses.id = provider_bindings.address_id
             JOIN messages
               ON messages.id = outbound_deliveries.message_id
             WHERE provider_bindings.provider IN (%s)
               AND provider_bindings.send_enabled = 1
               AND addresses.address = messages.envelope_from
               AND (
                   outbound_deliveries.status = 'queued'
                   OR (
                       outbound_deliveries.status = 'retry'
                       AND (
                           outbound_deliveries.next_attempt_at IS NULL
                           OR julianday(outbound_deliveries.next_attempt_at) <= julianday(?)
                       )
                   )
                   OR (
                       outbound_deliveries.status = 'sending'
                       AND julianday(outbound_deliveries.lease_expires_at) <= julianday(?)
                   )
               )
             ORDER BY CASE outbound_deliveries.status
                 WHEN 'queued' THEN julianday(outbound_deliveries.submitted_at)
                 WHEN 'retry' THEN julianday(COALESCE(
                     outbound_deliveries.next_attempt_at,
                     outbound_deliveries.submitted_at
                 ))
                 ELSE julianday(outbound_deliveries.lease_expires_at)
             END,
             outbound_deliveries.id
             LIMIT ?
         )
         RETURNING id`,
		providerPlaceholders,
	)
	arguments := []any{
		params.LeaseToken,
		formatTimestamp(params.LeaseExpiresAt),
		formatTimestamp(params.Now),
	}
	for _, providerName := range providers {
		arguments = append(arguments, providerName)
	}
	arguments = append(
		arguments,
		formatTimestamp(params.Now),
		formatTimestamp(params.Now),
		params.Limit,
	)
	rows, err := transaction.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("claim outbound deliveries: %w", err)
	}
	var deliveryIDs []int64
	for rows.Next() {
		var deliveryID int64
		if err := rows.Scan(&deliveryID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan claimed outbound delivery ID: %w", err)
		}
		deliveryIDs = append(deliveryIDs, deliveryID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate claimed outbound delivery IDs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close claimed outbound delivery IDs: %w", err)
	}

	claimed := make([]mailbox.ClaimedSubmission, 0, len(deliveryIDs))
	for _, deliveryID := range deliveryIDs {
		delivery, err := outboundDeliveryByID(ctx, transaction, deliveryID)
		if err != nil {
			return nil, err
		}
		message, err := messageByID(ctx, transaction, delivery.MessageID)
		if err != nil {
			return nil, err
		}
		binding, err := providerBindingByID(ctx, transaction, delivery.ProviderBindingID)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, mailbox.ClaimedSubmission{
			Message:         message,
			Delivery:        delivery,
			ProviderBinding: binding,
		})
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbound claim transaction: %w", err)
	}
	return claimed, nil
}

func (store *Store) RetryOutboundDelivery(
	ctx context.Context,
	params mailbox.RetryOutboundDeliveryParams,
) error {
	if err := validateOutboundTransition(
		params.DeliveryID,
		params.LeaseToken,
		params.LastError,
		params.UpdatedAt,
	); err != nil {
		return err
	}
	if params.NextAttemptAt.IsZero() || !params.NextAttemptAt.After(params.UpdatedAt) {
		return fmt.Errorf("%w: next attempt time must be after update time", mailbox.ErrInvalid)
	}
	result, err := store.database.ExecContext(
		ctx,
		`UPDATE outbound_deliveries
         SET status = 'retry',
             last_error = ?,
             next_attempt_at = ?,
             lease_token = '',
             lease_expires_at = NULL,
             updated_at = ?
         WHERE id = ? AND status = 'sending' AND lease_token = ?`,
		params.LastError,
		formatTimestamp(params.NextAttemptAt),
		formatTimestamp(params.UpdatedAt),
		params.DeliveryID,
		params.LeaseToken,
	)
	if err != nil {
		return mapWriteError("retry outbound delivery", err)
	}
	return requireOutboundLease(result, "retry outbound delivery")
}

func (store *Store) FailOutboundDelivery(
	ctx context.Context,
	params mailbox.FailOutboundDeliveryParams,
) error {
	if err := validateOutboundTransition(
		params.DeliveryID,
		params.LeaseToken,
		params.LastError,
		params.UpdatedAt,
	); err != nil {
		return err
	}
	result, err := store.database.ExecContext(
		ctx,
		`UPDATE outbound_deliveries
         SET status = 'failed',
             last_error = ?,
             next_attempt_at = NULL,
             lease_token = '',
             lease_expires_at = NULL,
             updated_at = ?
         WHERE id = ? AND status = 'sending' AND lease_token = ?`,
		params.LastError,
		formatTimestamp(params.UpdatedAt),
		params.DeliveryID,
		params.LeaseToken,
	)
	if err != nil {
		return mapWriteError("fail outbound delivery", err)
	}
	return requireOutboundLease(result, "fail outbound delivery")
}

func (store *Store) CompleteOutboundDelivery(
	ctx context.Context,
	params mailbox.CompleteOutboundDeliveryParams,
) (mailbox.SentSubmission, error) {
	if params.DeliveryID <= 0 {
		return mailbox.SentSubmission{}, fmt.Errorf("%w: outbound delivery ID must be positive", mailbox.ErrInvalid)
	}
	if err := validateOutboundToken(params.LeaseToken, "lease token"); err != nil {
		return mailbox.SentSubmission{}, err
	}
	providerMessageID := strings.TrimSpace(params.ProviderMessageID)
	if providerMessageID == "" {
		return mailbox.SentSubmission{}, fmt.Errorf("%w: provider message ID must not be empty", mailbox.ErrInvalid)
	}
	if params.SentAt.IsZero() {
		return mailbox.SentSubmission{}, fmt.Errorf("%w: sent time must not be zero", mailbox.ErrInvalid)
	}

	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return mailbox.SentSubmission{}, fmt.Errorf("begin outbound completion transaction: %w", err)
	}
	defer transaction.Rollback()

	delivery, err := outboundDeliveryByID(ctx, transaction, params.DeliveryID)
	if err != nil {
		return mailbox.SentSubmission{}, err
	}
	sentMailbox, err := sentMailboxForDelivery(ctx, transaction, delivery.ID)
	if err != nil {
		return mailbox.SentSubmission{}, err
	}
	if delivery.Status == mailbox.OutboundDeliverySent {
		if delivery.ProviderMessageID != providerMessageID {
			return mailbox.SentSubmission{}, fmt.Errorf(
				"%w: outbound delivery already completed with another provider message ID",
				mailbox.ErrConflict,
			)
		}
		return committedSentSubmission(ctx, transaction, delivery, sentMailbox.ID)
	}
	if delivery.Status == mailbox.OutboundDeliveryFailed {
		return mailbox.SentSubmission{}, fmt.Errorf(
			"%w: failed outbound delivery cannot be completed",
			mailbox.ErrConflict,
		)
	}
	if delivery.Status != mailbox.OutboundDeliverySending || delivery.LeaseToken != params.LeaseToken {
		return mailbox.SentSubmission{}, fmt.Errorf(
			"complete outbound delivery: %w",
			mailbox.ErrLeaseLost,
		)
	}

	uid, err := allocateUID(ctx, transaction, sentMailbox.ID)
	if err != nil {
		return mailbox.SentSubmission{}, err
	}
	if err := insertMailboxMessage(
		ctx,
		transaction,
		sentMailbox.ID,
		delivery.MessageID,
		uid,
		mailbox.MessageFlags{Seen: true},
		params.SentAt.UTC(),
	); err != nil {
		return mailbox.SentSubmission{}, err
	}
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE outbound_deliveries
         SET status = 'sent',
             provider_message_id = ?,
             last_error = '',
             next_attempt_at = NULL,
             lease_token = '',
             lease_expires_at = NULL,
             sent_at = ?,
             updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
         WHERE id = ? AND status = 'sending' AND lease_token = ?`,
		providerMessageID,
		formatTimestamp(params.SentAt),
		delivery.ID,
		params.LeaseToken,
	)
	if err != nil {
		return mailbox.SentSubmission{}, mapWriteError("complete outbound delivery", err)
	}
	if err := requireOutboundLease(result, "complete outbound delivery"); err != nil {
		return mailbox.SentSubmission{}, err
	}
	delivery, err = outboundDeliveryByID(ctx, transaction, delivery.ID)
	if err != nil {
		return mailbox.SentSubmission{}, err
	}
	return committedSentSubmission(ctx, transaction, delivery, sentMailbox.ID)
}

func committedSentSubmission(
	ctx context.Context,
	transaction *sql.Tx,
	delivery mailbox.OutboundDelivery,
	sentMailboxID int64,
) (mailbox.SentSubmission, error) {
	message, err := messageByID(ctx, transaction, delivery.MessageID)
	if err != nil {
		return mailbox.SentSubmission{}, err
	}
	membership, err := mailboxMessageByID(ctx, transaction, sentMailboxID, delivery.MessageID)
	if err != nil {
		return mailbox.SentSubmission{}, err
	}
	if err := transaction.Commit(); err != nil {
		return mailbox.SentSubmission{}, fmt.Errorf("commit outbound completion transaction: %w", err)
	}
	return mailbox.SentSubmission{
		Message:        message,
		Delivery:       delivery,
		MailboxMessage: membership,
	}, nil
}

func validateOutboundDeliveryParams(params mailbox.CreateOutboundDeliveryParams) error {
	if params.ProviderBindingID <= 0 {
		return fmt.Errorf("%w: provider binding ID must be positive", mailbox.ErrInvalid)
	}
	if err := validateMessageRecord(
		params.BlobKey,
		params.BlobSHA256,
		params.RawSize,
		params.SubmittedAt,
	); err != nil {
		return err
	}
	if err := validateOutboundToken(params.IdempotencyKey, "outbound idempotency key"); err != nil {
		return err
	}
	if _, err := mailbox.NormalizeAddress(params.EnvelopeFrom); err != nil {
		return err
	}
	if len(params.EnvelopeRecipients) == 0 {
		return fmt.Errorf("%w: at least one envelope recipient is required", mailbox.ErrInvalid)
	}
	return nil
}

func validateOutboundClaimParams(
	params mailbox.ClaimOutboundDeliveriesParams,
) ([]string, error) {
	if params.Limit <= 0 || params.Limit > 100 {
		return nil, fmt.Errorf("%w: outbound claim limit must be between 1 and 100", mailbox.ErrInvalid)
	}
	if err := validateOutboundToken(params.LeaseToken, "lease token"); err != nil {
		return nil, err
	}
	if params.Now.IsZero() {
		return nil, fmt.Errorf("%w: outbound claim time must not be zero", mailbox.ErrInvalid)
	}
	if params.LeaseExpiresAt.IsZero() || !params.LeaseExpiresAt.After(params.Now) {
		return nil, fmt.Errorf("%w: lease expiration must be after claim time", mailbox.ErrInvalid)
	}
	providers := make([]string, 0, len(params.Providers))
	seen := make(map[string]struct{}, len(params.Providers))
	for _, value := range params.Providers {
		providerName, err := mailbox.NormalizeProvider(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[providerName]; exists {
			continue
		}
		seen[providerName] = struct{}{}
		providers = append(providers, providerName)
	}
	return providers, nil
}

func validateOutboundTransition(
	deliveryID int64,
	leaseToken string,
	lastError string,
	updatedAt time.Time,
) error {
	if deliveryID <= 0 {
		return fmt.Errorf("%w: outbound delivery ID must be positive", mailbox.ErrInvalid)
	}
	if err := validateOutboundToken(leaseToken, "lease token"); err != nil {
		return err
	}
	if strings.TrimSpace(lastError) == "" {
		return fmt.Errorf("%w: outbound delivery error must not be empty", mailbox.ErrInvalid)
	}
	if updatedAt.IsZero() {
		return fmt.Errorf("%w: outbound delivery update time must not be zero", mailbox.ErrInvalid)
	}
	return nil
}

func validateOutboundToken(value string, name string) error {
	if len(value) != 64 || strings.ToLower(value) != value {
		return fmt.Errorf("%w: %s must be lowercase hexadecimal", mailbox.ErrInvalid, name)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%w: %s must be lowercase hexadecimal", mailbox.ErrInvalid, name)
	}
	return nil
}

func requireOutboundLease(result sql.Result, operation string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: read affected rows: %w", operation, err)
	}
	if changed != 1 {
		return fmt.Errorf("%s: %w", operation, mailbox.ErrLeaseLost)
	}
	return nil
}

func validateOutboundProviderBinding(
	ctx context.Context,
	transaction *sql.Tx,
	providerBindingID int64,
	envelopeFrom string,
) error {
	var address string
	var sendEnabled bool
	err := transaction.QueryRowContext(
		ctx,
		`SELECT addresses.address, provider_bindings.send_enabled
         FROM provider_bindings
         JOIN addresses ON addresses.id = provider_bindings.address_id
         WHERE provider_bindings.id = ?`,
		providerBindingID,
	).Scan(&address, &sendEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("validate outbound provider binding: %w", mailbox.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("validate outbound provider binding: %w", err)
	}
	if !sendEnabled {
		return fmt.Errorf("%w: provider binding does not support sending", mailbox.ErrInvalid)
	}
	if !strings.EqualFold(address, envelopeFrom) {
		return fmt.Errorf("%w: envelope sender is not owned by provider binding", mailbox.ErrInvalid)
	}
	return nil
}

func insertOutboundDelivery(
	ctx context.Context,
	transaction *sql.Tx,
	messageID int64,
	params mailbox.CreateOutboundDeliveryParams,
) (int64, error) {
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO outbound_deliveries
			(idempotency_key, message_id, provider_binding_id, submitted_at)
		 VALUES (?, ?, ?, ?)`,
		params.IdempotencyKey,
		messageID,
		params.ProviderBindingID,
		formatTimestamp(params.SubmittedAt),
	)
	if err != nil {
		return 0, mapWriteError("create outbound delivery", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read created outbound delivery ID: %w", err)
	}
	return id, nil
}

func outboundDeliveryByID(
	ctx context.Context,
	queries rowQuerier,
	id int64,
) (mailbox.OutboundDelivery, error) {
	return scanOutboundDelivery(queries.QueryRowContext(
		ctx,
		`SELECT id, idempotency_key, message_id, provider_binding_id, status, attempt_count,
                provider_message_id, last_error, next_attempt_at, lease_token,
                lease_expires_at, submitted_at, sent_at, created_at, updated_at
         FROM outbound_deliveries WHERE id = ?`,
		id,
	))
}

func sentMailboxForDelivery(
	ctx context.Context,
	queries rowQuerier,
	deliveryID int64,
) (mailbox.Mailbox, error) {
	return scanMailbox(queries.QueryRowContext(
		ctx,
		`SELECT mailboxes.id, mailboxes.address_id, mailboxes.name,
                mailboxes.special_use, mailboxes.uid_validity, mailboxes.uid_next,
                mailboxes.created_at, mailboxes.updated_at
         FROM outbound_deliveries
         JOIN provider_bindings
           ON provider_bindings.id = outbound_deliveries.provider_binding_id
         JOIN mailboxes
           ON mailboxes.address_id = provider_bindings.address_id
          AND mailboxes.special_use = 'sent'
         WHERE outbound_deliveries.id = ?`,
		deliveryID,
	))
}

func scanOutboundDelivery(row rowScanner) (mailbox.OutboundDelivery, error) {
	var delivery mailbox.OutboundDelivery
	var nextAttemptAt sql.NullString
	var leaseExpiresAt sql.NullString
	var submittedAt string
	var sentAt sql.NullString
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&delivery.ID,
		&delivery.IdempotencyKey,
		&delivery.MessageID,
		&delivery.ProviderBindingID,
		&delivery.Status,
		&delivery.AttemptCount,
		&delivery.ProviderMessageID,
		&delivery.LastError,
		&nextAttemptAt,
		&delivery.LeaseToken,
		&leaseExpiresAt,
		&submittedAt,
		&sentAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return mailbox.OutboundDelivery{}, mapReadError("read outbound delivery", err)
	}
	var err error
	delivery.SubmittedAt, err = parseTimestamp(submittedAt)
	if err != nil {
		return mailbox.OutboundDelivery{}, err
	}
	if nextAttemptAt.Valid {
		delivery.NextAttemptAt, err = parseTimestamp(nextAttemptAt.String)
		if err != nil {
			return mailbox.OutboundDelivery{}, err
		}
	}
	if leaseExpiresAt.Valid {
		delivery.LeaseExpiresAt, err = parseTimestamp(leaseExpiresAt.String)
		if err != nil {
			return mailbox.OutboundDelivery{}, err
		}
	}
	if sentAt.Valid {
		delivery.SentAt, err = parseTimestamp(sentAt.String)
		if err != nil {
			return mailbox.OutboundDelivery{}, err
		}
	}
	delivery.CreatedAt, err = parseTimestamp(createdAt)
	if err != nil {
		return mailbox.OutboundDelivery{}, err
	}
	delivery.UpdatedAt, err = parseTimestamp(updatedAt)
	if err != nil {
		return mailbox.OutboundDelivery{}, err
	}
	return delivery, nil
}

var _ mailbox.OutboundRepository = (*Store)(nil)
