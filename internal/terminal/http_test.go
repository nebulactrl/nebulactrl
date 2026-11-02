package terminal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// jsonAPI serves the huma endpoints of the rig's service as whoever r.who says.
func (r *rig) jsonAPI() func(method, path string, body any) (int, map[string]any) {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p := r.who.Load(); p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(router, cfg), r.svc)
	return func(method, path string, body any) (int, map[string]any) {
		r.t.Helper()
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(r.t, err)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var out map[string]any
		if rec.Body.Len() > 0 && rec.Body.Bytes()[0] == '{' {
			require.NoError(r.t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		}
		return rec.Code, out
	}
}

func TestTerminalJSONEndpoints(t *testing.T) {
	r := newRig(t, true)
	do := r.jsonAPI()
	svc := "/services/" + r.target.ServiceID.String()

	code, body := do(http.MethodGet, svc+"/terminal", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, body["available"])
	require.Equal(t, true, body["production"])
	require.Len(t, body["replicas"], 2)
	require.Equal(t, "hetzner-01", body["serverName"])

	r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: fs.id, Stage: agentprotocol.FSDone, Path: req.Path,
			Entries: []agentprotocol.FSEntry{{Name: "main.go", Type: agentprotocol.FSTypeFile, Size: 42, Mode: "0644"}},
		})
	})
	code, body = do(http.MethodGet, svc+"/files?path=/app&replica=0", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "/app", body["path"])
	require.Len(t, body["entries"], 1)
	code, body = do(http.MethodGet, svc+"/files?replica=1000", nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	code, body = do(http.MethodGet, svc+"/files?path=relative", nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)

	// Settings: anyone who can read the project sees them, only admins change them.
	pid := r.target.ProjectID.String()
	code, body = do(http.MethodGet, "/projects/"+pid+"/terminal-settings", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, body["allowProductionFileWrites"])
	code, body = do(http.MethodPut, "/projects/"+pid+"/terminal-settings", map[string]any{"allowProductionFileWrites": true})
	require.Equal(t, http.StatusForbidden, code, "a developer cannot switch production writes on: %v", body)
	r.who.Store(principal(auth.RoleAdmin, policy.AllScopes))
	code, body = do(http.MethodPut, "/projects/"+pid+"/terminal-settings", map[string]any{"allowProductionFileWrites": true})
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, body["allowProductionFileWrites"])
	require.True(t, r.store.prodWrites)

	// A viewer gets nothing of the terminal; signed out is 401.
	r.who.Store(principal(auth.RoleViewer, []string{policy.ScopeProjectsRead}))
	code, _ = do(http.MethodGet, svc+"/terminal", nil)
	require.Equal(t, http.StatusForbidden, code)
	r.who.Store(nil)
	code, _ = do(http.MethodGet, svc+"/terminal", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = do(http.MethodGet, "/projects/"+uuid.NewString()+"/terminal-settings", nil)
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestRawRoutesRefuseBadParameters(t *testing.T) {
	r := newRig(t, false)
	base := r.srv.URL + "/services/" + r.target.ServiceID.String()

	code, body, _ := get(t, base+"/files/content?path=/etc/hosts&replica=abc")
	require.Equal(t, http.StatusUnprocessableEntity, code, string(body))
	require.Contains(t, string(body), "invalid_replica")
	code, body, _ = get(t, base+"/files/content?path=/etc/hosts&replica=1000")
	require.Equal(t, http.StatusUnprocessableEntity, code, string(body))
	code, body, _ = get(t, r.srv.URL+"/services/not-a-uuid/files/content?path=/x")
	require.Equal(t, http.StatusNotFound, code, string(body))

	// An upload needs a known length (a chunked body has none) and stays within the limit.
	req, err := http.NewRequest(http.MethodPost, base+"/files/upload?path=/tmp&name=a.txt", io.NopCloser(strings.NewReader("data")))
	require.NoError(t, err)
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusLengthRequired, resp.StatusCode)

	conn, err := net.Dial("tcp", strings.TrimPrefix(r.srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "POST /services/%s/files/upload?path=/tmp&name=a.txt HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", r.target.ServiceID, agentprotocol.MaxUploadBytes+1)
	require.NoError(t, err)
	status, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, status, "413", "an upload over the limit is refused before a byte of it is read")
}

func TestClampSizeKeepsTheTerminalUsable(t *testing.T) {
	require.Equal(t, 80, clampSize(0, 80), "no size asked: the default")
	require.Equal(t, 24, clampSize(-5, 24))
	require.Equal(t, 100, clampSize(100, 80))
	require.Equal(t, agentprotocol.MaxTermSize, clampSize(1<<20, 80), "an absurd size is capped")
	require.Equal(t, agentprotocol.MinTermSize, clampSize(1, 80), "and a tiny one raised")
}
