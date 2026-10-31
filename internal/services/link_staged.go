package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// refApplied reports whether a release already holds variable key as a
// reference to the service with slug provider.
func refApplied(release map[string]RawVar, key, provider string) bool {
	v, ok := release[key]
	if !ok {
		return false
	}
	for _, ref := range References(v.Value) {
		if ref[0] == provider {
			return true
		}
	}
	return false
}

// MarkStagedLinks flags the references (and the links they make) that the
// consumer's running release does not hold yet: they are staged and take
// effect on its next deploy.
//
// clean lists consumers with nothing staged at all (generation equals the
// applied one); their references are all applied. release holds the user
// variables frozen into a consumer's current release; a consumer without an
// entry has never been deployed, so every reference is staged. A link is
// staged when every one of its references is, so an edge that is already
// live stays solid while one more reference is added to it. Links without
// references (hand-drawn) are never staged.
func MarkStagedLinks(links []LinkView, slugOf map[uuid.UUID]string, clean map[uuid.UUID]bool, release map[uuid.UUID]map[string]RawVar) {
	for i := range links {
		l := &links[i]
		if len(l.References) == 0 {
			continue
		}
		staged := true
		for j := range l.References {
			ref := &l.References[j]
			ref.Staged = !clean[l.FromServiceID] && !refApplied(release[l.FromServiceID], ref.Key, slugOf[l.ToServiceID])
			if !ref.Staged {
				staged = false
			}
		}
		l.Staged = staged
	}
}

// markStaged loads what MarkStagedLinks needs: the services of the
// environment and, for every consumer with staged changes, its current release.
func (s *Service) markStaged(ctx context.Context, envID uuid.UUID, links []LinkView) error {
	need := map[uuid.UUID]bool{}
	for _, l := range links {
		if len(l.References) > 0 {
			need[l.FromServiceID] = true
		}
	}
	if len(need) == 0 {
		return nil
	}
	svcs, err := s.q.ListAllEnvironmentServices(ctx, envID)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	slugOf := make(map[uuid.UUID]string, len(svcs))
	clean := map[uuid.UUID]bool{}
	release := map[uuid.UUID]map[string]RawVar{}
	for _, svc := range svcs {
		slugOf[svc.ID] = svc.Slug
		if !need[svc.ID] {
			continue
		}
		if svc.CurrentDeploymentID != nil && svc.Generation == svc.AppliedGeneration {
			clean[svc.ID] = true
			continue
		}
		if svc.CurrentDeploymentID == nil {
			continue
		}
		row, err := s.q.GetServiceCurrentRelease(ctx, svc.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("get current release: %w", err)
		}
		raw, err := ReleaseRaw(s.box, db.Release{ID: row.ID, EnvEnc: row.EnvEnc})
		if err != nil {
			return err
		}
		release[svc.ID] = raw
	}
	MarkStagedLinks(links, slugOf, clean, release)
	return nil
}
