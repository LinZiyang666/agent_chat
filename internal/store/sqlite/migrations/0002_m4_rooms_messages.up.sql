-- M4 schema: rooms (= 1:1 Discord channels), memberships (with the
-- separate subscribed flag from requirements §4), messages with
-- per-account read/reply state, and a placeholder reactions table for
-- M5+. All timestamps are Unix seconds (UTC), matching M2's convention.

-- Extend accounts so the inbound-message ingester can map an incoming
-- Discord author_id back to one of our agentchat accounts. Captured
-- when the account goes online (Provider.Identity returns it).
ALTER TABLE accounts ADD COLUMN bot_user_id TEXT;
CREATE INDEX idx_accounts_bot_user_id ON accounts(bot_user_id);

CREATE TABLE rooms (
  id                  TEXT PRIMARY KEY,
  discord_channel_id  TEXT NOT NULL UNIQUE,
  name                TEXT NOT NULL,
  archived            INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0,1)),
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);

CREATE INDEX idx_rooms_archived ON rooms(archived);

-- The two-bit "membership" state from requirements §4:
--   not-joined : no row
--   joined unsubscribed (旁观)  : row with subscribed = 0
--   joined subscribed (订阅)   : row with subscribed = 1
CREATE TABLE memberships (
  account_id  TEXT NOT NULL,
  room_id     TEXT NOT NULL,
  subscribed  INTEGER NOT NULL DEFAULT 0 CHECK (subscribed IN (0,1)),
  joined_at   INTEGER NOT NULL,
  PRIMARY KEY (account_id, room_id),
  FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
  FOREIGN KEY (room_id)    REFERENCES rooms(id)    ON DELETE CASCADE
);

CREATE INDEX idx_memberships_room_id ON memberships(room_id);
CREATE INDEX idx_memberships_subscribed ON memberships(account_id, subscribed);

-- One row per persisted message. discord_msg_id is the Discord-side
-- snowflake; the unique index lets both the send-path INSERT and the
-- gateway-ingest INSERT converge on the same row (ON CONFLICT IGNORE
-- on ingest, the send path inserts authoritatively).
--
-- author_account_id is NULLABLE because messages from Discord users
-- that are NOT one of our agentchat bots are still ingested (they
-- show up in history) but cannot be mapped to an account.
CREATE TABLE messages (
  id                  TEXT PRIMARY KEY,
  room_id             TEXT NOT NULL,
  author_account_id   TEXT,
  discord_msg_id      TEXT NOT NULL UNIQUE,
  content             TEXT NOT NULL,
  reply_to_msg_id     TEXT,
  requires_ack        INTEGER NOT NULL DEFAULT 0 CHECK (requires_ack IN (0,1)),
  priority            TEXT NOT NULL DEFAULT 'normal'
                      CHECK (priority IN ('normal','urgent','system')),
  created_at          INTEGER NOT NULL,
  content_hash        TEXT NOT NULL,
  FOREIGN KEY (room_id)         REFERENCES rooms(id)    ON DELETE CASCADE,
  FOREIGN KEY (author_account_id) REFERENCES accounts(id) ON DELETE SET NULL,
  FOREIGN KEY (reply_to_msg_id)   REFERENCES messages(id) ON DELETE SET NULL
);

-- (room_id, created_at DESC, id DESC) — the M2-P3-style tie-break on
-- the same-second case via the UUIDv7 id (which is itself time-ordered).
CREATE INDEX idx_messages_room_created ON messages(room_id, created_at DESC, id DESC);

CREATE TABLE message_states (
  message_id  TEXT NOT NULL,
  account_id  TEXT NOT NULL,
  read_at     INTEGER,
  replied_at  INTEGER,
  PRIMARY KEY (message_id, account_id),
  FOREIGN KEY (message_id) REFERENCES messages(id)  ON DELETE CASCADE,
  FOREIGN KEY (account_id) REFERENCES accounts(id)  ON DELETE CASCADE
);

CREATE INDEX idx_message_states_account_read ON message_states(account_id, read_at);

-- Placeholder for M5+. M4 creates the table so callers won't hit
-- "no such table" if they probe; no API surfaces it yet.
CREATE TABLE reactions (
  message_id  TEXT NOT NULL,
  account_id  TEXT NOT NULL,
  emoji       TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (message_id, account_id, emoji),
  FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
  FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
);
