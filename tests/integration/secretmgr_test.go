//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// openBaoImage is the official OpenBao image, pinned by its index digest
// (checked against Docker Hub: no pull is needed to read it).
const openBaoImage = "openbao/openbao:2.7.2@sha256:5046911075e137e154ce49ab051502f4ec7cf367d484ae4a4dce665d9b90561f"

const openBaoRootToken = "nctest-openbao-root"

// baoAPI is a tiny client of the dev-mode OpenBao the test starts.
type baoAPI struct {
	t    *testing.T
	base string
}

func (b *baoAPI) do(method, path string, body any) (int, []byte) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(b.t, err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.base+"/v1"+path, rd)
	require.NoError(b.t, err)
	req.Header.Set("X-Vault-Token", openBaoRootToken)
	res, err := http.DefaultClient.Do(req)
	require.NoError(b.t, err)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

// put writes a KV v2 secret.
func (b *baoAPI) put(path string, data map[string]any) {
	b.t.Helper()
	code, raw := b.do("POST", "/secret/data/"+path, map[string]any{"data": data})
	require.Equal(b.t, http.StatusOK, code, string(raw))
}

// startOpenBao runs OpenBao in dev mode (KV v2 at secret/, a fixed root token)
// and removes the container and the image when the test ends.
func startOpenBao(t *testing.T) *baoAPI {
	t.Helper()
	port, err := freePort()
	require.NoError(t, err)
	name := fmt.Sprintf("nctest-openbao-%d", port)
	out, err := sh(context.Background(), "", "docker", "run", "-d", "--name", name, "--cap-add", "IPC_LOCK",
		"-p", fmt.Sprintf("127.0.0.1:%d:8200", port),
		"-e", "BAO_DEV_ROOT_TOKEN_ID="+openBaoRootToken,
		"-e", "BAO_DEV_LISTEN_ADDRESS=0.0.0.0:8200",
		openBaoImage)
	require.NoError(t, err, out)
	t.Cleanup(func() {
		_, _ = sh(context.Background(), "", "docker", "rm", "-f", "-v", name)
		_, _ = sh(context.Background(), "", "docker", "image", "rm", openBaoImage)
	})
	b := &baoAPI{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	require.True(t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		res, err := http.Get(b.base + "/v1/sys/health")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == http.StatusOK
	}), "openbao did not become ready")
	return b
}

// requireNoValues fails when s holds any of the values or the encodings a
// platform could store them in.
func requireNoValues(t *testing.T, what, s string, values ...string) {
	t.Helper()
	for _, v := range values {
		require.NotContains(t, s, v, "%s leaks the secret", what)
		require.NotContains(t, s, url.QueryEscape(v), "%s leaks the URL-encoded secret", what)
	}
}

