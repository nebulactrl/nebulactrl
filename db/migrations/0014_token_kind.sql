-- +goose Up
-- A token is made for one use: 'mcp' tokens work only on /mcp and are refused
-- on /api/v1; 'rest' tokens (REST, CLI, CI) work only on /api/v1 and are
-- refused on /mcp. Tokens that exist stay 'rest'; an agent needs a new MCP token.
--
-- The production guard now applies to every token unless
-- allow_production_without_approval is set (instance admin or team admin, at
-- creation). The per-token opt-in require_production_approval is gone: the
-- guard is the default.
ALTER TABLE api_tokens
    ADD COLUMN kind text NOT NULL DEFAULT 'rest' CHECK (kind IN ('mcp', 'rest')),
    DROP COLUMN require_production_approval;

-- +goose Down
ALTER TABLE api_tokens
    ADD COLUMN require_production_approval boolean NOT NULL DEFAULT false,
    DROP COLUMN kind;
