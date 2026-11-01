---
title: Maintenance mode and error pages
description: "Put a service into maintenance and customise what visitors see on errors."
section: Applications
order: 140
---

# Maintenance mode and error pages

Two things share one design: a **maintenance mode** you switch on per service, and **automatic error pages** the proxy shows when there is nothing healthy to forward to. Both are served by the agent's own tiny static server, look like the login screen (the animated "Eclipse" backdrop, a pixel-dithered ringed planet whose light follows the cursor, behind a centred card), and are single self-contained HTML files: no external request, no web font, no tracking.

| Situation | Status | Page |
|---|---|---|
| Service is in maintenance mode | `503` + `Retry-After` | "Down for maintenance" (your title, message and expected-back time) |
| The service answers `502`, `503` or `504`, or has no healthy replica (stopped, crashed, restarting) | the same status | "Service unavailable" / "Service timed out" |
| A host that no service of this server serves | `404` | "Nothing here: No service is configured for this address." |

Every page is sent with `Cache-Control: no-store`, `X-Robots-Tag: noindex, nofollow` and a strict Content-Security-Policy. Error and maintenance pages other than the 404 also carry `Retry-After` (the ETA of a maintenance window as an HTTP date, a fixed delay otherwise: 120 s without an ETA, 300 s once it has passed, 30 s for automatic error pages).

## Maintenance mode

Use it when you want visitors to see a clean page instead of a half-working app: a database migration, a manual upgrade, planned downtime.

- **Turn it on:** the service's Settings → **Maintenance**, the canvas card's **More ▸ Maintenance mode…** (and **End maintenance** once it is on), or the CLI:

  ```sh
  nebulactrl services maintenance acme/production/web on --title "Upgrading the database" \
      --message "Nothing is lost. This takes a few minutes." --eta 45m
  nebulactrl services maintenance acme/production/web off
  ```

- **API:** `PUT /api/v1/services/{serviceId}/maintenance` with `{enabled, title?, message?, eta?}` (scope `deployments:write`, audited as `service.maintenance.on` / `service.maintenance.off`). `GET /api/v1/services/{serviceId}/maintenance/preview` returns the rendered page as HTML, optionally for unsaved text (`?draft=true&title=…&message=…&eta=…`); the settings form's **Preview page** opens it. The service resource carries `maintenance: {enabled, title, message, eta, since}`.
- **Text:** title up to 80 characters, message up to 600 (plain text, line breaks kept, always HTML-escaped), `eta` a future time shown in the visitor's local time and sent as `Retry-After`. Empty fields show the default copy. Switching off keeps the text for next time and clears the ETA.
- **While on:** every domain of the service answers `503` with the page. HTTP→HTTPS redirects still happen first, redirect-only domains still redirect, and basic-auth / IP allow rules are not applied to the page (a maintenance page must not need a login). The post-deploy **verification probe keeps reaching the real service**, so a deployment during maintenance still proves the new release works, and ends with the maintenance window intact.
- **Instant, no deploy.** The control plane sends the server its route table (`sync_routes`), and a stopped/offline server picks it up when it reconnects. A service that was never deployed stores the setting and applies it with its first deployment.
- **Operational, never staged.** Like replica counts it does not bump the service's generation, does not create a "changes to apply" entry, is not part of a release and is **never rolled back**. A rollback or a redeploy leaves maintenance exactly as it was.
- **Idempotent.** Repeating a request that changes nothing records no audit event and sends nothing.
- **Not for TCP/UDP routes** (`422 maintenance_unsupported`): there is no page to show on a raw port.

## Automatic error pages

The agent adds an `errors` middleware (status `502-504`) in front of every service router and a lowest-priority catch-all router on ports 80 and 443. Together they replace Traefik's bare `Bad Gateway` / `no available server` / `404 page not found` text:

- A stopped service, a crashed container, a failing upstream health check and a connection error all end in `502` or `503`; a timeout in `504`.
- A request for a host (or a path of a path-scoped domain) that no router matches gets the `404` page. The probe entrypoint is unaffected: an unrouted host is still a `404` there, so post-deploy verification can never pass by accident.
- The status code is the real one, so monitors, CDNs and API clients behave correctly.

