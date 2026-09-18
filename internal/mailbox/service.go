package mailbox

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"
	"time"

	"meilirize/internal/blob"
)

type IngestParams struct {
	MailboxID          int64
	EnvelopeFrom       string
	EnvelopeRecipients []string
	InternalDate       time.Time
	Flags              MessageFlags
}

type Service struct {
	repository MessageRepository
	blobs      blob.Store
	now        func() time.Time
}

func NewService(repository MessageRepository, blobs blob.Store) *Service {
	return &Service{
		repository: repository,
		blobs:      blobs,
		now:        time.Now,
	}
}

func (service *Service) Ingest(
	ctx context.Context,
	params IngestParams,
	raw io.Reader,
) (StoredMessage, error) {
	if params.MailboxID <= 0 {
		return StoredMessage{}, fmt.Errorf("%w: mailbox ID must be positive", ErrInvalid)
	}
	envelopeFrom, err := normalizeOptionalAddress(params.EnvelopeFrom)
	if err != nil {
		return StoredMessage{}, err
	}
	envelopeRecipients := make([]string, 0, len(params.EnvelopeRecipients))
	for _, recipient := range params.EnvelopeRecipients {
		normalized, err := NormalizeAddress(recipient)
		if err != nil {
			return StoredMessage{}, err
		}
		envelopeRecipients = append(envelopeRecipients, normalized)
	}

	reference, err := service.blobs.Put(ctx, raw)
	if err != nil {
		return StoredMessage{}, fmt.Errorf("store raw message: %w", err)
	}
	metadata, err := service.readMetadata(ctx, reference.Key)
	if err != nil {
		return StoredMessage{}, err
	}
	now := service.now().UTC()
	internalDate := params.InternalDate
	if internalDate.IsZero() {
		internalDate = now
	}
	return service.repository.CreateMessage(ctx, CreateMessageParams{
		MailboxID:          params.MailboxID,
		BlobKey:            reference.Key,
		BlobSHA256:         reference.SHA256,
		RawSize:            reference.Size,
		HeaderMessageID:    metadata.messageID,
		Subject:            metadata.subject,
		EnvelopeFrom:       envelopeFrom,
		EnvelopeRecipients: envelopeRecipients,
		Participants:       metadata.participants,
		SentAt:             metadata.sentAt,
		ReceivedAt:         now,
		InternalDate:       internalDate.UTC(),
		Flags:              params.Flags,
	})
}

func (service *Service) OpenRaw(ctx context.Context, messageID int64) (io.ReadCloser, error) {
	message, err := service.repository.Message(ctx, messageID)
	if err != nil {
		return nil, err
	}
	reader, err := service.blobs.Open(ctx, message.BlobKey)
	if err != nil {
		return nil, fmt.Errorf("open raw message %d: %w", messageID, err)
	}
	return reader, nil
}

type parsedMetadata struct {
	messageID    string
	subject      string
	participants []MessageParticipant
	sentAt       time.Time
}

func (service *Service) readMetadata(ctx context.Context, key string) (parsedMetadata, error) {
	reader, err := service.blobs.Open(ctx, key)
	if err != nil {
		return parsedMetadata{}, fmt.Errorf("open stored message for parsing: %w", err)
	}
	defer reader.Close()

	message, err := mail.ReadMessage(reader)
	if err != nil {
		return parsedMetadata{}, fmt.Errorf("parse raw message: %w", err)
	}
	metadata := parsedMetadata{
		messageID: strings.TrimSpace(message.Header.Get("Message-ID")),
		subject:   decodeHeader(message.Header.Get("Subject")),
	}
	if sentAt, err := mail.ParseDate(message.Header.Get("Date")); err == nil {
		metadata.sentAt = sentAt.UTC()
	}
	for _, header := range []struct {
		name string
		kind ParticipantKind
	}{
		{"From", ParticipantFrom},
		{"Sender", ParticipantSender},
		{"Reply-To", ParticipantReplyTo},
		{"To", ParticipantTo},
		{"Cc", ParticipantCC},
		{"Bcc", ParticipantBCC},
	} {
		addresses, err := message.Header.AddressList(header.name)
		if err != nil {
			continue
		}
		for position, address := range addresses {
			normalized, err := NormalizeAddress(address.Address)
			if err != nil {
				continue
			}
			metadata.participants = append(metadata.participants, MessageParticipant{
				Kind:        header.kind,
				Position:    position,
				Address:     normalized,
				DisplayName: address.Name,
			})
		}
	}
	return metadata, nil
}

func normalizeOptionalAddress(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return NormalizeAddress(value)
}

func decodeHeader(value string) string {
	decoded, err := new(mime.WordDecoder).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}
