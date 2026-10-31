package registries

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Image states (registry_images.state).
const (
	ImageLive    = "live"
	ImageExpired = "expired"
	ImageDeleted = "deleted"
)

// Page sizes of the image list.
const (
	DefaultImageLimit = 50
	MaxImageLimit     = 200
)

// RegistryImageView is one image NebulaCtrl pushed to a registry.
type RegistryImageView struct {
	ID         uuid.UUID  `json:"id"`
	Repository string     `json:"repository" doc:"The repository as recorded when the image was pushed."`
	Digest     string     `json:"digest" doc:"Manifest digest, sha256:…"`
	Reference  string     `json:"reference" doc:"The pull reference, host/repository@digest."`
	Platform   string     `json:"platform"`
	SizeBytes  *int64     `json:"sizeBytes,omitempty"`
	State      string     `json:"state" enum:"live,expired,deleted" doc:"live: in the rollback window. expired: outside it; the built-in registry deletes it at the next sweep. deleted: removed from the registry."`
	PushedAt   time.Time  `json:"pushedAt"`
	ServiceID  *uuid.UUID `json:"serviceId,omitempty"`
	ReleaseID  *uuid.UUID `json:"releaseId,omitempty"`
}

// RegistryImagePage is a page of images, newest first.
type RegistryImagePage struct {
	Items      []RegistryImageView `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// ListImages returns the images pushed to a registry, newest first.
func (s *Service) ListImages(ctx context.Context, id uuid.UUID, state, cursor string, limit int) (RegistryImagePage, error) {
	ws, err := s.workspace(ctx, policy.ScopeProjectsRead)
	if err != nil {
		return RegistryImagePage{}, err
	}
	r, err := s.load(ctx, ws, id)
	if err != nil {
		return RegistryImagePage{}, err
	}
	if limit <= 0 {
		limit = DefaultImageLimit
	}
	limit = min(limit, MaxImageLimit)
	p := db.ListRegistryImagesParams{RegistryID: id, RowLimit: int32(limit) + 1}
	switch state {
	case "":
	case ImageLive, ImageExpired, ImageDeleted:
		p.State = &state
	default:
		return RegistryImagePage{}, apierr.Unprocessable("validation_failed", "state must be live, expired or deleted")
	}
	if cursor != "" {
		at, cid, err := decodeImageCursor(cursor)
		if err != nil {
			return RegistryImagePage{}, apierr.BadRequest("invalid_cursor", "the cursor is not valid")
		}
		p.CursorAt, p.CursorID = &at, &cid
	}
	rows, err := s.q.ListRegistryImages(ctx, p)
	if err != nil {
		return RegistryImagePage{}, fmt.Errorf("list registry images: %w", err)
	}
	page := RegistryImagePage{Items: make([]RegistryImageView, 0, min(len(rows), limit))}
	for i, row := range rows {
		if i == limit {
			last := rows[limit-1]
			page.NextCursor = encodeImageCursor(last.PushedAt, last.ID)
			break
		}
		page.Items = append(page.Items, RegistryImageView{
			ID: row.ID, Repository: row.Repository, Digest: row.Digest, Reference: imageReference(r.Host, row.Repository, row.Digest),
			Platform: row.Platform, SizeBytes: row.SizeBytes, State: row.State, PushedAt: row.PushedAt, ServiceID: row.ServiceID, ReleaseID: row.ReleaseID,
		})
	}
	return page, nil
}

// imageReference is host/repository@digest; a repository recorded with its host
// already (host/ns/repo) is not given a second one.
func imageReference(host, repository, digest string) string {
	if !strings.HasPrefix(repository, host+"/") {
		repository = host + "/" + repository
	}
	return repository + "@" + digest
}

func encodeImageCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeImageCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	ts, ids, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, fmt.Errorf("malformed cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(ids)
	return at, id, err
}
