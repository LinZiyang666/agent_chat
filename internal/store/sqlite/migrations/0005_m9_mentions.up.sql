-- M9 schema (Phase 1): bridge agentchat's mention model to Discord's
-- native @ system. Only ADDs and a data backfill; no DROPs yet — those
-- come in M9 Phase 2 once all Go code is migrated off the old fields
-- (`messages.requires_ack`, `messages.mention_all`,
-- `message_states.replied_at`).
--
-- Two pieces:
--
--   1. `message_mentions` — per-message multi-account mention set. One
--      row per (message_id, account_id) when the account is @-mentioned
--      in that message. Populated by:
--      - ingester (Discord MESSAGE_CREATE.Mentions[] mapped via
--        accounts.bot_user_id → account_id);
--      - send handler (Phase 2; for now writes nothing here).
--
--   2. `messages.mention_everyone` — boolean indicating the message is
--      an `@everyone` ping. Backfilled from the old `mention_all` column
--      so the new query path (state.CountMentionsForSubscribed) returns
--      identical results before any code stops writing `mention_all`.
--
-- Phase 2 will DROP `requires_ack`, `mention_all`, `replied_at`.

CREATE TABLE message_mentions (
  message_id TEXT NOT NULL,
  account_id TEXT NOT NULL,
  PRIMARY KEY (message_id, account_id),
  FOREIGN KEY (message_id) REFERENCES messages(id)  ON DELETE CASCADE,
  FOREIGN KEY (account_id) REFERENCES accounts(id)  ON DELETE CASCADE
);

-- Index for the per-account hot path in CountMentionsForSubscribed.
CREATE INDEX idx_message_mentions_account ON message_mentions(account_id);

ALTER TABLE messages ADD COLUMN mention_everyone INTEGER NOT NULL DEFAULT 0
  CHECK (mention_everyone IN (0,1));

-- Backfill: old --all messages become @everyone-equivalent. The query
-- layer reads `mention_everyone`, so this preserves M5/M6 history
-- visibility through the cutover.
UPDATE messages SET mention_everyone = mention_all WHERE mention_all = 1;