// TestOpenBaoSecretReachesContainerAndIsNeverStored runs a real OpenBao: a
// variable points at one of its secrets, the deploy fetches it at send time
// and the container gets it, the log shows the mask, and no persisted row
// (not the release, not the command, not any table, not the API or MCP)
// ever holds the value. A new value in OpenBao reaches the next deploy, and a
// secret that disappears fails the deploy naming the reference while the old
// container keeps running.
func TestOpenBaoSecretReachesContainerAndIsNeverStored(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	bao := startOpenBao(t)
	s := SharedStack(t)

	// Contains characters that URL encoding changes, so both forms are checked.
	const v1, v2 = "openbao-canary/4f1a9c+one", "openbao-canary/8b2d7e+two"
	bao.put("apps/shop/db", map[string]any{"password": v1, "user": "shop"})

	// The integration: the credential is write-only.
	name := fmt.Sprintf("bao%d", time.Now().UnixNano()%1e6)
	r := s.Do("POST", "/secret-managers", map[string]any{"name": name, "kind": "vault", "address": bao.base, "secret": openBaoRootToken})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	integID := r.JSON().Str("id")
	require.NotContains(t, string(r.Raw), openBaoRootToken)
	t.Cleanup(func() { _ = s.Do("DELETE", "/secret-managers/"+integID, nil) })
	r = s.Do("POST", "/secret-managers/"+integID+"/test", map[string]any{"path": "apps/shop/db", "key": "password"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("ok"), string(r.Raw))
	requireNoValues(t, "test result", string(r.Raw), v1)

	ref := "${{vault:" + name + "/apps/shop/db#password}}"
	svc := s.NewService(SvcSpec{
		Name: "vaulted", Image: app.AppImage,
		// The test is about the environment, not about health probing: a "none"
		// check needs no route from the host to the container's network, which a
		// Mac with its bridge interfaces used up does not have (tests/README.md).
		Health: map[string]any{"type": "none"},
		Vars: map[string]string{
			"VERSION":      "v1",
			"SECRET_ECHO":  "DB_PASSWORD",
			"DB_PASSWORD":  ref,
			"DATABASE_URL": "postgres://shop:${{urlencode(vault:" + name + "/apps/shop/db#password)}}@db/app",
		},
	})
	d := s.DeployOK(svc.ID)

	// The container received the fetched values...
	cs := s.RunningContainers(svc.ID)
	require.Len(t, cs, 1)
	require.Contains(t, cs[0].Env, "DB_PASSWORD="+v1)
	require.Contains(t, cs[0].Env, "DATABASE_URL=postgres://shop:"+url.QueryEscape(v1)+"@db/app")

	// ...the app echoed it, and every log line shows the mask instead.
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		lr := s.Do("GET", "/services/"+svc.ID+"/logs?tail=200", nil)
		return lr.Status == http.StatusOK && strings.Contains(string(lr.Raw), "startup: DB_PASSWORD=")
	}), "the app never printed its startup line")
	lr := s.Do("GET", "/services/"+svc.ID+"/logs?tail=200", nil)
	require.Contains(t, string(lr.Raw), "startup: DB_PASSWORD=••••••")
	requireNoValues(t, "runtime logs", string(lr.Raw), v1)
	requireNoValues(t, "deployment logs", s.DeploymentLogs(d.Str("id")), v1)

	// Nothing the platform serves or stores holds the value.
	for _, path := range []string{
		"/services/" + svc.ID, "/services/" + svc.ID + "/variables", "/services/" + svc.ID + "/pending-changes",
		"/services/" + svc.ID + "/deployments", "/environments/" + s.EnvID + "/services",
		"/deployments/" + d.Str("id"), "/deployments/" + d.Str("id") + "/events", "/deployments/" + d.Str("id") + "/logs",
		"/secret-managers", "/audit-events",
	} {
		rr := s.Do("GET", path, nil)
		require.Equal(t, http.StatusOK, rr.Status, "%s: %s", path, rr.Raw)
		requireNoValues(t, "GET "+path, string(rr.Raw), v1)
	}
	requireNoValues(t, "plan", string(s.Do("POST", "/services/"+svc.ID+"/deployments/plan", map[string]any{}).Raw), v1)
	requireNoValues(t, "database dump", s.dumpAllTables(), v1)
	requireNoValues(t, "decrypted environments", decryptedEnvs(t, s), v1)
	var payload string
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(payload::text || coalesce(result::text,''), E'\n'), '') FROM agent_commands`).Scan(&payload))
	require.Contains(t, payload, "DB_PASSWORD", "the command lists variable keys")
	requireNoValues(t, "agent_commands", payload, v1)
	// The release holds the reference, so a rollback or redeploy can fetch it again.
	require.Contains(t, decryptedEnvs(t, s), ref)

	tok := s.MCPToken([]string{"projects:read", "servers:read", "logs:read"}, nil)
	cs2 := mcpSession(t, s.URL(), tok)
	for tool, args := range map[string]map[string]any{
		"service_get": {"serviceId": svc.ID}, "tail_logs": {"service": svc.ID},
		"deployment_get": {"deploymentId": d.Str("id")}, "review_changes": {"service": svc.ID},
	} {
		res, err := cs2.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err, tool)
		raw, _ := json.Marshal(res)
		requireNoValues(t, "mcp "+tool, string(raw), v1)
	}

	// A new value in OpenBao reaches the next deploy: it was never stored.
	bao.put("apps/shop/db", map[string]any{"password": v2, "user": "shop"})
	s.DeployOK(svc.ID)
	cs = s.RunningContainers(svc.ID)
	require.Len(t, cs, 1)
	require.Contains(t, cs[0].Env, "DB_PASSWORD="+v2)
	requireNoValues(t, "database dump after rotation", s.dumpAllTables(), v1, v2)
	requireNoValues(t, "decrypted environments after rotation", decryptedEnvs(t, s), v1, v2)
	// The first value is still masked in logs of the container that printed it.
	requireNoValues(t, "runtime logs after rotation", string(s.Do("GET", "/services/"+svc.ID+"/logs?tail=200", nil).Raw), v1, v2)

	// A secret that is gone fails the deploy and names the reference; the running container is untouched.
	code, raw := bao.do("DELETE", "/secret/metadata/apps/shop/db", nil)
	require.Equal(t, http.StatusNoContent, code, string(raw))
	before := s.RunningContainers(svc.ID)
	require.Len(t, before, 1)
	failed := s.WaitDeployment(s.Deploy(svc.ID), 2*time.Minute)
	require.Equal(t, "failed", failed.Str("status"), "%v", failed)
	msg := failed.Str("statusMessage")
	require.Contains(t, msg, "Not delivered")
	require.Contains(t, msg, ref, "the failure names the reference")
	require.Contains(t, msg, "no secret at apps/shop/db")
	require.Contains(t, msg, "No changes were made")
	after := s.RunningContainers(svc.ID)
	require.Len(t, after, 1)
	require.Equal(t, before[0].ID, after[0].ID, "the running release keeps serving")
}

// decryptedEnvs returns every release environment and variable value of the
// database, decrypted: what "stored" means for a secret that must not be there.
func decryptedEnvs(t *testing.T, s *Stack) string {
	t.Helper()
	box, err := secretbox.New(s.App.Config.SecretKey)
	require.NoError(t, err)
	var sb strings.Builder
	for _, col := range []secretbox.Column{secretbox.ReleasesEnv, secretbox.ServiceVariablesValue, secretbox.SharedVariablesValue} {
		q := fmt.Sprintf(`SELECT %s, %s FROM %s`, col.RowKey, col.Column, col.Table)
		rows, err := s.Pool.Query(context.Background(), q)
		require.NoError(t, err, q)
		for rows.Next() {
			var rowID string
			var enc []byte
			require.NoError(t, rows.Scan(&rowID, &enc))
			if len(enc) == 0 {
				continue
			}
			plain, err := box.Open(col, rowID, enc)
			require.NoError(t, err)
			sb.Write(plain)
			sb.WriteByte('\n')
		}
		rows.Close()
	}
	return sb.String()
}
