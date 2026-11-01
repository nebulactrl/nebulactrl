package tailscale

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Errors of the Headscale policy API.
var (
	// ErrPolicyNotFound is returned by GetPolicy while Headscale (policy.mode:
	// database) holds no policy yet: Headscale allows everything until the first PUT.
	ErrPolicyNotFound = errors.New("headscale: no policy stored")
	// ErrPolicyFileMode is returned by SetPolicy when Headscale runs with
	// policy.mode: file, which refuses updates over the API. The mesh then falls
	// back to the advisory mode.
	ErrPolicyFileMode = errors.New("headscale: policy.mode is file, updates over the API are disabled")
)

// Policy is a stored access policy.
type Policy struct {
	// Raw is the policy document (HuJSON text).
	Raw string
	// Version is Headscale's updatedAt, or Tailscale's ETag.
	Version string
}

type headscalePolicyBody struct {
	Policy    string `json:"policy"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// GetPolicy reads the stored policy (ErrPolicyNotFound before the first PUT).
func (c *HeadscaleClient) GetPolicy(ctx context.Context) (Policy, error) {
	var out headscalePolicyBody
	err := c.do(ctx, http.MethodGet, "/api/v1/policy", nil, nil, &out)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && (ae.Status == http.StatusNotFound || strings.Contains(strings.ToLower(ae.Message), "policy not found")) {
			return Policy{}, ErrPolicyNotFound
		}
		return Policy{}, err
	}
	return Policy{Raw: out.Policy, Version: out.UpdatedAt}, nil
}

// CheckPolicy asks Headscale to validate a policy before it is stored. Servers
// that do not offer the endpoint (404, 405, 501) are treated as "no way to
// check": SetPolicy validates anyway and refuses an invalid document.
func (c *HeadscaleClient) CheckPolicy(ctx context.Context, doc string) error {
	err := c.do(ctx, http.MethodPost, "/api/v1/policy/check", nil, headscalePolicyBody{Policy: doc}, nil)
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
			return nil
		}
	}
	return err
}

// SetPolicy stores the policy (PUT). It returns ErrPolicyFileMode when
// Headscale does not accept policy updates over the API.
func (c *HeadscaleClient) SetPolicy(ctx context.Context, doc string) (Policy, error) {
	var out headscalePolicyBody
	err := c.do(ctx, http.MethodPut, "/api/v1/policy", nil, headscalePolicyBody{Policy: doc}, &out)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			m := strings.ToLower(ae.Message)
			if strings.Contains(m, "update is disabled") || strings.Contains(m, "policy mode") || strings.Contains(m, "file mode") {
				return Policy{}, ErrPolicyFileMode
			}
		}
		return Policy{}, err
	}
	return Policy{Raw: out.Policy, Version: out.UpdatedAt}, nil
}
