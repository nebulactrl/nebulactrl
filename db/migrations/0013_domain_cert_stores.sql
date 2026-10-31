-- +goose Up
-- Which ACME storage holds the certificate a domain's host is served with, as the
-- agent reads it from the proxy's storage files (domain names only): 'http-01' is
-- the default resolver's file, 'dns-01' the DNS-01 resolver's. No row: unknown or
-- no certificate yet. A name that switched to DNS-01 keeps the certificate the
-- HTTP-01 resolver issued (and renews it there) until it has none, so what a
-- domain's certificate comes from is read, not assumed.
CREATE TABLE domain_cert_stores (
    domain_id   uuid        PRIMARY KEY REFERENCES domains (id) ON DELETE CASCADE,
    issued_via  text        NOT NULL CHECK (issued_via IN ('http-01', 'dns-01')),
    observed_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE domain_cert_stores;
