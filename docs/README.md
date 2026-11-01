# NebulaCtrl documentation

The pages in this folder are the single source of the **in-app docs**: the control plane serves them at `/docs` (public, searchable, no login), compiled into the web app when it is built. GitHub renders the same files, so every link is a relative `.md` link that works in both places.

## Layout

- [`getting-started/`](getting-started/what-is-nebulactrl.md): what NebulaCtrl is, the concepts, the quickstart.
- [`installing/`](installing/README.md): install the control plane, requirements and supported hosts, upgrade and roll back, uninstall.
- [`operations/`](operations/README.md): applications and deployments, databases and backups, servers, observability, templates, troubleshooting, known limitations. [`deploy-hooks.md`](operations/deploy-hooks.md) covers the per-service deploy webhook.
- [`security/`](security/README.md): threat model, login methods and 2FA, single sign-on, host shell, host hardening.
- [`reference/`](reference/api.md): the API reference intro (the endpoint pages are generated from `openapi/openapi.json`) and the CLI reference, generated from the command tree.
- [`decisions/`](decisions/0001-go-modular-monolith.md): architecture decision records.
- [`architecture/`](architecture/navigation-transitions.md): how route changes animate. The `*-research.md` notes are working documents and are not part of the in-app docs.

## For AI agents

The same pipeline writes [`/llms.txt`](https://llmstxt.org) (an index of every page, CLI, API and MCP first), `/llms-full.txt` (all pages in one file) and `/docs/<slug>.md` (any page as clean markdown, links absolute). The control plane serves them without login and fills in `NEBULACTRL_PUBLIC_URL` (or the request host) as the origin. The titles and descriptions in a page's frontmatter are what the index shows, so write them for a reader who has only that line.

## Writing a page

Every user-facing page starts with frontmatter. The build fails (and `bun test` says why) when it is missing or wrong:

```yaml
---
title: Variables and secrets          # the sidebar label and search title
description: "One sentence."          # shown in search and on the overview
section: Applications                 # one of the sections below
order: 50                             # position inside the section; ties sort by title
---
```

Sections, in sidebar order: Getting started, Installing, Applications, Databases and backups, Templates, Servers, Observability, Teams and security, Platform services (the built-in S3, registry and central logs, and the per-team Headscale), Reference, Troubleshooting, Internals.

- The first `#` heading is the page title on the page. Use `##` and `###` below it; they become the outline and the anchors.
- Link other pages with relative paths (`[variables](../operations/variables.md#syntax)`). Heading anchors follow GitHub's slugs. A broken page link or anchor fails the build. A link out of `docs/` (`../../SECURITY.md`) must point at a file that exists; it becomes a GitHub link in the app.
- Raw HTML is escaped, not rendered. Use markdown only.
- Callouts: `> [!NOTE]`, `> [!TIP]`, `> [!WARNING]`, `> [!DANGER]` on the first line of a blockquote.
- Code fences are highlighted at build time for `sh`, `yaml`, `json`, `go`, `sql`, `ini`, `dockerfile`, `nginx`, `diff`, `python`, `js`/`ts`, `http`.
- Use the words the UI uses, in **bold** (**Servers → Add server**). Check the button text in `apps/web` before writing it.

## Generated pages

Do not edit by hand; `make generate` rewrites them.

- `reference/cli.md`: `go run ./cmd/cli docs --out docs/reference/cli.md`. A Go test fails when it is stale.
- The API reference pages under `reference/api/<tag>`: built from `openapi/openapi.json` when the web app builds. Add the description of an operation in its Go handler, not here.

Contextual "Learn more" links in the app are `<DocsLink to="operations/backups#destinations" />`; a test checks that the page and heading exist.
