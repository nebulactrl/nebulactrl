package cloudflare

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// DNS-01 certificates are chosen from the name's DNS sync (internal/dns01): a sync
// that wrote the record with the connection's token is the evidence the token may
// edit the zone. These tests pin what the sync leaves behind and that the route
// table follows when the evidence appears.

// A DNS-only name (no push from a proxied record) is pushed when its first sync
// leaves it managed, and again when a failed sync is followed by a good one.
func TestRoutesFollowTheSyncThatLeavesANameManaged(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	rec := &pushRecorder{}
	e.svc.SetRoutePusher(rec)
	e.connect(t)
	e.deploy(t)
	d := e.domain(t, "app.example.com", "")
	off := false
	_, err := e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	rec.reset()
	e.exec(t, `UPDATE domains SET cf_status = '', cf_error = '' WHERE id = $1`, d.ID)
	e.fake.mu.Lock()
	e.fake.recs["z1"] = nil
	e.fake.mu.Unlock()
	e.exec(t, `DELETE FROM cloudflare_records WHERE owner_id = $1`, d.ID)

	// The first sync writes the record: managed, so the route table is pushed to
	// the server that serves the name, naming the resolver from now on.
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Equal(t, StatusManaged, e.dom(t, d.ID).CfStatus)
	require.Equal(t, []string{e.srvA.ID.String()}, pushedIDs(rec))

	// A sync that changes nothing pushes nothing.
	rec.reset()
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Zero(t, rec.count())

	// A refused write records why (the evidence is withdrawn: HTTP-01 again), and
	// the sync after it that succeeds pushes the route table once more.
	e.exec(t, `UPDATE domains SET cf_proxied = true WHERE id = $1`, d.ID) // the record has to change
	e.fake.failWrites = http.StatusForbidden
	err = e.svc.SyncDomain(bg, d.ID)
	require.Error(t, err)
	require.Contains(t, e.dom(t, d.ID).CfError, "answered 403", "the sync error path stores the refusal that DNS-01 reads as a permission failure")
	e.fake.failWrites = 0
	rec.reset()
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Empty(t, e.dom(t, d.ID).CfError)
	require.Equal(t, StatusManaged, e.dom(t, d.ID).CfStatus)
	require.GreaterOrEqual(t, rec.count(), 1)
}

func pushedIDs(p *pushRecorder) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, id := range p.servers {
		out = append(out, id.String())
	}
	return out
}

// A name served through the server's tunnel never gets the DNS-01 push: its
// certificate is not requested at the origin.
func TestTunnelledNameIsNotPushedForDNS01(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	rec := &pushRecorder{}
	e.svc.SetRoutePusher(rec)
	e.connect(t)
	e.deploy(t)
	e.tunnels.set(e.srvA.ID, "t1.cfargotunnel.com")
	d := e.domain(t, "app.example.com", "")
	rec.reset()
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Equal(t, StatusManaged, e.dom(t, d.ID).CfStatus)
	// The tunnel CNAME is proxied, so the origin lock's own push may run; the
	// DNS-01 push must not add a second one.
	require.LessOrEqual(t, rec.count(), 1)
}
