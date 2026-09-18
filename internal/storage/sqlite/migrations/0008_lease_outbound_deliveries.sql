ALTER TABLE outbound_deliveries
ADD COLUMN lease_token TEXT NOT NULL DEFAULT '' CHECK (
    lease_token = '' OR (
        length(lease_token) = 64
        AND lease_token NOT GLOB '*[^0-9a-f]*'
    )
);

ALTER TABLE outbound_deliveries
ADD COLUMN lease_expires_at TEXT;

UPDATE outbound_deliveries
SET status = 'retry',
    last_error = CASE
        WHEN last_error = '' THEN 'recovered delivery without a lease'
        ELSE last_error
    END,
    next_attempt_at = COALESCE(
        next_attempt_at,
        strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
    ),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE status = 'sending';

CREATE INDEX outbound_deliveries_lease_expiration_index
    ON outbound_deliveries(status, lease_expires_at)
    WHERE status = 'sending';

CREATE TRIGGER outbound_deliveries_validate_lease_insert
BEFORE INSERT ON outbound_deliveries
WHEN (
    NEW.status = 'sending'
    AND (NEW.lease_token = '' OR NEW.lease_expires_at IS NULL)
) OR (
    NEW.status <> 'sending'
    AND (NEW.lease_token <> '' OR NEW.lease_expires_at IS NOT NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'invalid outbound delivery lease');
END;

CREATE TRIGGER outbound_deliveries_validate_lease_update
BEFORE UPDATE OF status, lease_token, lease_expires_at ON outbound_deliveries
WHEN (
    NEW.status = 'sending'
    AND (NEW.lease_token = '' OR NEW.lease_expires_at IS NULL)
) OR (
    NEW.status <> 'sending'
    AND (NEW.lease_token <> '' OR NEW.lease_expires_at IS NOT NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'invalid outbound delivery lease');
END;
