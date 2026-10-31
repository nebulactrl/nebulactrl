-- +goose Up
-- The MCP endpoint can be switched on from the web UI by the instance admin.
-- NEBULACTRL_MCP_ENABLED, when set, still decides and locks the switch.
ALTER TABLE instance_settings ADD COLUMN mcp_enabled boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE instance_settings DROP COLUMN mcp_enabled;
