package proxyserver

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type serverIDInput struct {
	ID uuid.UUID `path:"serverId"`
}

type proxyOutput struct{ Body ServerProxy }

type certificateOutput struct{ Body ServerCertificate }

type certificatesOutput struct {
	Body struct {
		Items []ServerCertificate `json:"items"`
	}
}

type uploadCertificateInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Name    string `json:"name" minLength:"1" maxLength:"100" doc:"Unique per server"`
		CertPEM string `json:"certPem" minLength:"1" maxLength:"65536" doc:"ServerCertificate chain in PEM, leaf first"`
		KeyPEM  string `json:"keyPem" minLength:"1" maxLength:"65536" doc:"Unencrypted private key in PEM. Write-only: sealed at rest, never returned."`
	}
}

type certificateIDInput struct {
	ID uuid.UUID `path:"certificateId"`
}

type updateSettingsInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		UpstreamPolicy *struct {
			AllowLoopback    *bool `json:"allowLoopback,omitempty" doc:"Administrators only"`
			AllowLinkLocal   *bool `json:"allowLinkLocal,omitempty" doc:"Administrators only"`
			AllowHostGateway *bool `json:"allowHostGateway,omitempty"`
		} `json:"upstreamPolicy,omitempty"`
		Tailnet *struct {
			Mode     *string `json:"mode,omitempty" enum:"off,managed,host"`
			Hostname *string `json:"hostname,omitempty" maxLength:"63" doc:"Machine name of the managed node (default nebula-<server name>)"`
			MagicDNS *bool   `json:"magicDns,omitempty"`
		} `json:"tailnet,omitempty"`
		Ports *struct {
			HTTP  *int `json:"http,omitempty" minimum:"0" maximum:"65535" doc:"HTTP host port of the proxy; 0 resets it to the agent's own port (instance administrator)"`
			HTTPS *int `json:"https,omitempty" minimum:"0" maximum:"65535" doc:"HTTPS host port of the proxy (tcp and udp); 0 resets it to the agent's own port (instance administrator)"`
		} `json:"ports,omitempty"`
	}
}

type planInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Reprovision bool `json:"reprovision,omitempty" doc:"Delete the managed node and register it again with a new key"`
		Ports       *struct {
			HTTP  *int `json:"http,omitempty" minimum:"0" maximum:"65535" doc:"Plan with this HTTP host port (0 = the agent's own)"`
			HTTPS *int `json:"https,omitempty" minimum:"0" maximum:"65535" doc:"Plan with this HTTPS host port (0 = the agent's own)"`
		} `json:"ports,omitempty" doc:"Preview a proxy ports setting without staging it"`
	}
}

type planOutput struct{ Body ProxyPlan }

type deployInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Reprovision bool `json:"reprovision,omitempty" doc:"Delete the managed node and register it again with a new key"`
	}
}

type deployOutput struct{ Body ProxyDeployResult }

type routesOutput struct {
	Body struct {
		Items []ProxyRoute `json:"items"`
	}
}

