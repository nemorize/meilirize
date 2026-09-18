CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE addresses (
    id INTEGER PRIMARY KEY,
    owner_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    address TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK (length(trim(address)) > 0),
    display_name TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX addresses_owner_user_id_index ON addresses(owner_user_id);

CREATE TABLE provider_bindings (
    id INTEGER PRIMARY KEY,
    address_id INTEGER NOT NULL REFERENCES addresses(id) ON DELETE CASCADE,
    provider TEXT NOT NULL COLLATE NOCASE CHECK (length(trim(provider)) > 0),
    send_enabled INTEGER NOT NULL CHECK (send_enabled IN (0, 1)),
    receive_enabled INTEGER NOT NULL CHECK (receive_enabled IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (address_id, provider),
    CHECK (send_enabled = 1 OR receive_enabled = 1)
);

CREATE INDEX provider_bindings_address_id_index ON provider_bindings(address_id);
