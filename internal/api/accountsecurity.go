package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// LiveTokenBody is an API token that still works, listed after a password
// change or reset so the person can see what the new password did not end.
type LiveTokenBody struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix" doc:"The visible start of the token"`
	TeamID     uuid.UUID  `json:"teamId"`
	TeamName   string     `json:"teamName"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

// LiveTokenBodies converts token summaries (never nil, so JSON carries []).
func LiveTokenBodies(ts []auth.TokenSummary) []LiveTokenBody {
	out := make([]LiveTokenBody, 0, len(ts))
	for _, t := range ts {
		out = append(out, LiveTokenBody{
			ID: t.ID, Name: t.Name, Prefix: t.Prefix, TeamID: t.TeamID, TeamName: t.TeamName,
			CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
		})
	}
	return out
}
