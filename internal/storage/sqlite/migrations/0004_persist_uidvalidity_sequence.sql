CREATE TABLE mailbox_uidvalidity_sequence (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_value INTEGER NOT NULL CHECK (last_value BETWEEN 0 AND 4294967295)
);

INSERT INTO mailbox_uidvalidity_sequence (singleton, last_value)
SELECT 1, COALESCE(MAX(uid_validity), 0)
FROM mailboxes;

DROP TRIGGER addresses_create_inbox;

CREATE TRIGGER addresses_create_inbox
AFTER INSERT ON addresses
BEGIN
    UPDATE mailbox_uidvalidity_sequence
    SET last_value = last_value + 1
    WHERE singleton = 1;

    INSERT INTO mailboxes (address_id, name, special_use, uid_validity)
    SELECT NEW.id, 'INBOX', 'inbox', last_value
    FROM mailbox_uidvalidity_sequence
    WHERE singleton = 1;
END;
