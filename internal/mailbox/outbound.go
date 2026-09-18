package mailbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type OutboundDeliveryStatus string

const (
	OutboundDeliveryQueued  OutboundDeliveryStatus = "queued"
	OutboundDeliverySending OutboundDeliveryStatus = "sending"
	OutboundDeliveryRetry   OutboundDeliveryStatus = "retry"
	OutboundDeliverySent    OutboundDeliveryStatus = "sent"
	OutboundDeliveryFailed  OutboundDeliveryStatus = "failed"
)

type OutboundDelivery struct {
	ID                int64
	IdempotencyKey    string
	MessageID         int64
	ProviderBindingID int64
	Status            OutboundDeliveryStatus
	AttemptCount      int
	ProviderMessageID string
	LastError         string
	NextAttemptAt     time.Time
	LeaseToken        string
	LeaseExpiresAt    time.Time
	SubmittedAt       time.Time
	SentAt            time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type QueuedSubmission struct {
	Message  Message
	Delivery OutboundDelivery
}

type SentSubmission struct {
	Message        Message
	Delivery       OutboundDelivery
	MailboxMessage MailboxMessage
}

type ClaimedSubmission struct {
	Message         Message
	Delivery        OutboundDelivery
	ProviderBinding ProviderBinding
}

type QueueSubmissionParams struct {
	ProviderBindingID  int64
	EnvelopeFrom       string
	EnvelopeRecipients []string
}

type CreateOutboundDeliveryParams struct {
	ProviderBindingID  int64
	IdempotencyKey     string
	BlobKey            string
	BlobSHA256         string
	RawSize            int64
	HeaderMessageID    string
	Subject            string
	EnvelopeFrom       string
	EnvelopeRecipients []string
	Participants       []MessageParticipant
	HeaderSentAt       time.Time
	SubmittedAt        time.Time
}

type CompleteOutboundDeliveryParams struct {
	DeliveryID        int64
	LeaseToken        string
	ProviderMessageID string
	SentAt            time.Time
}

type ClaimOutboundDeliveriesParams struct {
	Providers      []string
	Limit          int
	LeaseToken     string
	Now            time.Time
	LeaseExpiresAt time.Time
}

type RetryOutboundDeliveryParams struct {
	DeliveryID    int64
	LeaseToken    string
	LastError     string
	NextAttemptAt time.Time
	UpdatedAt     time.Time
}

type FailOutboundDeliveryParams struct {
	DeliveryID int64
	LeaseToken string
	LastError  string
	UpdatedAt  time.Time
}

type OutboundRepository interface {
	CreateOutboundDelivery(context.Context, CreateOutboundDeliveryParams) (QueuedSubmission, error)
	ClaimOutboundDeliveries(context.Context, ClaimOutboundDeliveriesParams) ([]ClaimedSubmission, error)
	RetryOutboundDelivery(context.Context, RetryOutboundDeliveryParams) error
	FailOutboundDelivery(context.Context, FailOutboundDeliveryParams) error
	CompleteOutboundDelivery(context.Context, CompleteOutboundDeliveryParams) (SentSubmission, error)
}

func newOutboundIdempotencyKey() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate outbound idempotency key: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}
