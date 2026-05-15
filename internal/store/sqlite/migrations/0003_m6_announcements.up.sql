-- M6 schema: three announcement surfaces (per requirements §6 /
-- roadmap §7) plus the @all mention flag on messages.
--
-- Surfaces:
--   1. Room announcements      — group-scoped, replaces prior version;
--                                forces unread on every current member
--                                of the room.
--   2. System announcements    — admin-only, global; one row per
--                                announcement, unread for every account
--                                at create time (excluding system role,
--                                which doesn't exist today).
--   3. @all message flag       — `mention_all` on messages: any member
--                                of the room sees the message in their
--                                mentions feed regardless of bot-id.
--
-- The two _reads tables are append-only join rows; one row per
-- (announcement, account) pair, inserted when the account ACKs.
-- "Unread" is therefore derived: announcement exists ∧ no read row.
-- This matches the message_states model from M4 (no row = not read).

CREATE TABLE announcements (
  id          TEXT PRIMARY KEY,
  room_id     TEXT NOT NULL,
  content     TEXT NOT NULL,
  version     INTEGER NOT NULL,
  -- created_by is NULLABLE (M6-P3-002 fix): combined with
  -- `ON DELETE SET NULL` below, this lets `agentchat admin account
  -- delete` remove an account that has previously posted
  -- announcements without violating a NOT NULL constraint. Matches
  -- the same pattern used by `messages.author_account_id` in v2.
  created_by  TEXT,
  created_at  INTEGER NOT NULL,
  FOREIGN KEY (room_id)    REFERENCES rooms(id)    ON DELETE CASCADE,
  FOREIGN KEY (created_by) REFERENCES accounts(id) ON DELETE SET NULL
);

-- Newest-first lookup per room (the GET endpoint returns only the
-- highest-version row). Tie-break by id (UUIDv7, itself time-ordered)
-- mirrors the messages table pattern.
CREATE INDEX idx_announcements_room_version
  ON announcements(room_id, version DESC, id DESC);

CREATE TABLE announcement_reads (
  announcement_id  TEXT NOT NULL,
  account_id       TEXT NOT NULL,
  read_at          INTEGER NOT NULL,
  PRIMARY KEY (announcement_id, account_id),
  FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE,
  FOREIGN KEY (account_id)      REFERENCES accounts(id)      ON DELETE CASCADE
);

CREATE INDEX idx_announcement_reads_account
  ON announcement_reads(account_id);

CREATE TABLE system_announcements (
  id          TEXT PRIMARY KEY,
  content     TEXT NOT NULL,
  -- created_by is NULLABLE (M6-P3-002 fix): see announcements above.
  created_by  TEXT,
  created_at  INTEGER NOT NULL,
  FOREIGN KEY (created_by) REFERENCES accounts(id) ON DELETE SET NULL
);

CREATE INDEX idx_system_announcements_created
  ON system_announcements(created_at DESC, id DESC);

CREATE TABLE system_announcement_reads (
  sys_ann_id  TEXT NOT NULL,
  account_id  TEXT NOT NULL,
  read_at     INTEGER NOT NULL,
  PRIMARY KEY (sys_ann_id, account_id),
  FOREIGN KEY (sys_ann_id) REFERENCES system_announcements(id) ON DELETE CASCADE,
  FOREIGN KEY (account_id) REFERENCES accounts(id)             ON DELETE CASCADE
);

CREATE INDEX idx_system_announcement_reads_account
  ON system_announcement_reads(account_id);

-- @all flag on messages: when true, every room member counts the
-- message in their mentions feed (state aggregator dimension 3).
-- Default 0 preserves M4/M5 semantics for prior rows.
ALTER TABLE messages ADD COLUMN mention_all INTEGER NOT NULL DEFAULT 0
  CHECK (mention_all IN (0,1));
