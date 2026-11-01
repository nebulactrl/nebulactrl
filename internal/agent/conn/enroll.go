package conn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ErrEnrollRejected is returned when the control plane refuses the enrollment
// token (expired, used or revoked).
var ErrEnrollRejected = errors.New("enrollment token rejected")

// Enroll generates an Ed25519 keypair, registers it with the control plane using
// the one-time token and persists the resulting identity.
func Enroll(ctx context.Context, st *store.Store, enrollURL, token, hostname string, facts agentprotocol.Facts, hc *http.Client) (store.Identity, error) {
	if token == "" {
		return store.Identity{}, errors.New("no identity stored and no enrollment token provided (--token)")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return store.Identity{}, fmt.Errorf("generate key: %w", err)
	}
	body, err := json.Marshal(agentprotocol.EnrollRequest{Token: token, PublicKey: pub, Hostname: hostname, Facts: facts})
	if err != nil {
		return store.Identity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, enrollURL, bytes.NewReader(body))
	if err != nil {
		return store.Identity{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return store.Identity{}, fmt.Errorf("enroll request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return store.Identity{}, fmt.Errorf("%w: %s", ErrEnrollRejected, problemDetail(data))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return store.Identity{}, fmt.Errorf("enroll failed: HTTP %d: %s", resp.StatusCode, problemDetail(data))
	}
	var out agentprotocol.EnrollResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return store.Identity{}, fmt.Errorf("decode enroll response: %w", err)
	}
	if out.AgentID == "" || out.ServerID == "" {
		return store.Identity{}, errors.New("enroll response missing agentId/serverId")
	}
	id := store.Identity{AgentID: out.AgentID, ServerID: out.ServerID, PrivateKey: priv}
	if err := st.PutIdentity(id); err != nil {
		return store.Identity{}, fmt.Errorf("store identity: %w", err)
	}
	return id, nil
}

// problemDetail extracts a readable message from an RFC 9457 body, falling back
// to the raw text.
func problemDetail(data []byte) string {
	var p struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
	}
	if json.Unmarshal(data, &p) == nil {
		if p.Detail != "" {
			return p.Detail
		}
		if p.Title != "" {
			return p.Title
		}
	}
	return strings.TrimSpace(string(data))
}

// SignChallenge signs the handshake payload nonce || agentId || serverTime.
func SignChallenge(priv ed25519.PrivateKey, agentID string, ch agentprotocol.Challenge) []byte {
	return ed25519.Sign(priv, agentprotocol.SigningPayload(ch.Nonce, agentID, ch.ServerTime))
}
