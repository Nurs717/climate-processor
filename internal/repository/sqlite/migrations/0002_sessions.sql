-- Sign-ins of the owner. The secret token is never stored: id is its SHA-256 in hex.
CREATE TABLE sessions (
    id           TEXT    NOT NULL PRIMARY KEY CHECK (length(id) = 64),
    user_id      INTEGER NOT NULL,             -- Telegram user id of the owner
    created_at   INTEGER NOT NULL,             -- Unix seconds, UTC
    expires_at   INTEGER NOT NULL,             -- Unix seconds, UTC
    last_used_at INTEGER NOT NULL,             -- Unix seconds, UTC
    user_agent   TEXT    NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE INDEX sessions_user_id ON sessions (user_id);
CREATE INDEX sessions_expires_at ON sessions (expires_at);
