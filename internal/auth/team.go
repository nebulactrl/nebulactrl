package auth

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// MaxTeamNameLength bounds a team name.
const MaxTeamNameLength = 80

const maxTeamSlugLen = 40

// NormalizeTeamName trims a team name and validates its length.
func NormalizeTeamName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > MaxTeamNameLength {
		return "", apierr.Unprocessable("validation_failed", fmt.Sprintf("team name must be 1-%d characters", MaxTeamNameLength))
	}
	return name, nil
}

// TeamSlug lowercases name and reduces it to [a-z0-9-]; "team" when nothing is
// left.
func TeamSlug(name string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLower(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxTeamSlugLen {
		out = strings.Trim(out[:maxTeamSlugLen], "-")
	}
	if out == "" {
		return "team"
	}
	return out
}

// NewTeam creates a team named name with a unique slug. It does not add any
// member: the caller inserts the owner membership in the same transaction.
func NewTeam(ctx context.Context, q *db.Queries, name string, createdBy *uuid.UUID) (db.Workspace, error) {
	name, err := NormalizeTeamName(name)
	if err != nil {
		return db.Workspace{}, err
	}
	base := TeamSlug(name)
	taken, err := q.ListTeamSlugsLike(ctx, base)
	if err != nil {
		return db.Workspace{}, fmt.Errorf("list team slugs: %w", err)
	}
	set := make(map[string]struct{}, len(taken))
	for _, t := range taken {
		set[t] = struct{}{}
	}
	slug := base
	for i := 2; ; i++ {
		if _, ok := set[slug]; !ok {
			break
		}
		slug = base + "-" + strconv.Itoa(i)
	}
	return q.CreateTeam(ctx, db.CreateTeamParams{Name: name, Slug: slug, CreatedBy: createdBy})
}
