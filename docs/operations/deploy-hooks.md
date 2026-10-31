---
title: Deploy hooks
description: "A URL per service that deploys it from CI or a container registry."
section: Applications
order: 35
---

# Deploy hooks

A deploy hook is a URL that deploys one service. Call it from a CI pipeline after
a build, from `curl`, or let Docker Hub or GHCR call it after every image push.
It is the same as pressing **Deploy**: the service's current configuration is
deployed, staged changes included, with the same health checks, rolling update
and rollback. The deployment shows **Via deploy hook** in the Deployments list.

```
POST https://<your control plane>/hooks/deploy/<service id>/<secret>
```

The hook is **off** until you turn it on. It needs no login: the secret in the
URL is the credential.

## Turn it on

**Service → Settings → Deploy → Deploy hook** (or the CLI):

1. Turn on **Enable deploy hook**. NebulaCtrl creates a secret and shows the
   complete URL **once**. Copy it into your CI system's secret store.
2. Pick the **Set it up** tab for your CI or registry: curl, GitHub Actions,
   GitLab CI, Docker Hub or GHCR.

```sh
nebulactrl services deploy-hook <service>             # state (never the secret)
nebulactrl services deploy-hook <service> --enable    # new secret, prints the URL once
nebulactrl services deploy-hook <service> --rotate    # replace the secret, prints the URL once
nebulactrl services deploy-hook <service> --disable   # off, secret deleted
```

`NEBULACTRL_PUBLIC_URL` must be set: it is the host of the URL (422
`public_url_required` otherwise).

Who may manage a hook: owners and admins, and developers or API tokens with
`deployments:write` on the project. Anyone with `projects:read` can see whether it
is on, when the secret was created and what the last call did, but never the URL.

### The secret

- 256 bits from the system random source (`ndh_` plus 43 URL-safe characters).
- Only its SHA-256 is stored. The URL is returned by exactly two requests (enable
  and rotate) and never again: not in the UI, API, CLI or audit log (a backup holds the hash, which cannot be turned back into the URL).
- **Rotate** replaces the secret at once; the old URL answers 404 from then on.
- **Turn off** deletes the secret. Turning on again creates a new one; an old URL
  never comes back.
- A wrong secret, a disabled hook, a deleted service and a malformed id all give
  the same `404`, so a guess reveals nothing.

Treat the URL like a password: anyone who has it can deploy that service. Keep it
in a CI secret, not in the repository or a chat. Reverse proxies and CI systems
may log URLs, so rotate it if it might have leaked. For services in a
**production** environment the Settings section says so in a warning.

## What a call does

| Call | Result |
|---|---|
| plain `POST` (curl, CI) | Deploys the current configuration. A git service builds the head of its branch; an image service pulls its configured image again. |
| `?tag=<tag>` or `?digest=sha256:…` (image services) | Deploys that image, see below. |
| Docker Hub or GHCR payload | Deploys when the pushed repository and tag are this service's image; otherwise `202` ignored. |

Responses (all `202` unless noted, JSON):

```json
{"status": "deploying", "ignored": false, "deploymentId": "…"}
{"status": "queued", "ignored": false, "reason": "a deployment of this service is running; …"}
{"status": "ignored", "ignored": true, "reason": "pushed tag \"canary\" does not match the service's tag \"stable\""}
```

| Status | Meaning |
|---|---|
| `404` | wrong secret, hook off, service gone |
| `422` | an image reference outside the service's repository (`image_outside_repository`), a tag or digest on a git service, or the service cannot be deployed (for example its server is offline or revoked) |
| `429` | rate limit, with `Retry-After` |

`202 deploying` only says the deployment was created. Follow it with
`GET /api/v1/deployments/<id>` or `nebulactrl deployments status <id>`; a CI job
that must wait can use `nebulactrl services deploy <service> --wait` with an API
token instead.

### A deployment is already running

A service has one active deployment. A hook call that arrives meanwhile is
**queued**, and the newest queued call replaces an older one, exactly like a git
push. When the running deployment ends, the queued call deploys (within about 10
seconds). A queued call whose image no longer belongs to the service (the image
was changed meanwhile) is dropped.

### Rate limit

