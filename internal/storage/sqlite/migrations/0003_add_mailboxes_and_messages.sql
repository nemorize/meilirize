CREATE TABLE mailboxes (
    id INTEGER PRIMARY KEY,
    address_id INTEGER NOT NULL REFERENCES addresses(id) ON DELETE CASCADE,
    name TEXT NOT NULL COLLATE NOCASE CHECK (length(trim(name)) > 0),
    special_use TEXT NOT NULL DEFAULT '' CHECK (
        special_use IN ('', 'inbox', 'sent', 'drafts', 'trash', 'junk', 'archive')
    ),
    uid_validity INTEGER NOT NULL CHECK (uid_validity BETWEEN 1 AND 4294967295),
    uid_next INTEGER NOT NULL DEFAULT 1 CHECK (uid_next BETWEEN 1 AND 4294967295),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (address_id, name)
);

CREATE UNIQUE INDEX mailboxes_special_use_index
    ON mailboxes(address_id, special_use)
    WHERE special_use <> '';

INSERT INTO mailboxes (address_id, name, special_use, uid_validity)
SELECT id, 'INBOX', 'inbox', ((id - 1) % 4294967295) + 1
FROM addresses;

CREATE TRIGGER addresses_create_inbox
AFTER INSERT ON addresses
BEGIN
    INSERT INTO mailboxes (address_id, name, special_use, uid_validity)
    VALUES (NEW.id, 'INBOX', 'inbox', ((NEW.id - 1) % 4294967295) + 1);
END;

CREATE TABLE messages (
    id INTEGER PRIMARY KEY,
    blob_key TEXT NOT NULL CHECK (length(trim(blob_key)) > 0),
    blob_sha256 TEXT NOT NULL CHECK (length(blob_sha256) = 64),
    raw_size INTEGER NOT NULL CHECK (raw_size >= 0),
    header_message_id TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    envelope_from TEXT NOT NULL DEFAULT '',
    sent_at TEXT,
    received_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX messages_header_message_id_index ON messages(header_message_id)
    WHERE header_message_id <> '';
CREATE INDEX messages_received_at_index ON messages(received_at);

CREATE TABLE message_envelope_recipients (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    address TEXT NOT NULL COLLATE NOCASE CHECK (length(trim(address)) > 0),
    PRIMARY KEY (message_id, position)
);

CREATE INDEX message_envelope_recipients_address_index
    ON message_envelope_recipients(address);

CREATE TABLE message_participants (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('from', 'sender', 'reply_to', 'to', 'cc', 'bcc')),
    position INTEGER NOT NULL CHECK (position >= 0),
    address TEXT NOT NULL COLLATE NOCASE CHECK (length(trim(address)) > 0),
    display_name TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (message_id, kind, position)
);

CREATE INDEX message_participants_address_index ON message_participants(address);

CREATE TABLE mailbox_messages (
    mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    uid INTEGER NOT NULL CHECK (uid BETWEEN 1 AND 4294967295),
    seen INTEGER NOT NULL DEFAULT 0 CHECK (seen IN (0, 1)),
    answered INTEGER NOT NULL DEFAULT 0 CHECK (answered IN (0, 1)),
    flagged INTEGER NOT NULL DEFAULT 0 CHECK (flagged IN (0, 1)),
    deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
    draft INTEGER NOT NULL DEFAULT 0 CHECK (draft IN (0, 1)),
    internal_date TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (mailbox_id, message_id),
    UNIQUE (mailbox_id, uid)
);

CREATE INDEX mailbox_messages_message_id_index ON mailbox_messages(message_id);
