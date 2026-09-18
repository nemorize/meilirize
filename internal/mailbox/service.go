package mailbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"
	"sync"
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
	lifecycle  sync.RWMutex
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
	service.lifecycle.RLock()
	defer service.lifecycle.RUnlock()

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
	metadata, err := service.readMetadata(ctx, reference)
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
	service.lifecycle.RLock()
	defer service.lifecycle.RUnlock()

	message, err := service.repository.Message(ctx, messageID)
	if err != nil {
		return nil, err
	}
	reader, err := service.blobs.Open(ctx, blob.Ref{
		Key:    message.BlobKey,
		SHA256: message.BlobSHA256,
		Size:   message.RawSize,
	})
	if err != nil {
		return nil, fmt.Errorf("open raw message %d: %w", messageID, err)
	}
	return reader, nil
}

func (service *Service) CollectGarbage(
	ctx context.Context,
	gracePeriod time.Duration,
) (blob.GarbageCollectionResult, error) {
	if gracePeriod < 0 {
		return blob.GarbageCollectionResult{}, fmt.Errorf(
			"%w: blob garbage collection grace period must not be negative",
			ErrInvalid,
		)
	}
	service.lifecycle.Lock()
	defer service.lifecycle.Unlock()

	references, err := service.repository.ReferencedBlobs(ctx)
	if err != nil {
		return blob.GarbageCollectionResult{}, err
	}
	liveKeys := make(map[string]struct{}, len(references))
	for _, reference := range references {
		liveKeys[reference.Key] = struct{}{}
	}
	result, err := service.blobs.CollectGarbage(ctx, blob.GarbageCollection{
		LiveKeys:     liveKeys,
		DeleteBefore: service.now().UTC().Add(-gracePeriod),
	})
	if err != nil {
		return result, fmt.Errorf("collect message blobs: %w", err)
	}
	return result, nil
}

func (service *Service) VerifyBlobs(
	ctx context.Context,
) (BlobVerificationResult, error) {
	service.lifecycle.RLock()
	defer service.lifecycle.RUnlock()

	references, err := service.repository.ReferencedBlobs(ctx)
	if err != nil {
		return BlobVerificationResult{}, err
	}
	result := BlobVerificationResult{}
	var verificationErrors []error
	for _, reference := range references {
		if err := service.blobs.Verify(ctx, reference); err != nil {
			if ctx.Err() != nil {
				return result, fmt.Errorf("verify message blobs: %w", ctx.Err())
			}
			result.Failed++
			verificationErrors = append(
				verificationErrors,
				fmt.Errorf("verify blob %q: %w", reference.Key, err),
			)
		}
		result.Checked++
	}
	return result, errors.Join(verificationErrors...)
}

func (service *Service) RunGarbageCollection(
	ctx context.Context,
	interval time.Duration,
	gracePeriod time.Duration,
) error {
	if interval <= 0 {
		return fmt.Errorf("%w: blob garbage collection interval must be positive", ErrInvalid)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := service.CollectGarbage(ctx, gracePeriod); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

type parsedMetadata struct {
	messageID    string
	subject      string
	participants []MessageParticipant
	sentAt       time.Time
}

func (service *Service) readMetadata(
	ctx context.Context,
	reference blob.Ref,
) (metadata parsedMetadata, resultError error) {
	reader, err := service.blobs.Open(ctx, reference)
	if err != nil {
		return parsedMetadata{}, fmt.Errorf("open stored message for parsing: %w", err)
	}
	defer func() {
		if closeError := reader.Close(); closeError != nil {
			resultError = errors.Join(
				resultError,
				fmt.Errorf("verify stored message after parsing: %w", closeError),
			)
		}
	}()

	message, err := mail.ReadMessage(reader)
	if err != nil {
		return parsedMetadata{}, fmt.Errorf("parse raw message: %w", err)
	}
	metadata = parsedMetadata{
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