6 calls per minute per hook with a burst of 3 (`429 rate_limited`, `Retry-After:
10`). Calls with a wrong secret do not use the budget.

### Audit

Every call with a valid secret writes an audit event `deployment.hook` (source,
outcome, image, deployment id, reason; actor "Deploy hook"). Turning the hook on or
off and rotating it write `deploy_hook.enable`, `deploy_hook.disable` and
`deploy_hook.rotate`. No event contains the secret.

## Deploy a specific image: `?tag=` and `?digest=`

For image services:

```sh
curl -fsS -X POST "$HOOK_URL?tag=v1.4.2"
curl -fsS -X POST "$HOOK_URL?digest=sha256:0123…"
```

The tag or digest is applied to the service's own repository: for
`ghcr.io/acme/api:stable`, `?tag=v1.4.2` deploys `ghcr.io/acme/api:v1.4.2`. A
full reference is accepted only when it names the same registry and repository
(`?tag=ghcr.io/acme/api:v1.4.2`); anything else is `422 image_outside_repository`,
so the URL can move a service between versions but never to someone else's image.
`tag` and `digest` together are refused.

**How it fits releases.** The override is **one release, not a configuration
change**: the release records the image reference you passed (in its source), the
service's configured image and its generation are untouched, and nothing becomes
a staged change. Everything else is as for a manual deploy: staged changes are
deployed too, health checks and rollback apply, and **Rollback** to that release
redeploys exactly that image. A later plain deploy (button, hook without `?tag=`)
goes back to the configured image. If you want the service to follow a new tag
permanently, change the image in Settings → Source.

## Registries

Docker Hub and GitHub call the hook with a JSON payload. NebulaCtrl deploys only
when the **pushed repository and tag** match the service's image; every other push
is answered `202` with `status: "ignored"` and the reason, so one webhook per
repository is safe. A service whose image has no tag matches `latest`; a service
pinned by digest never matches a tag push.

### Docker Hub

Repository → **Webhooks** → New webhook, URL = the hook URL.
Payload (from Docker's [webhook documentation](https://docs.docker.com/docker-hub/repos/manage/webhooks/)):
the pushed tag is `push_data.tag`, the repository `repository.repo_name`
(`namespace/name`). Docker Hub sends no signature, so the URL secret is the only
protection.

### GHCR (GitHub Packages)

Repository or organization → Settings → **Webhooks** → Add webhook: Payload URL =
the hook URL, content type `application/json`, **Secret empty**, events: **Packages**.
NebulaCtrl reads GitHub's [`package` event](https://docs.github.com/en/webhooks/webhook-events-and-payloads#package)
(and the older `registry_package` event): the repository is
`ghcr.io/<package.namespace>/<package.name>`, the tag is
`package.package_version.container_metadata.tag.name`, and only the `published` and
`updated` actions deploy. A push without a tag (the per-platform manifests of a
multi-architecture image) is ignored; so is GitHub's `ping`. GitHub's optional
`X-Hub-Signature-256` is not verified: the URL is the secret.

Anything else that posts JSON (a body that is neither payload) is a plain call.

## CI examples

GitHub Actions, with the URL in the repository secret `NEBULACTRL_DEPLOY_HOOK`:

```yaml
- name: Deploy to NebulaCtrl
  run: curl -fsS -X POST "${{ secrets.NEBULACTRL_DEPLOY_HOOK }}"
```

GitLab CI, with the URL in a masked variable:

```yaml
deploy:
  stage: deploy
  image: curlimages/curl:8
  script:
    - curl -fsS -X POST "$NEBULACTRL_DEPLOY_HOOK"
```

`curl -f` makes the job fail on `404`, `422` and `429`.

## API

`GET`, `PUT {"enabled": true|false}` and `POST …/rotate` on
`/api/v1/services/{serviceId}/deploy-hook`; the OpenAPI reference lists them under
**deploy-hooks**. `GET` needs `projects:read`, the others `deployments:write`.
`PUT enabled=true` and `rotate` return the `url` once. The hook endpoint itself
(`/hooks/deploy/…`) is not part of the authenticated API and has no CORS or CSRF
checks: it takes no cookies and no tokens.
