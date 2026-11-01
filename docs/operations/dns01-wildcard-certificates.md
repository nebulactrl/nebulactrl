---
title: DNS-01 and wildcard certificates
description: "Issue wildcard certificates through your DNS provider."
section: Servers
order: 50
---

# DNS-01 and wildcard certificates

Let's Encrypt validates a wildcard (`*.example.com`) only through the **DNS-01**
challenge: Traefik creates a TXT record at `_acme-challenge.<domain>` through
your DNS provider's API. NebulaCtrl supports **every DNS provider of lego**, the ACME
client inside Traefik: **19 with a form and credential help** (Cloudflare, Amazon Route 53,
Hetzner, DigitalOcean, Gandi, OVHcloud, Porkbun, Namecheap, Google Cloud DNS, Azure DNS,
deSEC, Linode, Vultr, Scaleway, Bunny, DNSimple, IONOS, GoDaddy and Netcup) and **any other
lego provider** through a generic form (provider code plus name/value variables).

For names in a zone of the Cloudflare account connected to NebulaCtrl none of the setup below is needed: the certificate is obtained through DNS-01 [automatically](#names-in-a-connected-cloudflare-zone-automatic).

## Concepts

| Thing | What it is |
|---|---|
| Credential | The provider's API variables, sealed with the instance secret key. Scope **team** (usable on every server) or **server**. Created by administrators; secret values are write-only. |
| Resolver | One per provider and server (Traefik `certificatesresolvers.nc-dns-<provider>`). It is static proxy configuration with the credential as the proxy container's environment. |
| Wildcard certificate | `*.<domain>` (and the base domain with "Include the base domain") requested through a resolver. Optional: **also cover generated domains** and **serve as the default certificate** (`defaultGeneratedCert`, so unknown hosts get a valid certificate instead of Traefik's self-signed one). |
| Domain resolver | A single domain can name a resolver instead (useful for hosts that are not reachable on port 80). For names in a zone of your connected Cloudflare account the resolver is [automatic](#names-in-a-connected-cloudflare-zone-automatic). |

A resolver needs the server's **ACME email**. The environment is process-wide, so
there is **one credential per provider per server**; for two accounts of one
provider delegate `_acme-challenge` with a CNAME (Traefik documents this).

## Variables (verified against go-acme/lego v5.5.2, vendored by Traefik v3.7.16)

Where the names come from. Traefik v3.7.16's `go.mod` requires `github.com/go-acme/lego/v5 v5.5.2`
and its ACME provider calls `dns.NewDNSChallengeProviderByName` (`pkg/provider/acme/provider.go`).
Lego reads each provider's credentials from the process environment, with the names in
`providers/dns/<code>/<code>.toml` at tag `v5.5.2`; the lego documentation renders that file at
`https://go-acme.github.io/lego/dns/<code>/`. The catalog in
`pkg/agentprotocol/dns_lego_catalog.go` is generated from those files by `tools/dnscatalog`, and
`TestCatalogMatchesPinnedTraefik` fails when `proxy.TraefikImage` moves without regenerating it:

```sh
go run ./tools/dnscatalog -traefik v3.7.16 -o pkg/agentprotocol/dns_lego_catalog.go
```

Providers with a form (`R` = required; `OneOf` = set every variable of one method):

| Provider (code) | Variables | Lego page |
|---|---|---|
| Cloudflare (`cloudflare`) | `CF_DNS_API_TOKEN` R (Zone:Zone:Read + Zone:DNS:Edit), `CF_ZONE_API_TOKEN` | [cloudflare](https://go-acme.github.io/lego/dns/cloudflare/) |
| Amazon Route 53 (`route53`) | `AWS_ACCESS_KEY_ID` R, `AWS_SECRET_ACCESS_KEY` R, `AWS_REGION` R, `AWS_HOSTED_ZONE_ID` | [route53](https://go-acme.github.io/lego/dns/route53/) |
| Hetzner (`hetzner`) | `HETZNER_API_TOKEN` R | [hetzner](https://go-acme.github.io/lego/dns/hetzner/) |
| DigitalOcean (`digitalocean`) | `DO_AUTH_TOKEN` R | [digitalocean](https://go-acme.github.io/lego/dns/digitalocean/) |
| Gandi (`gandiv5`) | `GANDIV5_PERSONAL_ACCESS_TOKEN` R | [gandiv5](https://go-acme.github.io/lego/dns/gandiv5/) |
| OVHcloud (`ovh`) | `OVH_ENDPOINT`; OneOf: `OVH_APPLICATION_KEY` + `OVH_APPLICATION_SECRET` + `OVH_CONSUMER_KEY`, or `OVH_CLIENT_ID` + `OVH_CLIENT_SECRET`, or `OVH_ACCESS_TOKEN` | [ovh](https://go-acme.github.io/lego/dns/ovh/) |
| Porkbun (`porkbun`) | `PORKBUN_API_KEY` R, `PORKBUN_SECRET_API_KEY` R | [porkbun](https://go-acme.github.io/lego/dns/porkbun/) |
| Namecheap (`namecheap`) | `NAMECHEAP_API_USER` R, `NAMECHEAP_API_KEY` R | [namecheap](https://go-acme.github.io/lego/dns/namecheap/) |
| Google Cloud DNS (`gcloud`) | `GCE_SERVICE_ACCOUNT` R (the key JSON, stored on one line), `GCE_PROJECT`, `GCE_ZONE_ID` | [gcloud](https://go-acme.github.io/lego/dns/gcloud/) |
| Azure DNS (`azuredns`) | `AZURE_CLIENT_ID` R, `AZURE_CLIENT_SECRET` R, `AZURE_TENANT_ID` R, `AZURE_SUBSCRIPTION_ID`, `AZURE_RESOURCE_GROUP` | [azuredns](https://go-acme.github.io/lego/dns/azuredns/) |
| deSEC (`desec`) | `DESEC_TOKEN` R | [desec](https://go-acme.github.io/lego/dns/desec/) |
| Linode (`linode`) | `LINODE_TOKEN` R | [linode](https://go-acme.github.io/lego/dns/linode/) |
| Vultr (`vultr`) | `VULTR_API_KEY` R | [vultr](https://go-acme.github.io/lego/dns/vultr/) |
| Scaleway (`scaleway`) | `SCW_SECRET_KEY` R, `SCW_ACCESS_KEY`, `SCW_PROJECT_ID` | [scaleway](https://go-acme.github.io/lego/dns/scaleway/) |
| Bunny (`bunny`) | `BUNNY_API_KEY` R | [bunny](https://go-acme.github.io/lego/dns/bunny/) |
| DNSimple (`dnsimple`) | `DNSIMPLE_OAUTH_TOKEN` R (an account token; a user token is rejected) | [dnsimple](https://go-acme.github.io/lego/dns/dnsimple/) |
| IONOS (`ionos`) | `IONOS_API_KEY` R (`<prefix>.<secret>`) | [ionos](https://go-acme.github.io/lego/dns/ionos/) |
| GoDaddy (`godaddy`) | `GODADDY_API_KEY` R, `GODADDY_API_SECRET` R | [godaddy](https://go-acme.github.io/lego/dns/godaddy/) |
| Netcup (`netcup`) | `NETCUP_CUSTOMER_NUMBER` R, `NETCUP_API_KEY` R, `NETCUP_API_PASSWORD` R | [netcup](https://go-acme.github.io/lego/dns/netcup/) |

Each form has a **credential help** panel: a button to the provider's token page (prefilled where the
provider allows it: Cloudflare and OVHcloud), the permissions to select and the provider's own docs.
The provider pages they link to are listed in `apps/web/src/lib/dns-provider-guides.ts`; providers move their consoles, so the provider's own documentation wins when the two differ.

Provider caveats worth knowing:

- **GoDaddy:** lego's provider authenticates with a *classic developer key*. GoDaddy's developer
  docs call it "scheduled for retirement" and the v3 Domains APIs need a Personal Access Token
  instead. Expect this provider to stop working when GoDaddy turns classic keys off; delegate
  `_acme-challenge` with a CNAME to another provider or move the zone.
- **Namecheap:** API access is opt-in and the calling IP must be on the account's allowed list. The
  proxy calls from the server's public IPv4 address.
- **Gandi:** a Personal Access Token expires after at most one year; the credential needs rotating.
- **OVHcloud:** use one authentication method only; lego refuses a mix.
- **Google Cloud DNS:** paste the whole service account key file; it is compacted to one line.

### Any other lego provider

**Add credential → Any other lego provider.** Type the provider's lego code (the form suggests the
other 200 or so from the catalog), then add name/value variables. The form links the lego page of the
code and lists every variable lego reads for it. The control plane checks each name against the
catalog **for that code** and refuses anything else with 422 `invalid_credential`, so a credential can
never put `TRAEFIK_*`, proxy or loader variables (`HTTPS_PROXY`, `LD_PRELOAD`) in the proxy
container's environment. Left out of the catalog on purpose: the `exec` and `manual` providers
(they run a program or wait on stdin) and variables that read a file (`*_FILE`, `*_PATH`: the proxy
container has nothing to read). Values of variables whose name does not say they are an identifier or
a setting (`_ID`, `_USER`, `_REGION`, `_ENDPOINT`, `_URL`, `_TTL`, `_TIMEOUT`, …) are write-only.
A generic credential needs at least one variable; which ones depends on the authentication method,
so read the provider's lego page.

`GET /api/v1/dns/providers` lists the providers with a form; `GET /api/v1/dns/lego-providers` lists
the rest with their variables. Agents older than this feature (no `dns01-lego` capability) are
refused for every provider but the original four (422 `agent_too_old`).

The proxy container has **one environment**: two resolvers cannot both set the same variable name
(for example Route 53 and Lightsail both read `AWS_ACCESS_KEY_ID`). Staging the second resolver
fails with 422 `dns_variable_clash`; use a different provider for one of them.

Only these names can ever reach the proxy container; the agent drops everything else.

## Set it up

1. **Servers, Proxy, DNS-01 certificates**: add a credential (administrator), then pick it
   for the provider's resolver, then add a wildcard certificate.
2. **Deploy proxy.** The plan lists the changes and says the proxy container is
   recreated (about a second of dropped connections). A rotated credential shows as
   "credential rotated" and recreates it too. Nothing reaches the server before this.
3. Add a wildcard domain to a service (`*.example.com`, or pick the resolver for it);
   it is served by `HostRegexp` and covered by the certificate. Point a wildcard `A`
   record at the server.

The certificate's **status and expiry** come from the proxy itself: the agent opens a TLS
connection with the wildcard's SNI and reads the certificate Traefik serves. "Waiting for
certificate" means the proxy still serves its default certificate; an error shows
Traefik's ACME log line for the resolver. Renewal is Traefik's job (30 days before expiry).
Use **Let's Encrypt staging** on a resolver while testing; staging certificates are not
trusted by browsers.

## How secrets travel

- The database holds the values only sealed. `agent_commands.payload` carries variable
  **names**; the values are attached to the outgoing frame at send time (`dns01:<credential>:<NAME>`)
  and written to the container environment by the agent. The agent keeps them in its
  `0600` state file like uploaded certificate keys, so the proxy can be recreated offline.
- `docker inspect` on the host shows the environment: Docker access is root-equivalent
  anyway (see the threat model). Use API tokens limited to the zone.

## Limits

- Resolver and credential changes are an intentional proxy restart; they are never hot.
- Observed state uses the agent's TLS handshake; a CA outage looks like "waiting".
- `defaultGeneratedCert` is rendered through the REST provider; verify it against Let's
  Encrypt **staging** before relying on it (the anchor router that requests the wildcard is
  the primary mechanism).

## Names in a connected Cloudflare zone (automatic)

When Cloudflare is connected (**Settings, Integrations**), a domain, a wildcard domain or a status
page hostname that lives in one of its zones gets its certificate through **DNS-01 with the
connection's token**, with no resolver or credential to set up. It does not matter whether the name
is proxied (orange cloud) or DNS only, and it does not need port 80: a name that is issued over
DNS-01 does not depend on Let's Encrypt's HTTP-01 request getting through Cloudflare.

| | |
|---|---|
| What is chosen | The rule's resolver is `cloudflare` (whatever a person set for that name) and the server gets a resolver **Cloudflare connection (automatic)** whose credential is the connection's token. Nothing of it is stored: it follows the connection, its zones and its token. |
| Evidence | NebulaCtrl has no way to ask a token for its permissions, so it goes by what already happened: a DNS sync **wrote the name's record in that zone with the connection's token**, which shows the token may edit the zone. That is the name's DNS status `managed` with no error since. A record somebody else created (`present`), a conflict, a missing record or a name that has not been synced yet proves nothing. |
| Waiting | A name without that evidence keeps HTTP-01, and its certificate shows the **fallback** "Waiting for the first successful DNS sync in this zone". The sync that writes the record is the evidence: the next route table names the resolver. |
| Withdrawn | When a later DNS write fails the name falls back to HTTP-01 again. A refusal (HTTP 401 or 403) says "Cloudflare refused the last DNS change (the token lacks Zone, DNS: Edit?)"; another failure shows its error. The next successful sync brings DNS-01 back. |
| Also needed | The zone is active, the server has an ACME email and an agent that supports DNS-01. A token limited to some zones only sees those zones, so only their names qualify. |
| Not touched | Names outside every connected zone (a resolver set by hand stays), tunnel domains (Cloudflare ends their public TLS; the origin asks for no certificate) and names a wildcard certificate of the server covers. A `cloudflare` resolver you staged on the server takes the place of the automatic one: one resolver per provider. |

**Deploy proxy applies it.** A resolver is static proxy configuration, so the first time a server
serves such a name the plan lists `dns01.resolver.cloudflare` (credential "Cloudflare connection
(automatic)") and says the proxy container is recreated. Until you deploy, the rule already names
`cloudflare` but the proxy does not run that resolver, so the name keeps its HTTP-01 certificate
source and the domain shows **staged**. The sync that first leaves a name `managed` also pushes the
route table, so a server that already runs the resolver switches the name at once.

**Which certificate a host really has.** Traefik asks a resolver for a certificate only for names
its store does not hold one for (`getUncheckedDomains` consults the default store, which has the
certificates of every resolver, Traefik v3.7.16). A certificate Let's Encrypt already issued over
HTTP-01 therefore keeps being served through the switch, with no gap, and **keeps being renewed over
HTTP-01** by the default resolver; the DNS-01 resolver issues for names that have no certificate
yet. Nothing replaces a certificate that is still valid. So the agent reads the domain names of the
ACME storage files (`acme.json` for HTTP-01, `acme-<provider>.json` for each DNS-01 resolver; no
certificate or key is read out) and reports, per host, which file holds its certificate. The domain's
certificate shows it as **issued via** `http-01` or `dns-01`: a name chosen for DNS-01 whose
certificate is issued via `http-01` renews over HTTP-01, and only `dns-01` (or no certificate yet,
which DNS-01 will issue) is a DNS-01 certificate.

**The token.** It travels only in the command's secrets, like every DNS-01 credential, never in a
payload or in the dynamic configuration. Replacing the connection's token reaches the proxy with the
next route push (the container is recreated, as for a rotated credential). Removing the connection
leaves the proxy with the value it has until the next Deploy proxy removes the resolver.
