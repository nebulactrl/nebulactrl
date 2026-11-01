package notifications

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
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

// GenerateSigningSecret returns a new webhook signing secret ("whsec_" plus 32
// random bytes, base64url).
func GenerateSigningSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate signing secret: %w", err)
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b), nil
}
