package forward

import (
	"context"
	"net/http"
	"net/http/httputil"
	"strconv"
)

// MaxLokiPush bounds one push request.
const MaxLokiPush = 8 << 20

type lokiHandler struct {
	f     *Forwarder
	proxy *httputil.ReverseProxy
}

func newLoki(f *Forwarder, raw string) (http.Handler, error) {
	target, err := parseBase(raw)
	if err != nil {
		return nil, err
	}
	h := &lokiHandler{f: f}
	h.proxy = newProxy(target, f.log, func(pr *httputil.ProxyRequest) {
		// The tenant is the team of the stream, whatever the client put in the
		// header. Rewrite already dropped X-Forwarded-*; the tenant header and any
		// client credential are dropped here, and the tenant is set from the peer.
		pr.Out.Header.Del("X-Scope-OrgID")
		pr.Out.Header.Del("Authorization")
		pr.Out.Header.Set("X-Scope-OrgID", tenantOf(pr.In.Context()))
	}, nil, func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "the built-in log store is not reachable", http.StatusBadGateway)
	})
	return h, nil
}

func (h *lokiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pe := peerOf(ctx)
	if r.Method != http.MethodPost || r.URL.Path != "/loki/api/v1/push" {
		h.f.log.Warn("loki request refused", "team_id", pe.team.String(), "server_id", pe.server.String(), "method", r.Method, "path", r.URL.Path)
		http.Error(w, "only log pushes are served", http.StatusForbidden)
		return
	}
	if r.ContentLength > MaxLokiPush {
		http.Error(w, "push too large (limit "+strconv.Itoa(MaxLokiPush>>20)+" MiB)", http.StatusRequestEntityTooLarge)
		return
	}
	ts, err := h.f.teamSlices(ctx, pe.team)
	if err != nil || ts.Loki == nil || (ts.Loki.State != "ready" && ts.Loki.State != "full") || ts.Loki.Ref == "" {
		http.Error(w, "the built-in log store is not available", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxLokiPush)
	// The tenant travels in the request context to the proxy's Rewrite.
	h.proxy.ServeHTTP(w, r.WithContext(withTenant(ctx, ts.Loki.Ref)))
}

type tenantKey struct{}

func withTenant(ctx context.Context, t string) context.Context {
	return context.WithValue(ctx, tenantKey{}, t)
}

func tenantOf(ctx context.Context) string { s, _ := ctx.Value(tenantKey{}).(string); return s }
