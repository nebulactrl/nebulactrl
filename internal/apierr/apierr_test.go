package apierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

func TestConstructorsCarryStatusAndStableCode(t *testing.T) {
	cases := []struct {
		name   string
		err    *Error
		status int
		code   string
		msg    string
	}{
		{"not found", NotFound("service"), 404, "not_found", "service not found"},
		{"not found without a name", NotFound(""), 404, "not_found", "resource not found"},
		{"forbidden", Forbidden(), 403, "forbidden", "you do not have permission to perform this action"},
		{"unauthorized", Unauthorized(), 401, "unauthorized", "authentication required"},
		{"conflict", Conflict("name_taken", "taken"), 409, "name_taken", "taken"},
		{"unprocessable", Unprocessable("bad_port", "port"), 422, "bad_port", "port"},
		{"precondition failed", PreconditionFailed("stale", "reload"), 412, "stale", "reload"},
		{"bad request", BadRequest("bad_json", "nope"), 400, "bad_json", "nope"},
		{"too many requests", TooManyRequests("slow down"), 429, "rate_limited", "slow down"},
		{"internal", Internal(), 500, "internal_error", "unexpected error occurred"},
		{"integration in use", IntegrationInUse("used by the mesh"), 409, CodeIntegrationInUse, "used by the mesh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Status != c.status || c.err.GetStatus() != c.status {
				t.Errorf("status = %d / %d, want %d", c.err.Status, c.err.GetStatus(), c.status)
			}
			if c.err.Code != c.code {
				t.Errorf("code = %q, want %q", c.err.Code, c.code)
			}
			if c.err.Message != c.msg || c.err.Error() != c.msg {
				t.Errorf("message = %q / %q, want %q", c.err.Message, c.err.Error(), c.msg)
			}
			if c.err.Title != http.StatusText(c.status) {
				t.Errorf("title = %q, want %q", c.err.Title, http.StatusText(c.status))
			}
			if c.err.Type != "about:blank" {
				t.Errorf("type = %q, want about:blank", c.err.Type)
			}
		})
	}
}

func TestErrorTextFallsBackToTheCodeWithoutAMessage(t *testing.T) {
	if got := New(409, "busy", "").Error(); got != "busy" {
		t.Errorf("Error() = %q, want the code", got)
	}
}

func TestWithDetailsAttachesAndReturnsTheSameError(t *testing.T) {
	e := Conflict("x", "y")
	got := e.WithDetails(map[string]any{"count": 2})
	if got != e {
		t.Fatal("WithDetails must return the receiver")
	}
	if e.Details["count"] != 2 {
		t.Errorf("details = %v", e.Details)
	}
}

