package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MessageField is one key fact of a message.
type MessageField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Payload is an event as stored on a delivery and rendered by every channel.
// A retry renders the same payload, so it sends exactly what the first
// attempt did. It never holds credentials.
type Payload struct {
	Type      string         `json:"type"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	URL       string         `json:"url,omitempty"` // an in-app path or an absolute URL
	Fields    []MessageField `json:"fields,omitempty"`
	ProjectID string         `json:"projectId,omitempty"`
	ServerID  string         `json:"serverId,omitempty"`
	ServiceID string         `json:"serviceId,omitempty"`
	At        time.Time      `json:"at"`
	Test      bool           `json:"test,omitempty"`
	// Transactional marks a mail sent to one person for something they did (an
	// invite, a password reset), not an alert a channel subscribes to. Emails
	// then name the action in the button (LinkLabel) and omit the subscription
	// footer.
	Transactional bool   `json:"transactional,omitempty"`
	LinkLabel     string `json:"linkLabel,omitempty"`
}

// Message is a Payload resolved for rendering: the link is absolute (empty
// when neither the event nor the control plane knows one) and the delivery id
// is known.
type Message struct {
	Payload
	ID  string // delivery id
	URL string // absolute link or ""
	// Tone is ok, info, warn or bad.
	Tone string
}

// Limits that keep a message inside every channel's own limits.
const (
	maxTitle      = 200
	maxBody       = 1500
	maxFieldName  = 60
	maxFieldValue = 300
	maxFields     = 12
)

// Fields shown first, in this order; the rest follow alphabetically.
var fieldOrder = []string{"Project", "Environment", "Service", "Server"}

// cleanText removes control characters (newlines survive when keepNewlines)
// and trims the result.
func cleanText(s string, keepNewlines bool) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && keepNewlines:
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// truncate shortens s to at most n runes, ending with an ellipsis when cut.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "\u2026"
}

// orderedFields turns the caller's map into display order with limits applied.
func orderedFields(m map[string]string) []MessageField {
	var out []MessageField
	seen := map[string]bool{}
	add := func(k string) {
		v, ok := m[k]
		if !ok || seen[k] {
			return
		}
		seen[k] = true
		name, val := truncate(cleanText(k, false), maxFieldName), truncate(cleanText(v, true), maxFieldValue)
		if name == "" || val == "" {
			return
		}
		out = append(out, MessageField{Name: name, Value: val})
	}
	for _, k := range fieldOrder {
		add(k)
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		add(k)
	}
	if len(out) > maxFields {
		out = out[:maxFields]
	}
	return out
}

// newPayload normalizes an event for storage.
func newPayload(ev Event, fields map[string]string, severity, link string, at time.Time) Payload {
	p := Payload{
		Type:     ev.Type,
		Severity: severity,
		Title:    truncate(cleanText(ev.Title, false), maxTitle),
		Body:     truncate(cleanText(ev.Body, true), maxBody),
		URL:      link,
		Fields:   orderedFields(fields),
		At:       at.UTC().Truncate(time.Second),
	}
	if ev.ProjectID != nil {
		p.ProjectID = ev.ProjectID.String()
	}
	if ev.ServerID != nil {
		p.ServerID = ev.ServerID.String()
	}
	if ev.ServiceID != nil {
		p.ServiceID = ev.ServiceID.String()
	}
	if p.Title == "" {
		if e, ok := lookupType(ev.Type); ok {
			p.Title = e.Label
		} else {
			p.Title = ev.Type
		}
	}
	return p
}

// dedupeKey identifies identical events: same type, subjects, text and fields.
func dedupeKey(p Payload) string {
	h := sha256.New()
	w := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	w(p.Type)
	w(p.Severity)
	w(p.ProjectID)
	w(p.ServerID)
	w(p.ServiceID)
	w(p.Title)
	w(p.Body)
	for _, f := range p.Fields {
		w(f.Name)
		w(f.Value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resolveURL joins an in-app path onto the public URL. An absolute URL is kept;
// a path with no known public URL yields "".
func resolveURL(link, publicURL string) string {
	link = strings.TrimSpace(link)
	switch {
	case link == "":
		return ""
	case strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://"):
		return link
	case publicURL == "":
		return ""
	case strings.HasPrefix(link, "/"):
		return strings.TrimRight(publicURL, "/") + link
	}
	return strings.TrimRight(publicURL, "/") + "/" + link
}

// message resolves a stored payload for rendering.
func message(p Payload, id uuid.UUID, publicURL string) Message {
	return Message{Payload: p, ID: id.String(), URL: resolveURL(p.URL, publicURL), Tone: toneOf(p.Type, p.Severity)}
}

// fieldMap returns the fields as a map (for the webhook body).
func (m Message) fieldMap() map[string]string {
	out := make(map[string]string, len(m.Fields))
	for _, f := range m.Fields {
		out[f.Name] = f.Value
	}
	return out
}

// toneLabel is the short word that stands for the tone where colour is unavailable.
func toneLabel(tone string) string {
	switch tone {
	case toneBad:
		return "Alert"
	case toneWarn:
		return "Warning"
	case toneOK:
		return "Success"
	}
	return "Notice"
}
