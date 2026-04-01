-- Add per-user Telegram notification settings.
-- IF NOT EXISTS guards make this migration safe to re-run.

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS tg_bot_token TEXT    NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS tg_chat_id   BIGINT  NOT NULL DEFAULT 0;
