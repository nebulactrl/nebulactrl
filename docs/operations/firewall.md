---
title: Service firewall
description: "Block or allow requests by IP address, country, path, method or header before they reach a service."
section: Applications
order: 135
---

# Service firewall

A service's firewall is an ordered list of **block** and **allow** rules the proxy applies to every request for every domain of the service, before the request reaches a container. Every HTTP service also has the **standard protection**, which needs no setup. Use your own rules to lock an admin path to your network, keep known attack tools out, or refuse countries you do not serve.

It lives in the service's **Settings → Firewall**. Like maintenance mode it is operational: **Apply firewall** changes the proxy at once, without a deployment. Changes are never staged or rolled back, and they survive deployments.

## Standard protection

Every HTTP service blocks requests for secret files that no application serves on purpose and that scanners probe on every public address within minutes of it going live:

- environment files: `/.env`, `/.env.production`, `/.envrc`
- version control folders: `/.git/…`, `/.git-credentials`, `/.svn`, `/.hg`, `/.bzr`
- credentials: `/.aws/…`, `/.ssh/…`, `/.docker/…`, `/.npmrc`, `/.pypirc`, `/.netrc`, `/.htpasswd`
- cloud and database credentials: `/.kube/…`, `/.azure/…`, `/.boto`, `/.s3cfg`, `/.pgpass`, `/.my.cnf`, `/.vault-token`, `/.terraform/…`
- AI tool configuration, which holds API keys and MCP tokens: `/.claude/…`, `/.cursor/…`, `/.continue/…`, `/.aider…`, `/.roo/…`, `/.windsurf/…`, `/.codex/…`, `/.gemini/…`, `/.mcp.json`, `/.litellm`
- shell histories: `/.bash_history`, `/.zsh_history`, `/.psql_history`
- `/.DS_Store`

It matches anywhere in the path (`/app/.git/config`), ignoring case, and the name must end there: `/.github`, `/.gitignore`, `/.well-known`, `/.cursorrules` and `/repo.git` are not touched. Editor folders and dotfiles that hold no secret (`/.vscode`, `/.idea`, `/.htaccess`, `/.bashrc`) are not part of it either. Blocked requests get the same HTTP 403 as your own block rules.

There is no switch. Your own rules run first, so to let one of these paths through (a public `/.env.example`, say), add an **Allow** rule for it. The Firewall settings list it under your rules ("Always on, after your rules"), and **Try a request** includes it. TCP and UDP routes have no firewall, so they have no standard protection either.

WordPress paths (`wp-login.php`, `xmlrpc.php`, `/wp-admin`, `phpmyadmin`) are not part of it, because NebulaCtrl runs WordPress and phpMyAdmin too. For other apps the **Block WordPress probes** recipe adds them.

## How rules are evaluated

- Rules are checked **top to bottom**. The **first rule whose conditions all match decides**:
  - **Block** answers HTTP **403** with the "Access blocked" page, and the request never reaches the service.
  - **Allow** lets the request through and **skips the rules below it**.
- A request that no rule matches reaches the service.
- A rule matches when **all of its conditions** match. A condition with several values matches when **any** of them does. Every condition can be negated ("is not", "does not contain", "is missing").
- A switched-off rule is kept but never checked.

| What | Operators | Notes |
|---|---|---|
| IP address | is, is not | Addresses or CIDRs, IPv4 and IPv6 (up to 200 per condition). An IPv4-mapped IPv6 visitor matches IPv4 networks |
| Country | is, is not | ISO 3166-1 codes. Needs geolocation on the server (see below) |
| Path | starts with, is, contains, matches | The decoded path, without the query |
| Method | is, is not | GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, CONNECT, TRACE |
| User agent | contains, matches | |
| Header | is, contains, matches, is present | By name (case-insensitive) |
| Query parameter | is, contains, matches, is present | By name |

**contains** ignores case. **matches** takes a Go regular expression (RE2: no lookarounds, no backreferences) and is not anchored, so add `^` and `$` yourself. A firewall holds at most 50 rules, each with at most 10 conditions.

**Try a request** under the rules describes a request (method, path, address, country, user agent, headers) and shows which rule decides, using the rules as edited, before you apply them.

**Add from a recipe** (the cards of an empty firewall offer the same) adds ready-made block rules, listed under three headings. A recipe that is not for everyone says so under its description; those cautions are given here in brackets. A recipe of several rules (attack signatures, attack tools) adds them all, and one that would take the firewall past 50 rules is greyed out.

