package auth

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Codes of a failed issuer test. They are stable: the web app words its advice
// by code.
const (
	IssuerInvalid      = "invalid_issuer"
	IssuerHostNotFound = "host_not_found"
	IssuerUnreachable  = "unreachable"
	IssuerTimeout      = "timeout"
	IssuerTLSInvalid   = "tls_invalid"
	IssuerNotFound     = "discovery_not_found"
	IssuerBadResponse  = "discovery_invalid"
	IssuerMismatch     = "issuer_mismatch"
	IssuerIncomplete   = "discovery_incomplete"
)

const (
	discoveryPath       = "/.well-known/openid-configuration"
	discoveryBodyLimit  = 1 << 20
	issuerMessageDetail = 200
)

// IssuerCheck is what OpenID Connect discovery found at an issuer.
type IssuerCheck struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSURI               string
}

// IssuerProblem says why an issuer cannot be used, in terms an administrator
// can act on. Code is one of the Issuer* constants.
type IssuerProblem struct {
	Code    string
	Message string
}

func (p *IssuerProblem) Error() string { return p.Code + ": " + p.Message }

func problem(code, format string, args ...any) *IssuerProblem {
	return &IssuerProblem{Code: code, Message: fmt.Sprintf(format, args...)}
}

// TestIssuer reads the OpenID configuration of issuerURL the way saving an OIDC
// provider does, without saving anything. A failure is an *IssuerProblem.
func (s *Service) TestIssuer(ctx context.Context, issuerURL string) (IssuerCheck, error) {
	return DiscoverIssuer(ctx, s.methods.client(), issuerURL)
}

// DiscoverIssuer fetches issuerURL + /.well-known/openid-configuration and
// checks it the way the login flow later relies on it: the document names the
// same issuer byte for byte, and carries the authorization and token endpoints
// and the key set.
func DiscoverIssuer(ctx context.Context, client *http.Client, issuerURL string) (IssuerCheck, error) {
	issuer, err := validateIssuerURL(KindOIDC, issuerURL)
	if err != nil {
		return IssuerCheck{}, problem(IssuerInvalid, "%s", err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+discoveryPath, nil)
	if err != nil {
		return IssuerCheck{}, problem(IssuerInvalid, "the issuer URL is not valid")
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return IssuerCheck{}, transportProblem(ctx, req.URL.Host, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return IssuerCheck{}, problem(IssuerNotFound,
			"%s answered %d for %s. The issuer must be the provider's base address, the part before %s.",
			req.URL.Host, res.StatusCode, discoveryPath, discoveryPath)
	}
	var doc struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, discoveryBodyLimit)).Decode(&doc); err != nil {
		return IssuerCheck{}, problem(IssuerBadResponse, "%s did not return a JSON OpenID configuration.", req.URL.Host)
	}
	if doc.Issuer != issuer {
		return IssuerCheck{}, problem(IssuerMismatch,
			"The provider says its issuer is %q, but you entered %q. They must match exactly, including any trailing slash.",
			clip(doc.Issuer), clip(issuer))
	}
	var missing []string
	if doc.AuthorizationEndpoint == "" {
		missing = append(missing, "authorization_endpoint")
	}
	if doc.TokenEndpoint == "" {
		missing = append(missing, "token_endpoint")
	}
	if doc.JWKSURI == "" {
		missing = append(missing, "jwks_uri")
	}
	if len(missing) > 0 {
		return IssuerCheck{}, problem(IssuerIncomplete, "The OpenID configuration has no %s.", strings.Join(missing, ", "))
	}
	return IssuerCheck{
		Issuer: doc.Issuer, AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint: doc.TokenEndpoint, JWKSURI: doc.JWKSURI,
	}, nil
}

func transportProblem(ctx context.Context, host string, err error) *IssuerProblem {
	var dns *net.DNSError
	var unknownAuthority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return problem(IssuerHostNotFound, "The host name %s does not resolve. Check the spelling of the domain.", host)
	case errors.As(err, &unknownAuthority), errors.As(err, &invalid), errors.As(err, &hostname):
		return problem(IssuerTLSInvalid, "%s presented a TLS certificate this server does not trust (%s).", host, clip(rootCause(err)))
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err), ctx.Err() != nil:
		return problem(IssuerTimeout, "%s did not answer in time.", host)
	default:
		return problem(IssuerUnreachable, "Could not connect to %s (%s).", host, clip(rootCause(err)))
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func rootCause(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

func clip(s string) string {
	if len(s) > issuerMessageDetail {
		return s[:issuerMessageDetail] + "…"
	}
	return s
}
