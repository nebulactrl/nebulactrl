package adoption

import (
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ruleInput is the input of a new domain with the rule the proxy served. Only
// what differs from the defaults of a new domain is set, so a plain host name
// needs no proxy permission, as when it is added by hand. The route's Generated
// flag is never taken from the host.
func ruleInput(r agentprotocol.DomainRule) domains.RuleInput {
	var in domains.RuleInput
	if r.PathPrefix != "" {
		in.PathPrefix = &r.PathPrefix
	}
	if r.StripPrefix {
		in.StripPrefix = &r.StripPrefix
	}
	if r.AddPrefix != "" {
		in.AddPrefix = &r.AddPrefix
	}
	if r.Kind == agentprotocol.DomainRedirect {
		kind := agentprotocol.DomainRedirect
		in.Kind, in.RedirectTo = &kind, &r.RedirectTo
		if r.RedirectCode != 0 {
			in.RedirectCode = &r.RedirectCode
		}
		in.PreservePath = &r.PreservePath
	}
	if r.TLSMode != "" && r.TLSMode != agentprotocol.TLSAuto {
		in.TLSMode = &r.TLSMode
	}
	if !r.Options.IsZero() {
		in.Options = &r.Options
	}
	if r.DNSResolver != "" {
		in.DNSResolver = &r.DNSResolver
	}
	return in
}

// errText is the message a person reads for err.
func errText(err error) string {
	if e, ok := apierr.As(err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}
