CREATE TRIGGER messages_validate_blob_reference_insert
BEFORE INSERT ON messages
WHEN NEW.blob_key <> ('sha256/' || NEW.blob_sha256)
    OR NEW.blob_sha256 GLOB '*[^0-9a-f]*'
BEGIN
    SELECT RAISE(ABORT, 'invalid blob reference');
END;

CREATE TRIGGER messages_validate_blob_reference_update
BEFORE UPDATE OF blob_key, blob_sha256 ON messages
WHEN NEW.blob_key <> ('sha256/' || NEW.blob_sha256)
    OR NEW.blob_sha256 GLOB '*[^0-9a-f]*'
BEGIN
    SELECT RAISE(ABORT, 'invalid blob reference');
END;
