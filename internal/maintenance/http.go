package maintenance

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agent/pages"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type setMaintenanceInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Enabled bool       `json:"enabled" doc:"true puts the service into maintenance, false takes it out"`
		Title   string     `json:"title,omitempty" maxLength:"80" doc:"Page title; empty shows 'Down for maintenance'. Only used when enabling."`
		Message string     `json:"message,omitempty" maxLength:"600" doc:"Page message (plain text); empty shows the default copy. Only used when enabling."`
		ETA     *time.Time `json:"eta,omitempty" doc:"When the service is expected back (future). Shown on the page and sent as Retry-After. Only used when enabling."`
	}
}

type serviceOutput struct {
	ETag string `header:"ETag"`
	Body services.ServiceView
}

type previewMaintenanceInput struct {
	ID      uuid.UUID `path:"serviceId"`
	Draft   bool      `query:"draft" doc:"Preview the title, message and eta below instead of the stored ones (empty values show the defaults)"`
	Title   string    `query:"title" maxLength:"80"`
	Message string    `query:"message" maxLength:"600"`
	ETA     string    `query:"eta" doc:"RFC 3339 time"`
}

// Register attaches the maintenance endpoints.
func Register(h huma.API, s *Service) {
	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "setServiceMaintenance", Method: http.MethodPut, Path: "/services/{serviceId}/maintenance", Tags: []string{"services"},
		Summary: "Switch a service's maintenance mode",
		Description: "While enabled, the proxy answers every domain of the service with HTTP 503, a `Retry-After` header and the " +
			"maintenance page (the same animated page as the automatic error pages), instead of reaching the service. " +
			"It takes effect immediately, without a deployment, and keeps working through deployments of the service. " +
			"It is an operational setting: never staged and never rolled back. A deployed service's server is sent the new route table; " +
			"an undeployed service applies the setting with its first deployment. Idempotent: repeating the same request changes nothing. " +
			"Switching off keeps the stored text for the next time. tcp/udp route services cannot be put into maintenance (422 `maintenance_unsupported`).",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *setMaintenanceInput) (*serviceOutput, error) {
		v, err := s.Set(ctx, in.ID, SetInput{Enabled: in.Body.Enabled, Title: in.Body.Title, Message: in.Body.Message, ETA: in.Body.ETA})
		if err != nil {
			return nil, err
		}
		return &serviceOutput{ETag: services.ETag(v.Generation), Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "previewServiceMaintenance", Method: http.MethodGet, Path: "/services/{serviceId}/maintenance/preview", Tags: []string{"services"},
		Summary:     "Preview a service's maintenance page",
		Description: "Returns the self-contained HTML page exactly as the proxy serves it (status 200 here; the proxy answers 503). Optional query parameters preview unsaved text.",
		Responses: map[string]*huma.Response{
			"200": {Description: "The maintenance page", Content: map[string]*huma.MediaType{"text/html": {Schema: &huma.Schema{Type: "string"}}}},
		},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *previewMaintenanceInput) (*huma.StreamResponse, error) {
		pin := PreviewInput{}
		if in.Draft {
			pin.Draft, pin.Title, pin.Message = true, in.Title, in.Message
			if in.ETA != "" {
				eta, err := time.Parse(time.RFC3339, in.ETA)
				if err != nil {
					return nil, invalid("eta must be an RFC 3339 time")
				}
				pin.ETA = &eta
			}
		}
		body, err := s.Preview(ctx, in.ID, pin)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", "text/html; charset=utf-8")
			for k, v := range pages.SecurityHeaders() {
				hctx.SetHeader(k, v)
			}
			hctx.SetStatus(http.StatusOK)
			_, _ = hctx.BodyWriter().Write(body)
		}}, nil
	})
}