func TestContentTypeUsesProblemMediaTypes(t *testing.T) {
	e := Internal()
	for in, want := range map[string]string{
		"application/json": "application/problem+json",
		"application/cbor": "application/problem+cbor",
		"text/plain":       "text/plain",
	} {
		if got := e.ContentType(in); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAsAndIsNotFoundSeeThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("loading: %w", NotFound("project"))
	if e, ok := As(wrapped); !ok || e.Code != "not_found" {
		t.Errorf("As(wrapped) = %v, %v", e, ok)
	}
	if !IsNotFound(wrapped) {
		t.Error("a wrapped 404 must be IsNotFound")
	}
	if IsNotFound(Forbidden()) {
		t.Error("a 403 is not IsNotFound")
	}
	if IsNotFound(errors.New("plain")) {
		t.Error("a plain error is not IsNotFound")
	}
	if e, ok := As(errors.New("plain")); ok || e != nil {
		t.Errorf("As(plain) = %v, %v; want nil,false", e, ok)
	}
	if IsNotFound(nil) {
		t.Error("nil is not IsNotFound")
	}
}

func TestCodeForStatus(t *testing.T) {
	for status, want := range map[int]string{
		400: "bad_request",
		401: "unauthorized",
		403: "forbidden",
		404: "not_found",
		405: "method_not_allowed",
		406: "not_acceptable",
		409: "conflict",
		412: "precondition_failed",
		428: "precondition_required",
		413: "payload_too_large",
		415: "unsupported_media_type",
		422: "validation_failed",
		429: "rate_limited",
		500: "internal_error",
		503: "internal_error",
		418: "i'm_a_teapot",
		410: "gone",
	} {
		if got := codeForStatus(status); got != want {
			t.Errorf("codeForStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestFromHumaKeepsAHandlersAPIErrorVerbatim(t *testing.T) {
	orig := Conflict("name_taken", "taken")
	got := FromHuma(500, "ignored", errors.New("noise"), fmt.Errorf("wrap: %w", orig))
	if got != huma.StatusError(orig) {
		t.Fatalf("FromHuma replaced the handler's error: %#v", got)
	}
}

func TestFromHumaCollectsFieldErrorsAndPlainCauses(t *testing.T) {
	detail := &huma.ErrorDetail{Message: "expected number", Location: "body.port", Value: "x"}
	got := FromHuma(422, "validation failed", nil, detail, errors.New("body is not JSON")).(*Error)
	if got.Status != 422 || got.Code != "validation_failed" || got.Message != "validation failed" {
		t.Fatalf("got %+v", got)
	}
	if len(got.Errors) != 2 {
		t.Fatalf("errors = %+v, want the detail and the plain cause", got.Errors)
	}
	if got.Errors[0] != detail {
		t.Errorf("first error = %+v, want the huma detail", got.Errors[0])
	}
	if got.Errors[1].Message != "body is not JSON" {
		t.Errorf("second error = %+v", got.Errors[1])
	}
}

func TestFromHumaServerErrorsNeverLeakTheCause(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got := FromHuma(500, "pq: password authentication failed for user nebula", errors.New("dial tcp 10.0.0.5: refused")).(*Error)
	if got.Status != 500 || got.Code != "internal_error" || got.Message != "unexpected error occurred" {
		t.Fatalf("got %+v", got)
	}
	if len(got.Errors) != 0 {
		t.Errorf("a 5xx must not carry error details: %+v", got.Errors)
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "password") || strings.Contains(string(body), "10.0.0.5") {
		t.Errorf("response body leaks the cause: %s", body)
	}
	if !strings.Contains(buf.String(), "10.0.0.5") {
		t.Errorf("cause must still be logged server-side: %q", buf.String())
	}
}

func TestFromHumaServerErrorWithoutCausesLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got := FromHuma(502, "bad gateway").(*Error)
	if got.Message != "unexpected error occurred" || got.Code != "internal_error" {
		t.Fatalf("got %+v", got)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected log output: %q", buf.String())
	}
}

func TestHumaIsWiredToProduceAPIErrors(t *testing.T) {
	se := huma.NewError(http.StatusNotFound, "no such thing")
	e, ok := se.(*Error)
	if !ok {
		t.Fatalf("huma.NewError returned %T, want *apierr.Error", se)
	}
	if e.Code != "not_found" || e.Message != "no such thing" {
		t.Errorf("got %+v", e)
	}
}

func TestWriteRendersProblemJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, Conflict("name_taken", "taken").WithDetails(map[string]any{"name": "web"}))

	if rec.Code != 409 {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "name_taken" || body["detail"] != "taken" || body["status"] != float64(409) ||
		body["title"] != "Conflict" || body["type"] != "about:blank" {
		t.Errorf("body = %v", body)
	}
	if d, _ := body["details"].(map[string]any); d["name"] != "web" {
		t.Errorf("details = %v", body["details"])
	}
}

func TestMeshErrors(t *testing.T) {
	cases := []struct {
		name         string
		err          *Error
		status       int
		code         string
		wantContains string
	}{
		{"not configured", MeshNotConfigured(), 422, CodeMeshNotConfigured, "Integrations"},
		{"agent too old", MeshAgentTooOld("edge-1"), 422, CodeMeshAgentTooOld, "agent on edge-1 is too old"},
		{"no tun", MeshNoTun("edge-2"), 422, CodeMeshNoTun, "edge-2 has no /dev/net/tun"},
		{"coordinator down", MeshCoordinatorDown(), 422, CodeMeshCoordinatorDown, "unreachable"},
		{"paused without reason", MeshPaused(""), 409, CodeMeshPaused, "the mesh is paused. Established"},
		{"paused with reason", MeshPaused("maintenance"), 409, CodeMeshPaused, "the mesh is paused: maintenance. Established"},
		{"server required", MeshServerRequired("edge-3"), 422, CodeMeshServerRequired, "enable the mesh on edge-3 first"},
		{"not exposable", MeshNotExposable("it has no port"), 422, CodeMeshNotExposable, "exposed over the mesh: it has no port"},
		{"confirmation required", MeshConfirmationRequired(3), 409, CodeMeshConfirmationRequired, "acceptMeshChanges=true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Status != c.status || c.err.Code != c.code {
				t.Errorf("got %d %q, want %d %q", c.err.Status, c.err.Code, c.status, c.code)
			}
			if !strings.Contains(c.err.Message, c.wantContains) {
				t.Errorf("message %q lacks %q", c.err.Message, c.wantContains)
			}
		})
	}
	if got := MeshConfirmationRequired(3).Details["devicesAdded"]; got != 3 {
		t.Errorf("devicesAdded = %v, want 3", got)
	}
}
