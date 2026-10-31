-- The two queries below are one step, db.Queries.NumberRouteTable: call that,
-- never these. The counter's row stays locked until the transaction ends, so
-- the counter lock must be the LAST lock the transaction takes: every row it
-- writes or locks happens before the numbering, nothing that locks follows it,
-- and a transaction that numbers several servers does so in ascending
-- server id. One global order (rows, then counters) leaves no deadlock cycle.

-- name: LockServerForRouteTable :one
-- The server row, shared: the counter's and every command's foreign key to it
-- would lock it anyway, but only after the counter; taking it first keeps the
-- counter last (a FOR UPDATE of the row, as the proxy deploy does, then waits
-- for us before we hold the counter, not after).
SELECT id FROM servers WHERE id = sqlc.arg(server_id) FOR KEY SHARE;

-- name: NextRouteTableSeq :one
-- The number of the next whole route table of a server.
INSERT INTO server_route_tables (server_id, seq)
VALUES (sqlc.arg(server_id), 1)
ON CONFLICT (server_id) DO UPDATE SET seq = server_route_tables.seq + 1
RETURNING seq;