- **Probes and exploits**
  - **Block PHP file probes**: random `.php` files and web shells at the site root and in common folders (not for apps written in PHP).
  - **Block PHP framework and debug probes**: PHPUnit's `eval-stdin.php`, Laravel Ignition, Symfony profiler, ThinkPHP exploits (safe for every app).
  - **Block WordPress probes**: `wp-login.php`, `xmlrpc.php`, `/wp-admin`, `/wp-content`, `/wp-json`, phpMyAdmin and the typo-squats scanners request (not for WordPress sites).
  - **Block exposed-service probes**: `/server-status`, Docker, Kubernetes, Elasticsearch and CouchDB APIs, Vite `/@fs`, phpinfo pages (not for dev servers or Docker and Kubernetes API hosts).
  - **Block Java and Spring probes**: Spring actuator, Tomcat manager, Jenkins, `WEB-INF`, and `.jsp`, `.aspx`, `.do`, `.action` pages (not for Java, Spring, ASP.NET or ColdFusion apps).
  - **Block CMS and admin-panel probes**: Joomla, Drupal, Magento, TYPO3, phpMyAdmin, Adminer and file managers (not for those products).
  - **Block backup and database dumps**: `backup.sql`, `site.zip`, `wp-config.php.bak`, one folder deep at most so code hosts stay open (not if you serve downloads named like that).
  - **Block router, camera and VPN probes**: `/boaform`, `/HNAP1`, `/SDK/webLanguage`, VPN-appliance paths and `/cgi-bin/` (not if you serve CGI scripts or an Axis camera).
  - **Block Exchange and Outlook probes**: Autodiscover, OWA, ECP, EWS (not for mail servers such as Exchange, Mailcow, Mailu or Zimbra).
  - **Block attack signatures**: seven rules for `${jndi:` (Log4Shell) in the user agent, `Referer`, `X-Forwarded-For` and path, `() {` (Shellshock) in the user agent and `Referer`, and requests for OS files such as `/etc/passwd`.
- **Bots and crawlers**
  - **Block attack tools**: scanners and exploit tools that name themselves (sqlmap, Nikto, Nuclei, masscan, zgrab, ffuf and about fifty more), as three rules: tools, vulnerability scanners and fake browser strings such as `Mozilla/6.0` or `(Hydra)`. Scanners that pose as a browser are not caught; the path recipes stop those.
  - **Block AI training crawlers**: GPTBot, ClaudeBot, CCBot, Bytespider, Meta-ExternalAgent, Amazonbot and about twenty more (not if you want to appear in AI training data).
  - **Block AI answer and search bots**: ChatGPT-User, Claude-User, PerplexityBot, OAI-SearchBot and similar fetchers (not if you want AI assistants to cite or open your pages).
  - **Block SEO crawlers**: AhrefsBot, SemrushBot, MJ12bot, DotBot, PetalBot and similar backlink crawlers (not if you track backlinks with those tools).
  - **Block script-library user agents**: curl, Wget, python-requests, Go-http-client, okhttp, axios and similar (websites only: breaks APIs, webhooks, health checks and monitors).
  - **Block empty user agents**: requests with no or a blank `User-Agent` header (not for APIs, webhooks or health checks).
- **Access**
  - **Lock a path to your network**: only your addresses reach `/admin`; fill in the addresses.
  - **Block countries**: refuse the countries you pick; needs geolocation on the server.
  - **Block TRACE and CONNECT**: refuse those two HTTP methods.

Googlebot, Bingbot and the link-preview fetchers of Facebook, WhatsApp, Slack, Discord and X are on no list, and no recipe touches `/robots.txt`, `/favicon.ico`, `/sitemap.xml` or `/.well-known/security.txt`. A recipe is only a starting point: open the rule it adds to remove a value that collides with something you serve.

## How the proxy enforces it

- **Without country conditions** (the common case) the agent compiles every rule into Traefik routers next to the domain's own: there is no extra hop, and nothing else runs. A firewall router repeats its domain's host and path and leaves out the more specific paths other services serve on the same host, so one service's rules never touch another service's requests.
- **With country conditions**, or with IP conditions on a domain behind Cloudflare (a [Cloudflare Tunnel](cloudflare.md) or a domain locked to Cloudflare, whose visitor address arrives in `Cf-Connecting-IP`), the **edge guard** evaluates the firewall instead. It is the agent's own binary running as the `nebulactrl-guard` container in the proxy's network namespace, so only Traefik can reach it (`forwardAuth` on `127.0.0.1:83`). It adds about a millisecond per request, and only on the services that need it. The agent creates the guard when a route needs it, gives it the rules and the server's GeoIP database, and removes it when no route needs it any more.
- The Settings page says which of the two runs ("in its own routing, with no extra hop" or "by a small guard next to the proxy").

