package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"meilirize/internal/mailbox"
)

func (store *Store) MailboxByName(ctx context.Context, addressID int64, name string) (mailbox.Mailbox, error) {
	if addressID <= 0 {
		return mailbox.Mailbox{}, fmt.Errorf("%w: address ID must be positive", mailbox.ErrInvalid)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return mailbox.Mailbox{}, fmt.Errorf("%w: mailbox name must not be empty", mailbox.ErrInvalid)
	}
	return scanMailbox(store.database.QueryRowContext(
		ctx,
		`SELECT id, address_id, name, special_use, uid_validity, uid_next, created_at, updated_at
         FROM mailboxes WHERE address_id = ? AND name = ?`,
		addressID,
		name,
	))
}

func (store *Store) Message(ctx context.Context, id int64) (mailbox.Message, error) {
	if id <= 0 {
		return mailbox.Message{}, fmt.Errorf("%w: message ID must be positive", mailbox.ErrInvalid)
	}
	return messageByID(ctx, store.database, id)
}

type messageQuerier interface {
	rowQuerier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func messageByID(ctx context.Context, queries messageQuerier, id int64) (mailbox.Message, error) {
	message, err := scanMessage(queries.QueryRowContext(
		ctx,
		`SELECT id, blob_key, blob_sha256, raw_size, header_message_id, subject,
                envelope_from, sent_at, received_at, created_at
         FROM messages WHERE id = ?`,
		id,
	))
	if err != nil {
		return mailbox.Message{}, err
	}

	recipientRows, err := queries.QueryContext(
		ctx,
		`SELECT address FROM message_envelope_recipients
         WHERE message_id = ? ORDER BY position`,
		id,
	)
	if err != nil {
		return mailbox.Message{}, fmt.Errorf("read message envelope recipients: %w", err)
	}
	for recipientRows.Next() {
		var address string
		if err := recipientRows.Scan(&address); err != nil {
			_ = recipientRows.Close()
			return mailbox.Message{}, fmt.Errorf("scan message envelope recipient: %w", err)
		}
		message.EnvelopeRecipients = append(message.EnvelopeRecipients, address)
	}
	if err := recipientRows.Err(); err != nil {
		_ = recipientRows.Close()
		return mailbox.Message{}, fmt.Errorf("iterate message envelope recipients: %w", err)
	}
	if err := recipientRows.Close(); err != nil {
		return mailbox.Message{}, fmt.Errorf("close message envelope recipients: %w", err)
	}

	participantRows, err := queries.QueryContext(
		ctx,
		`SELECT kind, position, address, display_name FROM message_participants
         WHERE message_id = ? ORDER BY kind, position`,
		id,
	)
	if err != nil {
		return mailbox.Message{}, fmt.Errorf("read message participants: %w", err)
	}
	defer participantRows.Close()
	for participantRows.Next() {
		var participant mailbox.MessageParticipant
		if err := participantRows.Scan(
			&participant.Kind,
			&participant.Position,
			&participant.Address,
			&participant.DisplayName,
		); err != nil {
			return mailbox.Message{}, fmt.Errorf("scan message participant: %w", err)
		}
		message.Participants = append(message.Participants, participant)
	}
	if err := participantRows.Err(); err != nil {
		return mailbox.Message{}, fmt.Errorf("iterate message participants: %w", err)
	}
	return message, nil
}

func (store *Store) ReferencedBlobKeys(ctx context.Context) (map[string]struct{}, error) {
	rows, err := store.database.QueryContext(ctx, "SELECT DISTINCT blob_key FROM messages")
	if err != nil {
		return nil, fmt.Errorf("read referenced blob keys: %w", err)
	}
	defer rows.Close()

	keys := make(map[string]struct{})
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan referenced blob key: %w", err)
		}
		keys[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate referenced blob keys: %w", err)
	}
	return keys, nil
}

func (store *Store) CreateMessage(
	ctx context.Context,
	params mailbox.CreateMessageParams,
) (mailbox.StoredMessage, error) {
	if err := validateMessageParams(params); err != nil {
		return mailbox.StoredMessage{}, err
	}

	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return mailbox.StoredMessage{}, fmt.Errorf("begin message transaction: %w", err)
	}
	defer transaction.Rollback()

	uid, err := allocateUID(ctx, transaction, params.MailboxID)
	if err != nil {
		return mailbox.StoredMessage{}, err
	}
	messageID, err := insertMessage(ctx, transaction, params)
	if err != nil {
		return mailbox.StoredMessage{}, err
	}
	if err := insertEnvelopeRecipients(ctx, transaction, messageID, params.EnvelopeRecipients); err != nil {
		return mailbox.StoredMessage{}, err
	}
	if err := insertParticipants(ctx, transaction, messageID, params.Participants); err != nil {
		return mailbox.StoredMessage{}, err
	}
	if err := insertMailboxMessage(ctx, transaction, messageID, uid, params); err != nil {
		return mailbox.StoredMessage{}, err
	}
	message, err := messageByID(ctx, transaction, messageID)
	if err != nil {
		return mailbox.StoredMessage{}, err
	}
	membership, err := mailboxMessageByID(ctx, transaction, params.MailboxID, messageID)
	if err != nil {
		return mailbox.StoredMessage{}, err
	}
	if err := transaction.Commit(); err != nil {
		return mailbox.StoredMessage{}, fmt.Errorf("commit message transaction: %w", err)
	}
	return mailbox.StoredMessage{Message: message, MailboxMessage: membership}, nil
}

