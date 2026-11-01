package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Limits applied to agent-reported buckets. The agent is trusted to be
// enrolled but not to be well-formed: a bug or compromised host must not be
// able to bloat the database or write into another workspace.
const (
	maxBucketAge    = agentprotocol.TrafficRetention + time.Hour
	maxBucketAhead  = 5 * time.Minute
	maxHostLen      = 253
	maxHLLBytes     = 8 << 10
	maxKeyLen       = 200
	maxTopEntries   = agentprotocol.TopN + 1 // + "Other"
	maxStatusCodes  = 64
	maxCounterValue = int64(1) << 50
)

var _ agents.TrafficSink = (*Service)(nil)

// IngestTraffic stores the minute buckets of a traffic frame in traffic_1m.
// Attribution uses the agent's nc_service stamp (validated to belong to the
// server's workspace) and falls back to the host → domain mapping of services
// on this server; hosts matching neither are stored unrouted. Duplicates (an
// agent resending after a reconnect) are ignored. A nil error means every
// bucket is durable.
func (s *Service) IngestTraffic(ctx context.Context, serverID uuid.UUID, t agentprotocol.Traffic) error {
	if len(t.Buckets) == 0 {
		return nil
	}
	if len(t.Buckets) > agentprotocol.MaxTrafficBuckets {
		return apierr.BadRequest("too_many_buckets", fmt.Sprintf("a traffic frame carries at most %d buckets", agentprotocol.MaxTrafficBuckets))
	}
	srv, err := s.q.GetServerByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" || !srv.TrafficAnalyticsEnabled {
		return nil // acknowledged and dropped
	}

	now := s.now().UTC()
	var claimed []uuid.UUID
	hostSet := map[string]struct{}{}
	for _, b := range t.Buckets {
		if id, err := uuid.Parse(b.ServiceID); err == nil {
			claimed = append(claimed, id)
		}
		if b.ServiceID == "" {
			hostSet[strings.ToLower(b.Host)] = struct{}{}
		}
	}
	valid := map[uuid.UUID]bool{}
	if len(claimed) > 0 {
		ids, err := s.q.ListValidTrafficServices(ctx, db.ListValidTrafficServicesParams{ServiceIds: claimed, WorkspaceID: srv.WorkspaceID})
		if err != nil {
			return fmt.Errorf("validate services: %w", err)
		}
		for _, id := range ids {
			valid[id] = true
		}
	}
	byHost := map[string]uuid.UUID{}
	if len(hostSet) > 0 {
		hosts := make([]string, 0, len(hostSet))
		for h := range hostSet {
			hosts = append(hosts, h)
		}
		rows, err := s.q.ListDomainServicesByHost(ctx, db.ListDomainServicesByHostParams{Hostnames: hosts, ServerID: &serverID})
		if err != nil {
			return fmt.Errorf("resolve hosts: %w", err)
		}
		for _, r := range rows {
			byHost[r.Hostname] = r.ServiceID
		}
	}

	// Origins are kept only while the server counts them; city lists only at the city level.
	geoLevel := ""
	if srv.GeoEnabled {
		geoLevel = srv.GeoLevel
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		for _, b := range t.Buckets {
			p, ok := sanitize(b, now, geoLevel)
			if !ok {
				s.log.WarnContext(ctx, "dropping malformed traffic bucket", "server_id", serverID, "host", b.Host)
				continue
			}
			var svc *uuid.UUID
			if id, err := uuid.Parse(b.ServiceID); err == nil && valid[id] {
				svc = &id
			} else if id, ok := byHost[p.Host]; ok {
				svc = &id
			}
			p.ServerID, p.ServiceID = serverID, svc
			if err := q.InsertTraffic1m(ctx, p); err != nil {
				return fmt.Errorf("insert traffic_1m: %w", err)
			}
		}
		return nil
	})
}

func clampCounter(v int64) int64 { return min(max(v, 0), maxCounterValue) }

