-- +goose Up
-- The instance mail relay can send through Resend's HTTPS API as well as SMTP.
-- The sender address is shared by both providers.
ALTER TABLE instance_settings RENAME COLUMN smtp_from TO mail_from;
ALTER TABLE instance_settings ADD COLUMN mail_provider text NOT NULL DEFAULT 'smtp' CHECK (mail_provider IN ('smtp','resend'));
ALTER TABLE instance_settings ADD COLUMN resend_api_key_enc bytea;

-- +goose Down
ALTER TABLE instance_settings DROP COLUMN resend_api_key_enc;
ALTER TABLE instance_settings DROP COLUMN mail_provider;
ALTER TABLE instance_settings RENAME COLUMN mail_from TO smtp_from;
