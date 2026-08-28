ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ;

ALTER TABLE users ADD CONSTRAINT users_only_bots_soft_deleted
  CHECK (auth_source = 'bot' OR deleted_at IS NULL);

CREATE INDEX idx_users_active_bots ON users(created_at DESC, id DESC)
  WHERE auth_source = 'bot' AND deleted_at IS NULL;
