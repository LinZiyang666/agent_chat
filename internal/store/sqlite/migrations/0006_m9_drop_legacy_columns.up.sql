-- M9 Phase 2: retire the legacy ack / mention_all bookkeeping columns
-- that 0002 / 0003 introduced.
--
--   messages.requires_ack       — superseded by @-mention semantics
--   messages.mention_all        — superseded by mention_everyone +
--                                 message_mentions
--   message_states.replied_at   — the ack timestamp; with PendingAcks
--                                 gone there is no read path left.
--
-- ALTER TABLE DROP COLUMN is supported in SQLite 3.35+ (modernc/sqlite
-- v1.50.1 wraps 3.53.1). No data backfill is needed: mention_all was
-- mirrored into mention_everyone by migration 0005 already, and the
-- application no longer reads requires_ack / replied_at.

ALTER TABLE messages       DROP COLUMN requires_ack;
ALTER TABLE messages       DROP COLUMN mention_all;
ALTER TABLE message_states DROP COLUMN replied_at;
