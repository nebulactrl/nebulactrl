package agentprotocol

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Well-known names of the pages server (maintenance and error pages).
const (
	// PagesContainer is the static pages server container on the edge network.
	PagesContainer = "nebulactrl-pages"
	// PagesContainerAlt is the name the pages container takes while it is
	// replaced (the new one starts under the name the old one does not hold, so
	// the pages never go unserved). Traefik reaches either through the network
	// alias PagesContainer.
	PagesContainerAlt = "nebulactrl-pages-alt"
	// PagesPort is the port the pages server listens on inside the edge network.
	PagesPort = 8080
)

// Limits of the free text of a maintenance page.
const (
	MaxMaintenanceTitle   = 80
	MaxMaintenanceMessage = 600
)

// Maintenance puts a route into maintenance mode: while a Route carries it,
// every serving router of the route answers 503 with the maintenance page
// instead of reaching the upstream. The probe router keeps the real service.
type Maintenance struct {
	// Title and Message are plain text (the agent HTML-escapes them). Empty
	// values render the default copy.
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
	// ETA is when the service is expected back. It is shown on the page and
	// becomes the Retry-After header.
	ETA *time.Time `json:"eta,omitempty"`
}

// ValidateMaintenance checks the free text of a maintenance page: bounded
// length, valid UTF-8, no control characters other than newlines in the message.
func ValidateMaintenance(m Maintenance) error {
	if err := validateText("title", m.Title, MaxMaintenanceTitle, false); err != nil {
		return err
	}
	return validateText("message", m.Message, MaxMaintenanceMessage, true)
}

func validateText(field, s string, max int, newlines bool) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s is not valid UTF-8", field)
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s is longer than %d characters", field, max)
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(newlines && r == '\n') {
			return errors.New(field + " contains a control character")
		}
	}
	return nil
}

// CleanMaintenanceText trims s and cuts it to max runes; the agent applies it
// to whatever it receives, so a page can never carry unbounded text.
func CleanMaintenanceText(s string, max int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}
