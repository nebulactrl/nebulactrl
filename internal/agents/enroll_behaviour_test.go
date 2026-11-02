package agents_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// postEnrollRaw posts an arbitrary body to the enroll route and returns the
// status, headers and the problem code (if the answer is one).
func (h *harness) postEnrollRaw(body []byte) (*http.Response, string) {
	h.t.Helper()
	resp, err := http.Post(h.srv.URL+"/agent/v1/enroll", "application/json", bytes.NewReader(body))
	require.NoError(h.t, err)
	defer resp.Body.Close()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	return resp, p.Code
}

func enrollBody(token string, pub []byte) []byte {
	b, _ := json.Marshal(agentprotocol.EnrollRequest{Token: token, PublicKey: pub, Hostname: "host-1"})
	return b
}

func newKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub
}

func (h *harness) tokenUsed(token string) (used, revoked bool) {
	h.t.Helper()
	require.NoError(h.t, h.pool.QueryRow(h.ctx,
		"SELECT used_at IS NOT NULL, revoked_at IS NOT NULL FROM enrollment_tokens WHERE token_hash = $1",
		agents.HashEnrollmentToken(token)).Scan(&used, &revoked))
	return used, revoked
}

func (h *harness) identities(serverID uuid.UUID) (n int) {
	h.t.Helper()
	require.NoError(h.t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM agent_identities WHERE server_id = $1", serverID).Scan(&n))
	return n
}

func TestNewEnrollmentTokensAreUniqueAndHashToWhatIsStored(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		tok, hash, err := agents.NewEnrollmentToken()
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(tok, agents.EnrollmentTokenPrefix))
		require.False(t, seen[tok], "tokens never repeat")
		seen[tok] = true
		require.Equal(t, agents.HashEnrollmentToken(tok), hash)
		require.Len(t, hash, 32)
	}
	require.NotEqual(t, agents.HashEnrollmentToken("a"), agents.HashEnrollmentToken("b"))
}

func TestEnrollRejectsBadRequestsAndLeavesTheTokenUsable(t *testing.T) {
	h := newHarness(t)
	sid, tok := h.newToken("edge-1")
	pub := newKey(t)

	resp, code := h.postEnrollRaw([]byte("not json"))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "invalid_body", code)

	huge := append(append([]byte(`{"token":"`), bytes.Repeat([]byte("x"), 1<<20+512)...), `"}`...)
	resp, code = h.postEnrollRaw(huge)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "a body over 1 MiB is not read")
	require.Equal(t, "invalid_body", code)

	resp, code = h.postEnrollRaw(enrollBody(tok, pub[:16]))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "invalid_public_key", code)

	resp, code = h.postEnrollRaw(enrollBody("nct_other_"+strings.TrimPrefix(tok, agents.EnrollmentTokenPrefix), pub))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a token without the enrollment prefix is refused")
	require.Equal(t, "invalid_enrollment_token", code)

	used, _ := h.tokenUsed(tok)
	require.False(t, used, "none of the refusals consumed the token")
	require.Zero(t, h.identities(sid))

	// The same token still enrolls.
	status, out := h.postEnroll(tok, pub)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, sid.String(), out.ServerID)
}

func TestARevokedEnrollmentTokenCannotEnroll(t *testing.T) {
	h := newHarness(t)
	sid, tok := h.newToken("edge-1")
	_, err := h.pool.Exec(h.ctx, "UPDATE enrollment_tokens SET revoked_at = now() WHERE token_hash = $1", agents.HashEnrollmentToken(tok))
	require.NoError(t, err)

	resp, code := h.postEnrollRaw(enrollBody(tok, newKey(t)))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, "invalid_enrollment_token", code)
	require.Zero(t, h.identities(sid), "no agent was registered")
	used, _ := h.tokenUsed(tok)
	require.False(t, used)
}

