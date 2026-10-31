package proxyserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// ServerCertificate is the API view of an uploaded certificate. The private key is
// never part of it.
type ServerCertificate struct {
	ID            uuid.UUID  `json:"id"`
	ServerID      uuid.UUID  `json:"serverId"`
	Name          string     `json:"name"`
	Domains       []string   `json:"domains" doc:"Subject Alternative Names (DNS and IP)"`
	Issuer        string     `json:"issuer"`
	Fingerprint   string     `json:"fingerprint" doc:"SHA-256 of the leaf certificate, hex"`
	NotBefore     time.Time  `json:"notBefore"`
	NotAfter      time.Time  `json:"notAfter"`
	DaysToExpiry  int        `json:"daysToExpiry" doc:"Whole days until NotAfter (negative when expired)"`
	Status        string     `json:"status" enum:"pending,active,error" doc:"pending: never applied; active: loaded by the proxy; error: expired or the proxy could not load it"`
	StatusMessage string     `json:"statusMessage"`
	Change        string     `json:"change" enum:"none,add,remove" doc:"Staged change awaiting Deploy proxy"`
	AppliedAt     *time.Time `json:"appliedAt"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func certView(c db.Certificate, now time.Time) ServerCertificate {
	change := "none"
	switch {
	case c.DeletedAt != nil:
		change = "remove"
	case c.AppliedAt == nil:
		change = "add"
	}
	return ServerCertificate{
		ID: c.ID, ServerID: c.ServerID, Name: c.Name, Domains: nonNilStrings(c.Domains), Issuer: c.Issuer,
		Fingerprint: c.Fingerprint, NotBefore: c.NotBefore, NotAfter: c.NotAfter, DaysToExpiry: daysUntil(c.NotAfter, now),
		Status: c.Status, StatusMessage: c.StatusMessage, Change: change, AppliedAt: c.AppliedAt, CreatedAt: c.CreatedAt,
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// daysUntil is the whole number of days until t, rounded down (negative once expired).
func daysUntil(t, now time.Time) int {
	return int(math.Floor(t.Sub(now).Hours() / 24))
}

// UploadCertificateInput is a certificate upload.
type UploadCertificateInput struct {
	Name    string
	CertPEM string
	KeyPEM  string
}

// UploadCertificate validates and stages a certificate for a server. It takes
// effect with the next "Deploy proxy".
func (s *Service) UploadCertificate(ctx context.Context, serverID uuid.UUID, in UploadCertificateInput) (ServerCertificate, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, serverID)
	if err != nil {
		return ServerCertificate{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return ServerCertificate{}, apierr.Unprocessable("validation_failed", "name must be 1-100 characters")
	}
	if err := requireV2(srv); err != nil {
		return ServerCertificate{}, err
	}
	parsed, err := ParseCertificate(in.CertPEM, in.KeyPEM, s.now())
	if err != nil {
		return ServerCertificate{}, err
	}
	certID := uuid.New()
	sealed, err := s.box.SealString(secretbox.CertificatesKey, certID.String(), parsed.KeyPEM)
	if err != nil {
		return ServerCertificate{}, fmt.Errorf("seal certificate key: %w", err)
	}

	var row db.Certificate
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: serverID, WorkspaceID: srv.WorkspaceID}); err != nil {
			return err
		}
		existing, err := q.ListDesiredCertificates(ctx, serverID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.Fingerprint == parsed.Fingerprint {
				return apierr.Conflict("certificate_exists", fmt.Sprintf("this certificate is already uploaded as %q", e.Name))
			}
		}
		row, err = q.InsertCertificate(ctx, db.InsertCertificateParams{
			ID: certID, WorkspaceID: srv.WorkspaceID, ServerID: serverID, Name: name, Domains: parsed.Domains, CertPem: parsed.CertPEM,
			KeyEnc: sealed, Fingerprint: parsed.Fingerprint, Issuer: parsed.Issuer, NotBefore: parsed.NotBefore, NotAfter: parsed.NotAfter,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return apierr.Conflict("certificate_name_taken", fmt.Sprintf("a certificate named %q already exists on this server", name))
			}
			return err
		}
		if err := q.BumpServerProxyGeneration(ctx, serverID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "certificate.upload", ResourceType: "certificate", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": name, "domains": parsed.Domains, "serverId": serverID.String()},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ServerCertificate{}, err
		}
		return ServerCertificate{}, fmt.Errorf("upload certificate: %w", err)
	}
	s.publish(serverID)
	return certView(row, s.now()), nil
}

// ListCertificates returns every certificate of a server, staged removals included.
func (s *Service) ListCertificates(ctx context.Context, serverID uuid.UUID) ([]ServerCertificate, error) {
	if _, err := s.loadServer(ctx, s.q, policy.ScopeServersRead, serverID); err != nil {
		return nil, err
	}
	return s.certViews(ctx, s.q, serverID)
}

func (s *Service) certViews(ctx context.Context, q *db.Queries, serverID uuid.UUID) ([]ServerCertificate, error) {
	rows, err := q.ListServerCertificates(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	now := s.now()
	out := make([]ServerCertificate, 0, len(rows))
	for _, r := range rows {
		out = append(out, certView(r, now))
	}
	return out, nil
}

// DeleteCertificate removes a certificate. One that was never applied is
// deleted right away; an applied one is staged for removal until the next
// "Deploy proxy". Refused while a domain pins it.
func (s *Service) DeleteCertificate(ctx context.Context, id uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeProxyWrite, nil); err != nil {
		return err
	}
	ws := policy.WorkspaceID(ctx)
	var serverID uuid.UUID
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		c, err := q.GetCertificateScoped(ctx, db.GetCertificateScopedParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.DeletedAt != nil) {
			return apierr.NotFound("certificate")
		}
		if err != nil {
			return err
		}
		serverID = c.ServerID
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: c.ServerID, WorkspaceID: ws}); err != nil {
			return err
		}
		pins, err := q.CountDomainsPinningCertificate(ctx, &id)
		if err != nil {
			return err
		}
		if pins > 0 {
			return apierr.Conflict("certificate_in_use", fmt.Sprintf("%d domain(s) use this certificate; unpin them first", pins))
		}
		staged := c.AppliedAt != nil
		if staged {
			err = q.SoftDeleteCertificate(ctx, id)
		} else {
			err = q.HardDeleteCertificate(ctx, id)
		}
		if err != nil {
			return err
		}
		if err := q.BumpServerProxyGeneration(ctx, c.ServerID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "certificate.delete", ResourceType: "certificate", ResourceID: id.String(),
			Metadata: map[string]any{"name": c.Name, "stagedRemoval": staged, "serverId": c.ServerID.String()},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete certificate: %w", err)
	}
	s.publish(serverID)
	return nil
}