### Country rules and geolocation

Country conditions read the server's GeoIP database, the one [traffic analytics](traffic-analytics.md) uses for request origins. Turn on traffic analytics and geolocation in the server's settings first: the free DB-IP country database needs no account. Saving a country rule for a service on a server without geolocation is refused (`422 geo_required`). Until the database is downloaded, the guard skips the rules that match countries rather than guess, so a "block everyone except Germany" rule never locks every visitor out.

## Seeing what was blocked

NebulaCtrl counts what the firewall blocks, per rule, for your own rules and for the standard protection (rules `stdsecrets` and the like show under their names). Open the service's **Settings → Firewall** for the last 24 hours next to each rule, the **Blocked by firewall** card of the service's [traffic analytics](traffic-analytics.md#blocked-by-the-firewall) for the trend, and the [Logs page](logs.md#the-logs-page) for the individual requests.

- **Counts** travel to the control plane as part of the minute aggregates of [traffic analytics](traffic-analytics.md): per minute, host and rule, a number. They are kept under the same retention as the other traffic aggregates (24 hours by minute, then hours, then days) and rolled up by addition. A blocked request is also an ordinary request of the service in the totals and in the 4xx count. A rule you delete later still shows its past blocks, as `removed`, under its id.
- **Which paths, addresses and countries** stay on the host. Every blocked request is a line in the host's [request logs](logs.md#request-logs) with the rule in `blocked_by` (and the visitor's country in `country` while geolocation is on). Search `stream:requests @blocked_by:*` for all of them, `@blocked_by:stdsecrets` for one rule, and read the facets of the Logs page for the blocked paths (`@path`), client networks (`@ip`, the /24 or /48 only), countries (`@country`) and user agents of that query. Request lines never leave the host, so the control plane has no list of blocked addresses or paths.
- **Both enforcement modes are counted.** A rule compiled into the proxy's routers is identified by its router, so it needs nothing but the agent. A rule the [edge guard](#how-the-proxy-enforces-it) evaluates (country conditions, or visitor addresses behind Cloudflare) answers its 403 with an `X-Nebula-Firewall-Rule: <rule id>` header, which Traefik passes on to the client and the access log keeps. The header shows the visitor the id of the rule that blocked them; ids of the standard protection are public (they are in this page), and the ids of your own rules are random strings.

Limits:

- **It needs traffic analytics** on the server, and request logs for the per-request view. With traffic analytics off, the firewall still blocks, but nothing is counted.
- **The guard header needs one proxy recreate.** The access-log flag that keeps `X-Nebula-Firewall-Rule` is part of the proxy's startup configuration, so a server's proxy container is recreated once, by the agent, after the agent is updated (the same recreate as when traffic analytics is toggled). Until then blocks by the guard count as ordinary 403s of the service, without a rule. Compiled rules are counted at once.
- **Nothing is backfilled.** Counts start when the agent first reports them. Earlier blocks are in the totals as 4xx (or, for rules compiled into routers on older agents, under the server's unrouted requests), never under a rule.
- **Counts can fall short under flood.** The agent keeps at most 2000 live (minute, service, host) counters, so a scan that sends thousands of different `Host` headers in one minute can drop events. The request store drops lines when its queue is full, and says so in the agent log.
- Facets over a wide range scan the newest part of the request store within a budget, so a busy service's blocked-path ranking can be partial; the response says so.

Read the counts through the API (below): `GET /api/v1/services/{serviceId}/firewall/blocks`, or the `firewall` section of `GET /api/v1/services/{serviceId}/analytics`.

## In nebula.toml

A service's own rules can live in [`nebula.toml`](../reference/nebula-toml.md#firewall-and-maintenance) as `[[services.<s>.firewall.rules]]` tables, each with its tests as `[[services.<s>.firewall.rules.conditions]]` tables. The order is the dashboard's (the first matching rule decides), and an apply changes the proxy at once, like **Apply firewall**: no deployment, nothing staged, nothing rolled back. It needs the `proxy:write` scope on top of the `projects:write` every apply holds.

```toml
# An office that may always get in, then the "Block WordPress probes" recipe.
[[services.web.firewall.rules]]
id     = "office"
name   = "Allow the office"
action = "allow"

[[services.web.firewall.rules.conditions]]
field  = "ip"
op     = "in"
values = ["203.0.113.0/24"]

[[services.web.firewall.rules]]
name   = "Block WordPress probes"
action = "block"

[[services.web.firewall.rules.conditions]]
field  = "path"
op     = "contains"
values = ["wp-login.php", "xmlrpc.php", "/wp-admin", "/wp-includes", "phpmyadmin", "/wp-content"]
```

- **Words of the file.** `field` is `ip`, `country`, `path`, `method`, `userAgent`, `header` or `query`; `op` is `in` (ip, country, method), `prefix`, `exact`, `contains`, `regex`, `equals` or `present`, as the [conditions table](#how-rules-are-evaluated) allows per field. "is not" is `not = true`; `name` is for `header` and `query` only.
- **The whole list is one unit.** An apply replaces the service's rules with the file's, in the file's order. Moving a rule is a change, and so is a rule added in the dashboard that the file does not list.
- **Ids are optional.** A rule without `id` keeps the id of the live rule at the same position, and a new rule gets a generated one, so a file without ids shows no change on the next plan. Write an `id` (1-16 lowercase letters or digits, unique) when a rule should keep its identity while it moves. Ids starting with `std` are reserved.
- **Limits:** at most 50 rules, 10 conditions per rule, 50 values per condition (200 for an `ip` condition). Mistakes are `invalid_value`, naming the rule and condition.
- **Checked at plan time:** a server whose agent predates firewalls is `agent_too_old`, and a `country` rule on a server without geolocation is `geo_required` (see [country rules](#country-rules-and-geolocation)). The plan is blocked instead of the apply failing. A tcp or udp route, a database and a stack take no firewall (`not_allowed_for_kind`).
- **The standard protection is not in the file.** It is always on, after your rules, and an export never writes it. To let one of its paths through, add an `allow` rule for it.
- **Removing the table releases it.** Deleting `[services.<s>.firewall]` leaves the live rules in place; `nebulactrl config apply --prune` removes the service's own rules (the standard protection stays). An empty `[services.<s>.firewall]` table says "no rules" and removes them on any apply.
- **Export** writes the live rules with their ids, so a round trip through the file keeps them.

## API

- `GET /api/v1/services/{serviceId}/firewall` (scope `projects:read`) returns `{rules, standard, enforcement, hosts, deployed, server, geoAvailable, supported}`. `standard` lists the standard protection (`{rule, description, examples}`, evaluated after `rules`). `enforcement` is `routes` or `guard` for HTTP services and `none` for tcp/udp routes.
- `PUT /api/v1/services/{serviceId}/firewall` with `{rules: [...]}` replaces the service's own rules (at most 50; scopes `projects:write` and `proxy:write`). An empty list removes them, and the standard protection stays. A rule is `{id, name, action: "block" | "allow", disabled?, conditions: [{field, op, name?, values?, not?}]}`. A rule without an `id` gets one, and ids starting with `std` are reserved for the standard protection. The change is audited as `service.firewall.update`, and a production service asks for approval when a token changes it (see [production approval](../reference/mcp.md#production-approval)).
- `GET /api/v1/services/{serviceId}/firewall/blocks?range=24h|7d|30d` (operationId `getServiceFirewallBlocks`, scope `projects:read`) returns `{range, from, to, trafficAnalytics, total, byRule, logsQuery}`. `byRule` is `[{id, name, kind: "standard" | "rule" | "removed", action?, requests}]`, largest first, named as the rules are now (`removed`: the rule is gone, `name` is its id). `trafficAnalytics` is `false` when the service's server does not count traffic (or the service has no server), so a zero means "not counted" and not "nothing blocked". `logsQuery` is `stream:requests @blocked_by:*`, the Logs explorer query for the blocked request lines of the service. The same per-rule counts and a `series` of blocked requests per step are the `firewall` section (`{blocked, byRule, series: [{ts, blocked}]}`) of the service, server and workspace analytics reports; server and workspace reports name the standard protection and list other rules by id.
- `422 invalid_firewall` names the rule and condition at fault, and `422 agent_too_old` means the server's agent predates firewalls (capability `proxy.firewall`). TCP and UDP route services have no firewall (`422 firewall_unsupported`).
