-- +goose Up
-- Order of the whole route tables the control plane sends to an agent
-- (sync_routes, proxy_configure with routes, the reconcile frame).
--
-- A table is computed from the database and applied by the agent some time
-- later, and the agent runs commands concurrently, so an older table can be
-- applied after a newer one and undo what the newer one changed (a maintenance
-- window, a domain). Every table therefore carries the number it was built
-- under: the builder bumps the server's counter, in the transaction that reads
-- the routes, before it reads them. The bump locks the row until the
-- transaction ends, so tables of one server are numbered in the order of the
-- states they read. The agent applies no table older than the newest it has
-- applied (agentprotocol.SyncRoutesPayload.TableSeq).
CREATE TABLE server_route_tables (
    server_id uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    seq       bigint NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE server_route_tables;
