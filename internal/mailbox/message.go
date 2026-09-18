package mailbox

import (
	"context"
	"time"

	"meilirize/internal/blob"
)

type SpecialUse string

const (
	SpecialUseInbox   SpecialUse = "inbox"
	SpecialUseSent    SpecialUse = "sent"
	SpecialUseDrafts  SpecialUse = "drafts"
	SpecialUseTrash   SpecialUse = "trash"
	SpecialUseJunk    SpecialUse = "junk"
	SpecialUseArchive SpecialUse = "archive"
)

type ParticipantKind string

const (
	ParticipantFrom    ParticipantKind = "from"
	ParticipantSender  ParticipantKind = "sender"
	ParticipantReplyTo ParticipantKind = "reply_to"
	ParticipantTo      ParticipantKind = "to"
	ParticipantCC      ParticipantKind = "cc"
	ParticipantBCC     ParticipantKind = "bcc"
)

type Mailbox struct {
	ID          int64
	AddressID   int64
	Name        string
	SpecialUse  SpecialUse
	UIDValidity uint32
	UIDNext     uint32
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type MessageFlags struct {
	Seen     bool
	Answered bool
	Flagged  bool
	Deleted  bool
	Draft    bool
}

type MessageParticipant struct {
	Kind        ParticipantKind
	Position    int
	Address     string
	DisplayName string
}

type Message struct {
	ID                 int64
	BlobKey            string
	BlobSHA256         string
	RawSize            int64
	HeaderMessageID    string
	Subject            string
	EnvelopeFrom       string
	EnvelopeRecipients []string
	Participants       []MessageParticipant
	SentAt             time.Time
	ReceivedAt         time.Time
	CreatedAt          time.Time
}

type MailboxMessage struct {
	MailboxID    int64
	MessageID    int64
	UID          uint32
	Flags        MessageFlags
	InternalDate time.Time
	CreatedAt    time.Time
}

type StoredMessage struct {
	Message        Message
	MailboxMessage MailboxMessage
}

type BlobVerificationResult struct {
	Checked int
	Failed  int
}

type CreateMessageParams struct {
	MailboxID          int64
	BlobKey            string
	BlobSHA256         string
	RawSize            int64
	HeaderMessageID    string
	Subject            string
	EnvelopeFrom       string
	EnvelopeRecipients []string
	Participants       []MessageParticipant
	SentAt             time.Time
	ReceivedAt         time.Time
	InternalDate       time.Time
	Flags              MessageFlags
}

type MessageRepository interface {
	MailboxByName(context.Context, int64, string) (Mailbox, error)
	Message(context.Context, int64) (Message, error)
	CreateMessage(context.Context, CreateMessageParams) (StoredMessage, error)
	ReferencedBlobs(context.Context) ([]blob.Ref, error)
}