// sanitize validates one reported bucket and converts it into insert params.
func sanitize(b agentprotocol.TrafficBucket, now time.Time, geoLevel string) (db.InsertTraffic1mParams, bool) {
	host := agentprotocol.MaskIPHost(strings.ToLower(strings.TrimSpace(b.Host)))
	if host == "" || len(host) > maxHostLen || len(b.LatHist) != agentprotocol.LatencyBuckets {
		return db.InsertTraffic1mParams{}, false
	}
	bucket := b.Bucket.UTC().Truncate(time.Minute)
	if bucket.After(now.Add(maxBucketAhead)) || bucket.Before(now.Add(-maxBucketAge)) {
		return db.InsertTraffic1mParams{}, false
	}
	hist := make([]int64, len(b.LatHist))
	for i, v := range b.LatHist {
		hist[i] = clampCounter(v)
	}
	var hll []byte
	if len(b.HLL) > 0 && len(b.HLL) <= maxHLLBytes && decodeHLL(b.HLL) != nil {
		hll = b.HLL
	}
	if hll == nil {
		hll = []byte{}
	}
	codes := map[string]int64{}
	for k, v := range b.StatusCodes {
		if len(codes) >= maxStatusCodes {
			break
		}
		if len(k) == 3 && k[0] >= '1' && k[0] <= '9' {
			codes[k] = clampCounter(v)
		}
	}
	statusJSON, _ := json.Marshal(codes)
	topJSON, _ := json.Marshal(cleanTop(b.Top))
	return db.InsertTraffic1mParams{
		Host: host, Bucket: bucket,
		Requests: clampCounter(b.Requests), Documents: clampCounter(b.Documents), BotRequests: clampCounter(b.BotRequests),
		BytesIn: clampCounter(b.BytesIn), BytesOut: clampCounter(b.BytesOut),
		S1xx: clampCounter(b.S1xx), S2xx: clampCounter(b.S2xx), S3xx: clampCounter(b.S3xx),
		S4xx: clampCounter(b.S4xx), S5xx: clampCounter(b.S5xx), SOther: clampCounter(b.SOther),
		LatHist: hist, LatSumMs: min(max(b.LatSumMs, 0), float64(maxCounterValue)),
		Visitors: clampCounter(b.Visitors), Hll: hll, StatusCodes: statusJSON, Top: topJSON,
		Geo: marshalGeo(cleanGeo(b.Geo, geoLevel)),
	}, true
}

func cleanList(in []agentprotocol.TopEntry) []agentprotocol.TopEntry {
	if len(in) > maxTopEntries {
		in = in[:maxTopEntries]
	}
	out := make([]agentprotocol.TopEntry, 0, len(in))
	for _, e := range in {
		if e.K == "" {
			continue
		}
		if len(e.K) > maxKeyLen {
			e.K = e.K[:maxKeyLen]
		}
		e.N, e.E, e.B = clampCounter(e.N), clampCounter(e.E), clampCounter(e.B)
		e.Ms = min(max(e.Ms, 0), float64(maxCounterValue))
		out = append(out, e)
	}
	return out
}

// cleanReferrers masks IP-literal referrer hosts (older agents report them raw).
func cleanReferrers(in []agentprotocol.TopEntry) []agentprotocol.TopEntry {
	out := cleanList(in)
	for i := range out {
		out[i].K = agentprotocol.MaskIPHost(out[i].K)
	}
	return out
}

func cleanTop(t agentprotocol.TrafficTop) agentprotocol.TrafficTop {
	return agentprotocol.TrafficTop{
		Paths: cleanList(t.Paths), Pages: cleanList(t.Pages), Referrers: cleanReferrers(t.Referrers),
		Browsers: cleanList(t.Browsers), OS: cleanList(t.OS), Devices: cleanList(t.Devices), NotFound: cleanList(t.NotFound),
	}
}
