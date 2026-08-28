-- Bot accounts are passwordless identities shared by every user.

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_auth_source_valid;

ALTER TABLE users ADD CONSTRAINT users_auth_source_valid
  CHECK (auth_source IN ('password', 'proxy', 'bot'));
