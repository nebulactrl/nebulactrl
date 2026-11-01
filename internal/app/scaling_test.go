package app_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScalingEndpoint(t *testing.T) {
	h := newHarness(t)
	h.setup()
	svc, _ := h.newService("web")

	// An undeployed stateless service stores the setting; replicas are never staged.
	r := h.do("PUT", "/services/"+svc+"/scaling", "", map[string]any{"replicas": 3}, "Idempotency-Key", "scale-1")
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	sc := r.JSON()["scaling"].(map[string]any)
	require.EqualValues(t, 3, sc["replicas"])
	require.Equal(t, true, sc["eligible"])

	// An idempotent replay returns the identical body.
	again := h.do("PUT", "/services/"+svc+"/scaling", "", map[string]any{"replicas": 3}, "Idempotency-Key", "scale-1")
	require.Equal(t, http.StatusOK, again.Status)
	require.JSONEq(t, string(r.Raw), string(again.Raw))

	r = h.do("PUT", "/services/"+svc+"/scaling", "", map[string]any{"replicas": 11})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	r = h.do("PUT", "/services/"+svc+"/scaling", "", map[string]any{"autoscaling": map[string]any{"enabled": true, "min": 5, "max": 2}})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))

	r = h.do("PUT", "/services/"+svc+"/scaling", "", map[string]any{"autoscaling": map[string]any{"enabled": true, "min": 2, "max": 4, "targetCpuPercent": 60}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	as := r.JSON()["scaling"].(map[string]any)["autoscaling"].(map[string]any)
	require.Equal(t, true, as["enabled"])
	require.EqualValues(t, 60, as["targetCpuPercent"])

	r = h.do("PUT", "/services/"+svc+"/scaling", "", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "autoscaling_enabled", r.JSON()["code"])

	// A managed database is not stateless.
	envID := h.do("GET", "/services/"+svc, "", nil).JSON()["environmentId"].(string)
	r = h.do("POST", "/environments/"+envID+"/databases", "", map[string]any{"name": "pg", "engine": "postgres"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	pg := r.JSON()["id"].(string)
	r = h.do("PUT", "/services/"+pg+"/scaling", "", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "replicas_require_stateless", r.JSON()["code"])
	require.Equal(t, false, h.do("GET", "/services/"+pg, "", nil).JSON()["scaling"].(map[string]any)["eligible"])

	// A read-only token may not scale.
	tok := h.createToken([]string{"projects:read"}, nil)
	r = h.do("PUT", "/services/"+svc+"/scaling", tok, map[string]any{"replicas": 2})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
}
