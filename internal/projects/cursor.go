package projects

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
)

// EncodeCursor and DecodeCursor wrap the shared keyset cursor helpers.
func EncodeCursor(t time.Time, id uuid.UUID) string { return api.EncodeTimeCursor(t, id) }

// DecodeCursor decodes a cursor; ok is false for an empty cursor.
func DecodeCursor(c string) (time.Time, uuid.UUID, bool, error) { return api.DecodeTimeCursor(c) }
