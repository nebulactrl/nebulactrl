package db

import (
	"bytes"
	"context"
	"slices"

	"github.com/google/uuid"
)

// NumberRouteTable returns the number of the next whole route table of a
// server (agentprotocol.SyncRoutesPayload.TableSeq). Call it on a transaction's
// Queries, before reading the routes the table carries, and only then.
//
// The counter's row stays locked until the transaction ends: that is what
// makes the tables of one server come out in the order of the states they
// read. So the counter lock must be the last lock the transaction takes. Every
// row the transaction writes or locks is written or locked BEFORE this call,
// nothing that takes a row lock (a write, FOR UPDATE, a job insert, an audit
// entry) follows it, and a transaction that numbers several servers does so in
// ascending server id. With that single order (rows, then counters) no two
// transactions can wait for each other. The server row is taken shared first
// for the same reason: the command that carries the table references it.
func (q *Queries) NumberRouteTable(ctx context.Context, serverID uuid.UUID) (uint64, error) {
	if _, err := q.LockServerForRouteTable(ctx, serverID); err != nil {
		return 0, err
	}
	seq, err := q.NextRouteTableSeq(ctx, serverID)
	if err != nil {
		return 0, err
	}
	return uint64(seq), nil
}

// SortedServerIDs merges server id lists into one, without duplicates, in the
// ascending order a transaction numbers the route tables of several servers in
// (see NumberRouteTable).
func SortedServerIDs(lists ...[]uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	for _, l := range lists {
		for _, id := range l {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}
