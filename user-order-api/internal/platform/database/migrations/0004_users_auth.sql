ALTER TABLE users
    ADD COLUMN password_hash VARCHAR(255) NULL,
    ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user',
    ADD COLUMN auth_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    ADD CONSTRAINT chk_users_role CHECK (role IN ('user', 'admin'));