func validateMessageParams(params mailbox.CreateMessageParams) error {
	if params.MailboxID <= 0 {
		return fmt.Errorf("%w: mailbox ID must be positive", mailbox.ErrInvalid)
	}
	if strings.TrimSpace(params.BlobKey) == "" {
		return fmt.Errorf("%w: blob key must not be empty", mailbox.ErrInvalid)
	}
	if len(params.BlobSHA256) != 64 || strings.ToLower(params.BlobSHA256) != params.BlobSHA256 {
		return fmt.Errorf("%w: blob SHA-256 must be lowercase hexadecimal", mailbox.ErrInvalid)
	}
	if _, err := hex.DecodeString(params.BlobSHA256); err != nil {
		return fmt.Errorf("%w: blob SHA-256 must be lowercase hexadecimal", mailbox.ErrInvalid)
	}
	if params.RawSize < 0 {
		return fmt.Errorf("%w: raw message size must not be negative", mailbox.ErrInvalid)
	}
	if params.ReceivedAt.IsZero() {
		return fmt.Errorf("%w: received time must not be zero", mailbox.ErrInvalid)
	}
	if params.InternalDate.IsZero() {
		return fmt.Errorf("%w: internal date must not be zero", mailbox.ErrInvalid)
	}
	return nil
}

func allocateUID(ctx context.Context, transaction *sql.Tx, mailboxID int64) (uint32, error) {
	var uid int64
	err := transaction.QueryRowContext(
		ctx,
		`UPDATE mailboxes
         SET uid_next = uid_next + 1,
             updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
         WHERE id = ? AND uid_next < 4294967295
         RETURNING uid_next - 1`,
		mailboxID,
	).Scan(&uid)
	if err == sql.ErrNoRows {
		var exists int
		if checkError := transaction.QueryRowContext(
			ctx,
			"SELECT 1 FROM mailboxes WHERE id = ?",
			mailboxID,
		).Scan(&exists); checkError == sql.ErrNoRows {
			return 0, fmt.Errorf("allocate mailbox UID: %w", mailbox.ErrNotFound)
		} else if checkError != nil {
			return 0, fmt.Errorf("check mailbox UID exhaustion: %w", checkError)
		}
		return 0, fmt.Errorf("allocate mailbox UID: UID space exhausted")
	}
	if err != nil {
		return 0, fmt.Errorf("allocate mailbox UID: %w", err)
	}
	return uint32(uid), nil
}

