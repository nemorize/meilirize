UPDATE mailboxes
SET special_use = 'sent',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE special_use = ''
  AND name = 'Sent' COLLATE NOCASE
  AND NOT EXISTS (
      SELECT 1
      FROM mailboxes AS existing_sent
      WHERE existing_sent.address_id = mailboxes.address_id
        AND existing_sent.special_use = 'sent'
  );

CREATE TABLE migration_0007_sent_mailboxes (
    address_id INTEGER PRIMARY KEY,
    uid_validity INTEGER NOT NULL UNIQUE CHECK (uid_validity BETWEEN 1 AND 4294967295)
);

INSERT INTO migration_0007_sent_mailboxes (address_id, uid_validity)
SELECT
    addresses.id,
    mailbox_uidvalidity_sequence.last_value
        + ROW_NUMBER() OVER (ORDER BY addresses.id)
FROM addresses
CROSS JOIN mailbox_uidvalidity_sequence
WHERE mailbox_uidvalidity_sequence.singleton = 1
  AND NOT EXISTS (
      SELECT 1
      FROM mailboxes
      WHERE mailboxes.address_id = addresses.id
        AND mailboxes.special_use = 'sent'
  );

UPDATE mailbox_uidvalidity_sequence
SET last_value = COALESCE(
    (SELECT MAX(uid_validity) FROM migration_0007_sent_mailboxes),
    last_value
)
WHERE singleton = 1;

INSERT INTO mailboxes (address_id, name, special_use, uid_validity)
SELECT address_id, 'Sent', 'sent', uid_validity
FROM migration_0007_sent_mailboxes
ORDER BY address_id;

DROP TABLE migration_0007_sent_mailboxes;

DROP TRIGGER addresses_create_inbox;

CREATE TRIGGER addresses_create_default_mailboxes
AFTER INSERT ON addresses
BEGIN
    UPDATE mailbox_uidvalidity_sequence
    SET last_value = last_value + 2
    WHERE singleton = 1;

    INSERT INTO mailboxes (address_id, name, special_use, uid_validity)
    SELECT NEW.id, 'INBOX', 'inbox', last_value - 1
    FROM mailbox_uidvalidity_sequence
    WHERE singleton = 1;

    INSERT INTO mailboxes (address_id, name, special_use, uid_validity)
    SELECT NEW.id, 'Sent', 'sent', last_value
    FROM mailbox_uidvalidity_sequence
    WHERE singleton = 1;
END;

CREATE TABLE outbound_deliveries (
    id INTEGER PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE CHECK (
        length(idempotency_key) = 64
        AND idempotency_key NOT GLOB '*[^0-9a-f]*'
    ),
    message_id INTEGER NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
    provider_binding_id INTEGER NOT NULL REFERENCES provider_bindings(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (
        status IN ('queued', 'sending', 'retry', 'sent', 'failed')
    ),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    provider_message_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    next_attempt_at TEXT,
    submitted_at TEXT NOT NULL,
    sent_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX outbound_deliveries_status_next_attempt_index
    ON outbound_deliveries(status, next_attempt_at);

CREATE UNIQUE INDEX outbound_deliveries_provider_message_index
    ON outbound_deliveries(provider_binding_id, provider_message_id)
    WHERE provider_message_id <> '';

CREATE TRIGGER outbound_deliveries_validate_binding_insert
BEFORE INSERT ON outbound_deliveries
WHEN NOT EXISTS (
    SELECT 1
    FROM provider_bindings
    JOIN addresses ON addresses.id = provider_bindings.address_id
    JOIN messages ON messages.id = NEW.message_id
    WHERE provider_bindings.id = NEW.provider_binding_id
      AND provider_bindings.send_enabled = 1
      AND addresses.address = messages.envelope_from
)
BEGIN
    SELECT RAISE(ABORT, 'invalid outbound provider binding');
END;

CREATE TRIGGER outbound_deliveries_validate_binding_update
BEFORE UPDATE OF message_id, provider_binding_id ON outbound_deliveries
WHEN NOT EXISTS (
    SELECT 1
    FROM provider_bindings
    JOIN addresses ON addresses.id = provider_bindings.address_id
    JOIN messages ON messages.id = NEW.message_id
    WHERE provider_bindings.id = NEW.provider_binding_id
      AND provider_bindings.send_enabled = 1
      AND addresses.address = messages.envelope_from
)
BEGIN
    SELECT RAISE(ABORT, 'invalid outbound provider binding');
END;

DROP TRIGGER mailbox_messages_delete_orphan_message;

CREATE TRIGGER mailbox_messages_delete_orphan_message
AFTER DELETE ON mailbox_messages
WHEN NOT EXISTS (
    SELECT 1
    FROM mailbox_messages
    WHERE message_id = OLD.message_id
)
AND NOT EXISTS (
    SELECT 1
    FROM outbound_deliveries
    WHERE message_id = OLD.message_id
      AND status <> 'sent'
)
BEGIN
    DELETE FROM messages
    WHERE id = OLD.message_id;
END;

CREATE TRIGGER outbound_deliveries_delete_orphan_message
AFTER DELETE ON outbound_deliveries
WHEN NOT EXISTS (
    SELECT 1
    FROM mailbox_messages
    WHERE message_id = OLD.message_id
)
AND NOT EXISTS (
    SELECT 1
    FROM outbound_deliveries
    WHERE message_id = OLD.message_id
)
BEGIN
    DELETE FROM messages
    WHERE id = OLD.message_id;
END;
