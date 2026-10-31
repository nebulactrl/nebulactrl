package templates

import (
	"encoding/json"
	"fmt"
)

// AppVersion is the version of the app a template pins: the tag of the first
// image service that is neither a one-shot job nor a managed database, as
// written in the template ("3.4.1", "v2.0.0", "1.8-alpine"). It is "" for a
// template without an image service. It is display text; the revision is what
// the update flow compares.
func AppVersion(t *CatalogTemplate) string {
	for _, s := range t.Services {
		if s.Image == "" || s.Database != nil || s.OneShot {
			continue
		}
		_, tag := SplitImage(s.Image)
		return tag
	}
	for _, s := range t.Services {
		if s.Image != "" {
			_, tag := SplitImage(s.Image)
			return tag
		}
	}
	return ""
}

// encodeSnapshot serialises the template as installed. The text keeps its
// ${input:KEY} placeholders and generators; no input value is part of a
// template, so the snapshot carries no secret.
func encodeSnapshot(t *CatalogTemplate) ([]byte, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("encode template snapshot: %w", err)
	}
	return raw, nil
}

// decodeSnapshot reads a snapshot written by encodeSnapshot.
func decodeSnapshot(raw []byte) (*CatalogTemplate, error) {
	var t CatalogTemplate
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("decode template snapshot: %w", err)
	}
	return &t, nil
}