func insertMessage(
	ctx context.Context,
	transaction *sql.Tx,
	params mailbox.CreateMessageParams,
) (int64, error) {
	var sentAt any
	if !params.SentAt.IsZero() {
		sentAt = formatTimestamp(params.SentAt)
	}
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO messages
            (blob_key, blob_sha256, raw_size, header_message_id, subject,
             envelope_from, sent_at, received_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		params.BlobKey,
		params.BlobSHA256,
		params.RawSize,
		params.HeaderMessageID,
		params.Subject,
		params.EnvelopeFrom,
		sentAt,
		formatTimestamp(params.ReceivedAt),
	)
	if err != nil {
		return 0, mapWriteError("create message", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read created message ID: %w", err)
	}
	return id, nil
}

func insertEnvelopeRecipients(
	ctx context.Context,
	transaction *sql.Tx,
	messageID int64,
	recipients []string,
) error {
	for position, recipient := range recipients {
		normalized, err := mailbox.NormalizeAddress(recipient)
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(
			ctx,
			`INSERT INTO message_envelope_recipients (message_id, position, address)
             VALUES (?, ?, ?)`,
			messageID,
			position,
			normalized,
		); err != nil {
			return mapWriteError("create message envelope recipient", err)
		}
	}
	return nil
}

func insertParticipants(
	ctx context.Context,
	transaction *sql.Tx,
	messageID int64,
	participants []mailbox.MessageParticipant,
) error {
	for _, participant := range participants {
		if participant.Position < 0 {
			return fmt.Errorf("%w: participant position must not be negative", mailbox.ErrInvalid)
		}
		if !validParticipantKind(participant.Kind) {
			return fmt.Errorf("%w: invalid participant kind %q", mailbox.ErrInvalid, participant.Kind)
		}
		address, err := mailbox.NormalizeAddress(participant.Address)
		if err != nil {
			return err
		}
		if _, err := transaction.ExecContext(
			ctx,
			`INSERT INTO message_participants
                (message_id, kind, position, address, display_name)
             VALUES (?, ?, ?, ?, ?)`,
			messageID,
			participant.Kind,
			participant.Position,
			address,
			participant.DisplayName,
		); err != nil {
			return mapWriteError("create message participant", err)
		}
	}
	return nil
}

func insertMailboxMessage(
	ctx context.Context,
	transaction *sql.Tx,
	messageID int64,
	uid uint32,
	params mailbox.CreateMessageParams,
) error {
	_, err := transaction.ExecContext(
		ctx,
		`INSERT INTO mailbox_messages
            (mailbox_id, message_id, uid, seen, answered, flagged, deleted, draft, internal_date)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		params.MailboxID,
		messageID,
		uid,
		params.Flags.Seen,
		params.Flags.Answered,
		params.Flags.Flagged,
		params.Flags.Deleted,
		params.Flags.Draft,
		formatTimestamp(params.InternalDate),
	)
	if err != nil {
		return mapWriteError("add message to mailbox", err)
	}
	return nil
}

func mailboxMessageByID(
	ctx context.Context,
	queries rowQuerier,
	mailboxID int64,
	messageID int64,
) (mailbox.MailboxMessage, error) {
	return scanMailboxMessage(queries.QueryRowContext(
		ctx,
		`SELECT mailbox_id, message_id, uid, seen, answered, flagged, deleted, draft,
                internal_date, created_at
         FROM mailbox_messages WHERE mailbox_id = ? AND message_id = ?`,
		mailboxID,
		messageID,
	))
}

func scanMailbox(row rowScanner) (mailbox.Mailbox, error) {
	var result mailbox.Mailbox
	var uidValidity int64
	var uidNext int64
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&result.ID,
		&result.AddressID,
		&result.Name,
		&result.SpecialUse,
		&uidValidity,
		&uidNext,
		&createdAt,
		&updatedAt,
	); err != nil {
		return mailbox.Mailbox{}, mapReadError("read mailbox", err)
	}
	result.UIDValidity = uint32(uidValidity)
	result.UIDNext = uint32(uidNext)
	var err error
	result.CreatedAt, err = parseTimestamp(createdAt)
	if err != nil {
		return mailbox.Mailbox{}, err
	}
	result.UpdatedAt, err = parseTimestamp(updatedAt)
	if err != nil {
		return mailbox.Mailbox{}, err
	}
	return result, nil
}

func scanMessage(row rowScanner) (mailbox.Message, error) {
	var result mailbox.Message
	var sentAt sql.NullString
	var receivedAt string
	var createdAt string
	if err := row.Scan(
		&result.ID,
		&result.BlobKey,
		&result.BlobSHA256,
		&result.RawSize,
		&result.HeaderMessageID,
		&result.Subject,
		&result.EnvelopeFrom,
		&sentAt,
		&receivedAt,
		&createdAt,
	); err != nil {
		return mailbox.Message{}, mapReadError("read message", err)
	}
	var err error
	if sentAt.Valid {
		result.SentAt, err = parseTimestamp(sentAt.String)
		if err != nil {
			return mailbox.Message{}, err
		}
	}
	result.ReceivedAt, err = parseTimestamp(receivedAt)
	if err != nil {
		return mailbox.Message{}, err
	}
	result.CreatedAt, err = parseTimestamp(createdAt)
	if err != nil {
		return mailbox.Message{}, err
	}
	return result, nil
}

func scanMailboxMessage(row rowScanner) (mailbox.MailboxMessage, error) {
	var result mailbox.MailboxMessage
	var uid int64
	var internalDate string
	var createdAt string
	if err := row.Scan(
		&result.MailboxID,
		&result.MessageID,
		&uid,
		&result.Flags.Seen,
		&result.Flags.Answered,
		&result.Flags.Flagged,
		&result.Flags.Deleted,
		&result.Flags.Draft,
		&internalDate,
		&createdAt,
	); err != nil {
		return mailbox.MailboxMessage{}, mapReadError("read mailbox message", err)
	}
	result.UID = uint32(uid)
	var err error
	result.InternalDate, err = parseTimestamp(internalDate)
	if err != nil {
		return mailbox.MailboxMessage{}, err
	}
	result.CreatedAt, err = parseTimestamp(createdAt)
	if err != nil {
		return mailbox.MailboxMessage{}, err
	}
	return result, nil
}

func validParticipantKind(kind mailbox.ParticipantKind) bool {
	switch kind {
	case mailbox.ParticipantFrom,
		mailbox.ParticipantSender,
		mailbox.ParticipantReplyTo,
		mailbox.ParticipantTo,
		mailbox.ParticipantCC,
		mailbox.ParticipantBCC:
		return true
	default:
		return false
	}
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
