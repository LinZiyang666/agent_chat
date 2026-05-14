-- M2 initial schema: accounts, tokens, audit log.
-- All timestamps are Unix seconds (INTEGER, UTC).

CREATE TABLE accounts (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  role            TEXT NOT NULL CHECK (role IN ('admin','user')),
  lifecycle_state TEXT NOT NULL CHECK (
                    lifecycle_state IN ('created','online','offline','archived','deleted')
                  ),
  bot_token_enc   BLOB,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);

CREATE TABLE tokens (
  id           TEXT PRIMARY KEY,
  account_id   TEXT NOT NULL,
  token_hash   BLOB NOT NULL,
  created_at   INTEGER NOT NULL,
  revoked_at   INTEGER,
  last_used_at INTEGER,
  FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
);

CREATE INDEX idx_tokens_account_id ON tokens(account_id);

CREATE TABLE audit_log (
  id         TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT,
  payload    TEXT,
  created_at INTEGER NOT NULL
);

CREATE INDEX idx_audit_account_id ON audit_log(account_id);
CREATE INDEX idx_audit_created_at ON audit_log(created_at);
