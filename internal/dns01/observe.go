package dns01

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Observe stores the DNS-01 certificate states a heartbeat reports for a
// server, and which ACME storage holds each domain's certificate. It writes a
// row only when its state changed or the stored one is older than five
// minutes, and reports whether anything was written (the caller republishes the
// server then). Reports for rows that do not belong to the server are ignored.
func Observe(ctx context.Context, q *db.Queries, serverID uuid.UUID, statuses []agentprotocol.AcmeCertStatus) (changed bool, err error) {
	for _, st := range statuses {
		id, perr := uuid.Parse(st.ID)
		if perr != nil {
			continue
		}
		switch st.Status {
		case agentprotocol.AcmeStatusPending, agentprotocol.AcmeStatusActive, agentprotocol.AcmeStatusError:
		case "":
			// Only a domain host that still waits for a DNS-01 resolver is reported
			// without a status: what is stored is the storage of its certificate.
			if st.Kind != agentprotocol.AcmeKindDomain {
				continue
			}
		default:
			continue
		}
		var notAfter *time.Time
		if st.NotAfter != nil {
			t := st.NotAfter.UTC()
			notAfter = &t
		}
		msg := st.Message
		if len(msg) > 500 {
			msg = msg[:500]
		}
		switch st.Kind {
		case agentprotocol.AcmeKindWildcard:
			_, err = q.ObserveWildcardCert(ctx, db.ObserveWildcardCertParams{
				ID: id, ServerID: serverID, Status: st.Status, StatusMessage: msg, Issuer: st.Issuer, NotAfter: notAfter,
			})
		case agentprotocol.AcmeKindDomain:
			if st.Status != "" {
				_, err = q.ObserveDomainCert(ctx, db.ObserveDomainCertParams{
					ID: id, ServerID: serverID, Status: st.Status, StatusMessage: msg, Issuer: st.Issuer, NotAfter: notAfter,
				})
				if err == nil {
					changed = true
				} else if errors.Is(err, pgx.ErrNoRows) {
					err = nil
				} else {
					return changed, fmt.Errorf("record %s certificate status: %w", st.Kind, err)
				}
			}
			wrote, serr := observeStore(ctx, q, id, serverID, st.IssuedVia)
			if serr != nil {
				return changed, serr
			}
			changed = changed || wrote
			continue
		default:
			continue
		}
		switch {
		case err == nil:
			changed = true
		case errors.Is(err, pgx.ErrNoRows):
			err = nil
		default:
			return changed, fmt.Errorf("record %s certificate status: %w", st.Kind, err)
		}
	}
	return changed, nil
}

// observeStore records which storage holds the domain's certificate; an empty
// answer (no storage holds one) forgets the last.
func observeStore(ctx context.Context, q *db.Queries, id, serverID uuid.UUID, via string) (bool, error) {
	switch via {
	case agentprotocol.AcmeIssuedHTTP01, agentprotocol.AcmeIssuedDNS01:
		_, err := q.ObserveDomainCertStore(ctx, db.ObserveDomainCertStoreParams{ID: id, ServerID: serverID, IssuedVia: via})
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, pgx.ErrNoRows):
			return false, nil
		}
		return false, fmt.Errorf("record certificate storage: %w", err)
	case "":
		n, err := q.ClearDomainCertStore(ctx, db.ClearDomainCertStoreParams{ID: id, ServerID: serverID})
		if err != nil {
			return false, fmt.Errorf("clear certificate storage: %w", err)
		}
		return n > 0, nil
	}
	return false, nil
}
