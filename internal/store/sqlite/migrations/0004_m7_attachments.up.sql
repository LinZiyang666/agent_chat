-- M7 schema: attachment index (per roadmap §8).
--
-- One row per file attached to a persisted message. The daemon's
-- downloader subscribes to inbound message events, fetches each
-- attachment in the background, and lands `local_path` + `sha256`
-- when the download completes. Outbound (send-path) attachments
-- skip the downloader and land local_path = the source file the
-- caller passed.
--
-- A message can carry multiple files; (message_id, filename) is
-- unique within a message because Discord disambiguates same-name
-- files by appending a suffix during upload, but our index keys on
-- our own row id and treats (message_id, filename) as a soft
-- identity (uniqueness enforced by application layer if needed).

CREATE TABLE attachments (
  id              TEXT PRIMARY KEY,
  message_id      TEXT NOT NULL,
  filename        TEXT NOT NULL,
  size            INTEGER NOT NULL,
  mime            TEXT NOT NULL DEFAULT '',
  -- local_path stays empty until the downloader finishes the
  -- inbound fetch (or, for outbound, is set immediately to the
  -- source file path the caller uploaded from). downloaded_at is
  -- NULL until the file is committed locally.
  local_path      TEXT NOT NULL DEFAULT '',
  discord_url     TEXT NOT NULL DEFAULT '',
  downloaded_at   INTEGER,
  -- sha256 hex string of the persisted bytes; empty until downloaded.
  sha256          TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE INDEX idx_attachments_message ON attachments(message_id);
CREATE INDEX idx_attachments_pending ON attachments(downloaded_at)
  WHERE downloaded_at IS NULL;