func TestAnEnrollmentTokenOfARevokedServerCannotEnroll(t *testing.T) {
	h := newHarness(t)
	sid, tok := h.newToken("edge-1")
	_, err := h.pool.Exec(h.ctx, "UPDATE servers SET status = 'revoked' WHERE id = $1", sid)
	require.NoError(t, err)

	resp, code := h.postEnrollRaw(enrollBody(tok, newKey(t)))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, "invalid_enrollment_token", code)
	require.Zero(t, h.identities(sid))
	used, _ := h.tokenUsed(tok)
	require.False(t, used, "the refused redemption did not burn the token")
}

func TestASecondTokenCannotEnrollASecondAgentForTheSameServer(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	var firstKey []byte
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT public_key FROM agent_identities WHERE server_id = $1", e.serverID).Scan(&firstKey))

	tok, hash, err := agents.NewEnrollmentToken()
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx,
		"INSERT INTO enrollment_tokens (workspace_id, server_id, token_hash, expires_at) VALUES ($1, $2, $3, now() + interval '1 hour')",
		h.ws, e.serverID, hash)
	require.NoError(t, err)

	resp, code := h.postEnrollRaw(enrollBody(tok, newKey(t)))
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Equal(t, "already_enrolled", code)

	require.Equal(t, 1, h.identities(e.serverID))
	var keyNow []byte
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT public_key FROM agent_identities WHERE server_id = $1", e.serverID).Scan(&keyNow))
	require.Equal(t, firstKey, keyNow, "the enrolled agent's key is untouched")
	used, _ := h.tokenUsed(tok)
	require.False(t, used, "the failed enrolment rolled back completely")
}

func TestEnrollRegistersTheAgentRecordsFactsAuditsAndAnnouncesTheServer(t *testing.T) {
	h := newHarness(t)
	sid, tok := h.newToken("edge-1")
	_, err := h.pool.Exec(h.ctx, "UPDATE servers SET name = '' WHERE id = $1", sid)
	require.NoError(t, err)
	pub := newKey(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.bus.Subscribe(ctx)

	body, _ := json.Marshal(agentprotocol.EnrollRequest{Token: tok, PublicKey: pub, Hostname: "  edge-1.example.com \n", Facts: agentprotocol.Facts{OS: "linux", Arch: "arm64", CPUs: 8}})
	resp, code := h.postEnrollRaw(body)
	require.Equal(t, http.StatusOK, resp.StatusCode, code)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	srv, err := h.q.GetServerByID(h.ctx, sid)
	require.NoError(t, err)
	require.Equal(t, "edge-1.example.com", srv.Name, "a server without a name takes the trimmed hostname")
	var facts agentprotocol.Facts
	require.NoError(t, json.Unmarshal(srv.Facts, &facts))
	require.Equal(t, agentprotocol.Facts{OS: "linux", Arch: "arm64", CPUs: 8}, facts)

	used, _ := h.tokenUsed(tok)
	require.True(t, used)
	var stored []byte
	var agentID uuid.UUID
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT id, public_key FROM agent_identities WHERE server_id = $1", sid).Scan(&agentID, &stored))
	require.Equal(t, []byte(pub), stored)

	var action, resource, auditAgent string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		"SELECT action, resource_id, metadata->>'agentId' FROM audit_events WHERE action = 'server.enroll'").Scan(&action, &resource, &auditAgent))
	require.Equal(t, sid.String(), resource)
	require.Equal(t, agentID.String(), auditAgent)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == "server.updated" && ev.ResourceID == sid.String() {
				return
			}
		case <-deadline:
			t.Fatal("the enrolled server was not announced")
		}
	}
}

func TestEnrollmentAttemptsAreRateLimitedPerAddressWithoutConsumingTokens(t *testing.T) {
	h := newHarness(t)
	sid, tok := h.newToken("edge-1")
	pub := newKey(t)
	unknown := "nct_enroll_" + strings.Repeat("a", 52)

	for i := range 10 {
		resp, _ := h.postEnrollRaw(enrollBody(unknown, pub))
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "attempt %d", i+1)
	}
	resp, code := h.postEnrollRaw(enrollBody(tok, pub))
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "60", resp.Header.Get("Retry-After"))
	require.NotEmpty(t, code)
	used, _ := h.tokenUsed(tok)
	require.False(t, used, "a throttled request never reaches the token")
	require.Zero(t, h.identities(sid))
}
