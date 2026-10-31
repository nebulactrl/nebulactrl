package api

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// Pagination limits.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// PageQuery is embedded in list-operation inputs.
type PageQuery struct {
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous page's nextCursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size (default 50, max 200)"`
}

// Size returns the effective page size.
func (q PageQuery) Size() int {
	switch {
	case q.Limit <= 0:
		return DefaultPageSize
	case q.Limit > MaxPageSize:
		return MaxPageSize
	}
	return q.Limit
}

// Page is the response envelope of every list endpoint.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty" doc:"Present when more items exist"`
}

// EncodeTimeCursor encodes a (timestamp, id) keyset position.
func EncodeTimeCursor(t time.Time, id uuid.UUID) string {
	raw := strconv.FormatInt(t.UnixMicro(), 10) + "." + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeTimeCursor decodes EncodeTimeCursor. An empty cursor yields ok=false.
func DecodeTimeCursor(c string) (t time.Time, id uuid.UUID, ok bool, err error) {
	if c == "" {
		return time.Time{}, uuid.Nil, false, nil
	}
	bad := apierr.BadRequest("invalid_cursor", "invalid cursor")
	b, derr := base64.RawURLEncoding.DecodeString(c)
	if derr != nil {
		return time.Time{}, uuid.Nil, false, bad
	}
	us, rest, found := strings.Cut(string(b), ".")
	if !found {
		return time.Time{}, uuid.Nil, false, bad
	}
	micro, perr := strconv.ParseInt(us, 10, 64)
	if perr != nil {
		return time.Time{}, uuid.Nil, false, bad
	}
	id, perr = uuid.Parse(rest)
	if perr != nil {
		return time.Time{}, uuid.Nil, false, bad
	}
	return time.UnixMicro(micro).UTC(), id, true, nil
}
