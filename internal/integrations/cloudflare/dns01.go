package cloudflare

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// A name in a connected zone gets its certificate through DNS-01 once a sync has
// written its record with the connection's token (internal/dns01 decides from the
// name's status: managed, with no error). That is the evidence the token may edit
// the zone. The route table names the resolver the moment the evidence exists, so
// the sync that first leaves a name managed (or leaves it managed again after a
// failed sync) pushes the route table of the server that serves it, inside the
// transaction that stores the state, like the origin lock does when a record turns
// proxied. A server whose proxy runs no Cloudflare resolver yet ignores the name
// in the pushed rule until Deploy proxy applies the resolver.

// storeState saves the evaluated state of a name. When it just became eligible for
// DNS-01 the route table follows in the same transaction.
func (s *Service) storeState(ctx context.Context, ow owner, n *dnsName, res *runResult) error {
	eligible := res.status == StatusManaged && !res.tunnel && n.Served
	if !eligible || (n.Status == StatusManaged && n.Error == "") {
		return ow.setState(ctx, s.q, n.ID, res.status, res.message)
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ow.setState(ctx, db.New(tx), n.ID, res.status, res.message); err != nil {
			return err
		}
		return ow.push(ctx, tx, n)
	})
}
