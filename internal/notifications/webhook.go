package notifications

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Headers of a generic webhook delivery.
const (
	// SignatureHeader carries "t=<unix seconds>,v1=<hex>": the HMAC-SHA256, keyed
	// with the channel's signing secret, of "<t>.<raw request body>".
	SignatureHeader = "X-NebulaCtrl-Signature"
	EventHeader     = "X-NebulaCtrl-Event"
	DeliveryHeader  = "X-NebulaCtrl-Delivery"
)

// DefaultSignatureTolerance is how far a signature's timestamp may differ from
// the receiver's clock before VerifySignature rejects it (replay protection).
const DefaultSignatureTolerance = 5 * time.Minute

type webhookBody struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Severity  string            `json:"severity"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	ProjectID string            `json:"projectId"`
	ServerID  string            `json:"serverId"`
	ServiceID string            `json:"serviceId"`
	Timestamp string            `json:"timestamp"`
	Test      bool              `json:"test"`
}

// FormatWebhook renders msg as the JSON body of a generic webhook delivery.
// Every key is always present (empty string / empty object when unknown), so
// receivers can rely on the shape.
func FormatWebhook(m Message) ([]byte, error) {
	return json.Marshal(webhookBody{
		ID: m.ID, Type: m.Type, Severity: m.Severity, Title: m.Title, Body: m.Payload.Body, URL: m.URL, Fields: m.fieldMap(),
		ProjectID: m.ProjectID, ServerID: m.ServerID, ServiceID: m.ServiceID, Timestamp: m.At.Format(time.RFC3339), Test: m.Test,
	})
}

// Sign returns the signature header value for body at time t.
func Sign(secret string, body []byte, t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + signHex(secret, ts, body)
}

func signHex(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a signature header against the raw body: the timestamp
// must be within tolerance of now (0: DefaultSignatureTolerance) and at least
// one v1 entry must equal the expected HMAC (compared in constant time).
// Receivers must verify the raw bytes, not a re-serialized JSON value.
func VerifySignature(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultSignatureTolerance
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return errors.New("malformed signature header")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("malformed signature timestamp")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("signature timestamp outside the %s tolerance", tolerance)
	}
	want := []byte(signHex(secret, ts, body))
	for _, s := range sigs {
		if subtle.ConstantTimeCompare(want, []byte(s)) == 1 {
			return nil
		}
	}
	return errors.New("signature mismatch")
}

// GenerateSigningSecret returns a new webhook signing secret ("whsec_" plus 32
// random bytes, base64url).
func GenerateSigningSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate signing secret: %w", err)
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b), nil
}