// Register attaches the certificate and server-proxy endpoints.
func Register(h huma.API, svc *Service) {
	certTags := []string{"certificates"}
	tags := []string{"proxy"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "uploadCertificate", Method: http.MethodPost, Path: "/servers/{serverId}/certificates", Tags: certTags,
		Summary: "Upload a custom TLS certificate (staged until the proxy is deployed)", DefaultStatus: http.StatusCreated,
		Description: "Needs `proxy:write`. The leaf is the first certificate of the chain; encrypted keys, mismatching keys, expired and " +
			"not-yet-valid certificates and certificates without a SAN are rejected. The key is sealed at rest and never returned.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeProxyWrite), func(ctx context.Context, in *uploadCertificateInput) (*certificateOutput, error) {
		c, err := svc.UploadCertificate(ctx, in.ID, UploadCertificateInput{Name: in.Body.Name, CertPEM: in.Body.CertPEM, KeyPEM: in.Body.KeyPEM})
		if err != nil {
			return nil, err
		}
		return &certificateOutput{Body: c}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listCertificates", Method: http.MethodGet, Path: "/servers/{serverId}/certificates", Tags: certTags,
		Summary: "List a server's custom certificates", Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*certificatesOutput, error) {
		items, err := svc.ListCertificates(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &certificatesOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteCertificate", Method: http.MethodDelete, Path: "/certificates/{certificateId}", Tags: certTags,
		Summary: "Delete a certificate (staged removal when it is installed)", DefaultStatus: http.StatusNoContent,
		Description: "Refused with 409 `certificate_in_use` while a domain pins it.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *certificateIDInput) (*struct{}, error) {
		return nil, svc.DeleteCertificate(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerProxy", Method: http.MethodGet, Path: "/servers/{serverId}/proxy", Tags: tags,
		Summary: "Server-level proxy state: certificates, upstream policy, TCP/UDP ports and tailnet", Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*proxyOutput, error) {
		v, err := svc.GetProxy(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &proxyOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServerProxySettings", Method: http.MethodPatch, Path: "/servers/{serverId}/proxy/settings", Tags: tags,
		Summary: "Stage upstream policy and tailnet settings",
		Description: "Needs `proxy:write`; the loopback and link-local toggles need `admin`; the proxy `ports` can only be changed by the instance administrator " +
			"(403 `instance_admin_required`; 422 `invalid_proxy_ports`, `proxy_port_clash`, `agent_too_old`). Changes are staged until `POST /servers/{id}/proxy/deploy`, which recreates the proxy container when a port changes. " +
			"A managed tailnet needs the Tailscale integration (422 `tailnet_not_configured`); host mode needs Tailscale detected on the host (422 `host_tailscale_not_detected`).",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *updateSettingsInput) (*proxyOutput, error) {
		si := SettingsInput{}
		if p := in.Body.UpstreamPolicy; p != nil {
			si.AllowLoopback, si.AllowLinkLocal, si.AllowHostGateway = p.AllowLoopback, p.AllowLinkLocal, p.AllowHostGateway
		}
		if t := in.Body.Tailnet; t != nil {
			si.TailnetMode, si.TailnetHostname, si.TailnetMagicDNS = t.Mode, t.Hostname, t.MagicDNS
		}
		if pp := in.Body.Ports; pp != nil {
			si.HTTPPort, si.HTTPSPort = pp.HTTP, pp.HTTPS
		}
		v, err := svc.UpdateSettings(ctx, in.ID, si)
		if err != nil {
			return nil, err
		}
		return &proxyOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planServerProxy", Method: http.MethodPost, Path: "/servers/{serverId}/proxy/plan", Tags: tags,
		Summary: "Preview what deploying the proxy would change",
		Description: "Lists the staged differences, whether the proxy container is recreated and warnings (a tailnet node is created, replaced or deleted, a port another process holds, HTTP-01 certificates that cannot validate off port 80). " +
			"`ports` plans the deploy as if that proxy ports setting were staged too, without staging it (0 = the agent's own port).",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *planInput) (*planOutput, error) {
		var preview *PortsInput
		if pp := in.Body.Ports; pp != nil {
			preview = &PortsInput{HTTP: pp.HTTP, HTTPS: pp.HTTPS}
		}
		p, err := svc.PlanDeploy(ctx, in.ID, in.Body.Reprovision, preview)
		if err != nil {
			return nil, err
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "deployServerProxy", Method: http.MethodPost, Path: "/servers/{serverId}/proxy/deploy", Tags: tags,
		Summary: "Deploy the server-level proxy configuration", DefaultStatus: http.StatusAccepted,
		Description: "Sends a `proxy_configure` command with the desired certificates, upstream policy and tailnet. The result updates the applied state asynchronously.",
		Errors:      append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeProxyWrite), func(ctx context.Context, in *deployInput) (*deployOutput, error) {
		r, err := svc.Deploy(ctx, in.ID, in.Body.Reprovision)
		if err != nil {
			return nil, err
		}
		return &deployOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "reprovisionServerTailnet", Method: http.MethodPost, Path: "/servers/{serverId}/proxy/tailnet/reprovision", Tags: tags,
		Summary: "Re-provision the managed tailnet node", DefaultStatus: http.StatusAccepted,
		Description: "Deletes the node's state, registers it again with a fresh single-use key and removes the old device from the tailnet. Use it when the device was removed in the Tailscale admin console.",
		Errors:      append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeProxyWrite), func(ctx context.Context, in *serverIDInput) (*deployOutput, error) {
		r, err := svc.Deploy(ctx, in.ID, true)
		if err != nil {
			return nil, err
		}
		return &deployOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServerProxyRoutes", Method: http.MethodGet, Path: "/servers/{serverId}/proxy/routes", Tags: tags,
		Summary:     "The effective route table of the server's proxy (read-only)",
		Description: "Hostnames, upstreams and policies as the proxy receives them. Basic-auth users and hashes are never included.",
		Errors:      errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*routesOutput, error) {
		items, err := svc.Routes(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &routesOutput{}
		out.Body.Items = items
		return out, nil
	})
}
