-- Quantix Admin Extension
-- Adds role-based access control to users table.

ALTER TABLE users ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'user';

-- Create index for fast admin lookups
CREATE INDEX IF NOT EXISTS idx_users_role ON users (role);
