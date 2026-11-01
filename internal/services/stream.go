package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

// Stream writes server-sent events from a huma.StreamResponse body. Unlike
// api.SSE it can emit named events (`event: end`).
type Stream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// NewStream prepares hctx for text/event-stream.
func NewStream(hctx huma.Context) (*Stream, error) {
	_, w := humachi.Unwrap(hctx)
	hctx.SetHeader("Content-Type", "text/event-stream")
	hctx.SetHeader("Cache-Control", "no-cache")
	hctx.SetHeader("Connection", "keep-alive")
	hctx.SetHeader("X-Accel-Buffering", "no")
	hctx.SetStatus(http.StatusOK)
	s := &Stream{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.SetWriteDeadline(time.Time{})
	if err := s.Comment("connected"); err != nil {
		return nil, err
	}
	return s, nil
}

// Data sends v as an unnamed JSON message.
func (s *Stream) Data(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Event sends v as a named JSON message.
func (s *Stream) Event(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Comment sends a comment line (heartbeat).
func (s *Stream) Comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	return s.rc.Flush()
}

// WriteJSON replies with a JSON body from a huma.StreamResponse body (used by
// endpoints that stream when following and return plain JSON otherwise).
func WriteJSON(hctx huma.Context, v any) {
	hctx.SetHeader("Content-Type", "application/json")
	hctx.SetStatus(http.StatusOK)
	_ = json.NewEncoder(hctx.BodyWriter()).Encode(v)
}

// StreamResponses documents the dual JSON / event-stream response of log endpoints.
func StreamResponses() map[string]*huma.Response {
	return map[string]*huma.Response{
		"200": {
			Description: "JSON `{items}` without `follow`; a `text/event-stream` with `follow=true`",
			Content: map[string]*huma.MediaType{
				"application/json":  {Schema: &huma.Schema{Type: "object"}},
				"text/event-stream": {Schema: &huma.Schema{Type: "string"}},
			},
		},
	}
}
