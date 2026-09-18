CREATE TRIGGER mailbox_messages_delete_orphan_message
AFTER DELETE ON mailbox_messages
WHEN NOT EXISTS (
    SELECT 1
    FROM mailbox_messages
    WHERE message_id = OLD.message_id
)
BEGIN
    DELETE FROM messages
    WHERE id = OLD.message_id;
END;

DELETE FROM messages
WHERE NOT EXISTS (
    SELECT 1
    FROM mailbox_messages
    WHERE mailbox_messages.message_id = messages.id
);