**One caveat:** the middleware replaces *any* `502`/`503`/`504` the upstream itself sends, including an application's own JSON error body for a deliberate `503`. If an API must return its own `503` body, answer `429`/`500` instead or keep that status below the proxy.

## How it works

The agent starts a second infrastructure container next to Traefik, **`nebulactrl-pages`**, on the `nebulactrl-edge` network. It publishes nothing; only Traefik reaches it (`nc-pages` → `nebulactrl-pages:8080`).

- **Image:** `nginx:1.30.6-alpine-slim`, pinned by digest (the multi-arch index `sha256:dede8895648b461178c3bc604f7eed4423efe5220b2c4d839f5eb3d34ac3f8f7`), about 5 MB per platform. It is the official image, from the stable branch; it only serves static files and needs no build per service.
- **Hardened:** all capabilities dropped except `CHOWN`/`SETGID`/`SETUID`, 64 MiB memory limit, rotating 1 MB logs, `unless-stopped`.
- **Content** is rendered by the agent from templates embedded in the binary (`deploy/pages`), written into the container before its first start (like Traefik's bootstrap file) and fingerprinted in a label. The nginx configuration maps `/err/<status>` to the error pages and `/m/<serviceId>` to a service's maintenance page, each with the right status and headers.
- **Changing the content replaces the container without a gap.** The agent orders it so no page ever vanishes while a router still points at it: entering or editing maintenance, the page is rendered *before* the route push; leaving maintenance, the routers move back *first* and the page is dropped after. The replacement starts next to the running server under the other of two names (`nebulactrl-pages` and `nebulactrl-pages-alt`, alternating) with the same network alias, has to answer its health check, and only then is the old server removed. A service in maintenance (or any error page) never sees a `502` from a replacement. If the new server cannot start, the old one keeps serving its old content. After an interrupted replacement both containers exist; the next apply keeps the one that runs and is up to date and removes the other.
- **It is excluded from the orphan sweep** (like the proxy and tailnet containers) and is repaired by the proxy health loop if someone removes it.
- **Failure is contained.** If the pages server cannot start (no disk, image pull blocked), route tables without maintenance still apply, with Traefik's own error pages (or the pages of the server that still runs) and a warning in the agent log; a table that *needs* the pages server (a service in maintenance) is refused with `maintenance pages unavailable` and the previous routes stay live, still with their maintenance pages. **A service in maintenance never reaches its upstream:** if no pages server runs at all, its routers answer a bare `503` from Traefik until the pages are back. The next apply retries.

An old agent without pages support ignores the maintenance flag; update the agent first.

## What the pages look like

The same engine as the sign-in screen draws the backdrop on a `<canvas>` (two WebGL2 passes, about 20 fps, paused in hidden tabs, one static frame under `prefers-reduced-motion`). Without JavaScript, without WebGL2 or in forced-colors mode the page falls back to an inline CSS halftone orb, with the same card and text. Fonts are the system UI and monospace stacks. The visited hostname is shown in mono on the card (filled in by a few lines of script; the maintenance page also carries it in the HTML). A small violet mark sits above the card; there is no other branding.

### Rebuilding the assets

Sources live in `deploy/pages` (`page.html.tmpl`, `page.css`, `page.js`, `nginx.conf.tmpl`) and are embedded into both binaries. The backdrop engine is a generated, committed bundle, so `go build` never needs bun:

```sh
go generate ./deploy/pages        # or: cd packages/ascii && bun run scripts/build-pages-engine.ts
```

It writes `deploy/pages/assets/eclipse.js` from the same sources as the login backdrop (`packages/ascii`). The control plane embeds the same assets to serve the maintenance preview.

## Troubleshooting

- **Visitors still see the app after turning maintenance on.** Check the server is online (the change is a `sync_routes` command; see the service's server page) and `docker logs nebulactrl-traefik`. `docker ps` on the host should show `nebulactrl-pages` running.
- **The generic `404 page not found` text appears.** The agent runs without the pages server (see "Failure is contained" above) or the request reached another reverse proxy first.
- **`docker logs nebulactrl-pages`** shows nginx's error log; access logging is off.
