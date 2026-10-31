-- +goose Up
-- The built-in single sign-on platform service (kind 'idp': Pocket ID or
-- Authelia) is gone: single sign-on is bring-your-own OpenID Connect only. No
-- instance of it exists in any deployment, so a row of that kind is simply
-- dropped; the sign-in provider it may have created stays an ordinary provider.
DELETE FROM platform_services WHERE kind = 'idp';
ALTER TABLE platform_services DROP CONSTRAINT platform_services_kind_check;
ALTER TABLE platform_services ADD CONSTRAINT platform_services_kind_check
    CHECK (kind IN ('s3','loki','grafana','headscale','registry'));

-- +goose Down
ALTER TABLE platform_services DROP CONSTRAINT platform_services_kind_check;
ALTER TABLE platform_services ADD CONSTRAINT platform_services_kind_check
    CHECK (kind IN ('s3','loki','grafana','headscale','idp','registry'));
