package deploykeys

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// HasKey reports whether the service has a deploy key.
func (s *Service) HasKey(ctx context.Context, serviceID uuid.UUID) bool {
	_, err := s.q.GetServiceSSHKey(ctx, serviceID)
	return err == nil
}

// Credentials returns what a clone of rawURL needs: the deploy key and the
// pinned host key of the URL's host. It returns nil for a URL that is not an
// SSH URL, and ErrNoKey when the service has none. The first call for a host
// records its key (trust on first use). It is for the deployment pipeline and
// does no authorization.
func (s *Service) Credentials(ctx context.Context, serviceID uuid.UUID, rawURL string) (*gitssh.Credentials, error) {
	if !gitssh.LooksLikeSSH(rawURL) {
		return nil, nil
	}
	ep, err := gitssh.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid ssh git url: %w", err)
	}
	k, err := s.q.GetServiceSSHKey(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, gitssh.ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("get ssh key: %w", err)
	}
	pem, err := s.box.Open(secretbox.ServiceSSHKeysPrivateKey, serviceID.String(), k.PrivateKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt deploy key: %w", err)
	}
	pinned, err := s.pinnedHost(ctx, serviceID, ep)
	if err != nil {
		return nil, err
	}
	return &gitssh.Credentials{PrivateKeyPEM: pem, KnownHosts: []string{pinned.Line()}}, nil
}

// pinnedHost returns the pinned key of ep's host, recording the key the host
// presents when none is pinned yet.
func (s *Service) pinnedHost(ctx context.Context, serviceID uuid.UUID, ep gitssh.Endpoint) (gitssh.HostKey, error) {
	row, err := s.q.GetServiceSSHHost(ctx, db.GetServiceSSHHostParams{ServiceID: serviceID, Host: ep.HostName()})
	if err == nil {
		return gitssh.HostKeyFromAuthorized(row.Host, row.PublicKey)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gitssh.HostKey{}, fmt.Errorf("get ssh host: %w", err)
	}
	scanned, err := gitssh.ScanHostKey(ctx, ep)
	if err != nil {
		return gitssh.HostKey{}, err
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return gitssh.HostKey{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.PinServiceSSHHostIfAbsent(ctx, db.PinServiceSSHHostIfAbsentParams{
			ServiceID: serviceID, Host: scanned.Host, KeyType: scanned.Type, PublicKey: scanned.Authorized(), Fingerprint: scanned.Fingerprint,
		})
		if err != nil || n == 0 {
			return err // n == 0: a concurrent fetch pinned first; the row below wins
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionHostPin, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"host": scanned.Host, "fingerprint": scanned.Fingerprint, "firstUse": true},
		})
	})
	if err != nil {
		return gitssh.HostKey{}, fmt.Errorf("pin ssh host key: %w", err)
	}
	s.publish(sc)
	row, err = s.q.GetServiceSSHHost(ctx, db.GetServiceSSHHostParams{ServiceID: serviceID, Host: ep.HostName()})
	if err != nil {
		return gitssh.HostKey{}, fmt.Errorf("get ssh host: %w", err)
	}
	return gitssh.HostKeyFromAuthorized(row.Host, row.PublicKey)
}

// Resolve is git ls-remote over SSH: it returns the commit of branch in rawURL
// using the service's deploy key and pinned host key. A host key that differs
// from the pinned one fails with *gitssh.MismatchError and is recorded so the
// UI can offer "Accept new host key".
func (s *Service) Resolve(ctx context.Context, serviceID uuid.UUID, rawURL, branch string) (string, error) {
	creds, err := s.Credentials(ctx, serviceID, rawURL)
	if err != nil {
		return "", err
	}
	if creds == nil {
		return "", errors.New("not an SSH URL")
	}
	ep, err := gitssh.ParseURL(rawURL)
	if err != nil {
		return "", err
	}
	// go-git dials the SSH host itself, so the address policy of the clone
	// path is applied here before it connects.
	if err := netguard.CheckHost(ctx, ep.Host); err != nil {
		return "", err
	}
	auth, pins, err := creds.Auth(ep)
	if err != nil {
		return "", err
	}
	sha, err := gitssh.ResolveRef(ctx, rawURL, branch, auth)
	if err == nil {
		return sha, nil
	}
	err = gitssh.Explain(err, ep, pins)
	var mm *gitssh.MismatchError
	if errors.As(err, &mm) {
		s.recordMismatch(ctx, serviceID, ep, mm)
	}
	return "", err
}

// recordMismatch remembers the refused key (scanning for it when the server no
// longer offers the pinned key type) so the UI shows it with the accept action.
func (s *Service) recordMismatch(ctx context.Context, serviceID uuid.UUID, ep gitssh.Endpoint, mm *gitssh.MismatchError) {
	if mm.Offered.Fingerprint == "" {
		if k, err := gitssh.ScanHostKey(ctx, ep); err == nil {
			mm.Offered = k
		}
	}
	if mm.Offered.Fingerprint == "" {
		return
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return
	}
	prev, err := s.q.GetServiceSSHHost(ctx, db.GetServiceSSHHostParams{ServiceID: serviceID, Host: mm.Host})
	if err != nil || (prev.MismatchFingerprint != nil && *prev.MismatchFingerprint == mm.Offered.Fingerprint) {
		return // unknown host, or this very key was already recorded
	}
	_ = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).RecordServiceSSHHostMismatch(ctx, db.RecordServiceSSHHostMismatchParams{
			ServiceID: serviceID, Host: mm.Host, MismatchFingerprint: &mm.Offered.Fingerprint,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionHostClash, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"host": mm.Host, "pinned": mm.Pinned[0].Fingerprint, "offered": mm.Offered.Fingerprint},
		})
	})
	s.publish(sc)
}
