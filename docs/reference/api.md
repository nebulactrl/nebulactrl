---
title: API reference
description: "Every REST endpoint, generated from the OpenAPI spec, with authentication, errors and conventions."
section: Reference
order: 20
api: true
---

# API reference

NebulaCtrl's dashboard, CLI and MCP server all use the same REST API under `/api/v1`. Everything you can click, you can call. The pages below list every operation, grouped by area. They are generated from `openapi/openapi.json`; a running control plane also serves the spec at `/api/v1/openapi.json`.

## Authentication

There are two ways to authenticate:

| Method | Use it for |
|---|---|
| `Authorization: Bearer nct_…` | Scripts, CI and the CLI. Create a token under **Account settings → API tokens**. Tokens are accepted only in this header, never in a query string. |
| The `nebulactrl_session` cookie | The dashboard. Cross-origin writes are rejected, so a browser page on another site cannot act as you. |

A token's **scopes** limit what it can do, and its project list can limit where. Scopes are intersected with the creator's role when the token is created and again on every use. See [API tokens](../operations/teams.md#api-tokens).

| Scope | Allows |
|---|---|
| `projects:read` | Read projects, environments, services, deployments and logs |
| `projects:write` | Create and change projects, environments, services, variables and domains |
| `deployments:write` | Deploy, restart and stop services |
| `deployments:rollback` | Roll back a deployment |
| `servers:read` | Read servers and their metrics |
| `servers:write` | Enroll, change and revoke servers |
| `proxy:write` | Publish routes, expose TCP/UDP ports, certificates and tailnet egress |
| `admin` | Everything the creating member's role allows |

An operation marked **Public** needs no credentials, for example the first-run setup and sign-in calls.

## Errors

Errors are [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) `application/problem+json` with an extra stable `code` field. Match on `code`, not on the message.

```json
{
  "status": 409,
  "code": "deployment_in_progress",
  "title": "Conflict",
  "detail": "another deployment of this service is active"
}
```

| Status | Meaning |
|---|---|
| `401` | No valid credentials: no session, or a missing, revoked or expired token |
| `403` | Credentials are valid but lack the scope or role, or the project is not allowed |
| `404` | Not found, **or not visible to you**. NebulaCtrl does not reveal whether a resource in another project exists |
| `409` | A conflict, such as `deployment_in_progress` or a name already taken |
| `412` | Stale `If-Match`: the resource changed since you read it (`generation_conflict`) |
| `422` | Valid JSON that fails validation. `errors` lists the fields |

## Conventions

- **Lists** are paginated with `?cursor=` and `?limit=` (default 50, maximum 200). The response carries `nextCursor` while more items exist.
- **Idempotency.** Mutating calls that create things accept an `Idempotency-Key` header (up to 255 characters). A retry with the same key replays the original response for 24 hours, so a flaky network cannot create two deployments.
- **Optimistic concurrency.** Updating a service takes `If-Match` with the generation you read. A mismatch returns `412` with `generation_conflict`.
- **Changes are staged.** Writing configuration, variables or domains stages the change and returns immediately. Nothing restarts until you call a deploy endpoint.
- **Deploy and rollback return `202`** with the deployment. They run asynchronously and never report success before the agent has finished; poll the deployment, or use `nebulactrl services deploy --wait`.
- **Secrets are write-only.** A secret variable is returned as `{key, isSecret: true, set: true}`, never its value.
- **Live updates** are Server-Sent Events: `GET /events` for resource changes, and the log endpoints with `follow=true`.

## Endpoints by area

Pick an area below, or use the search.
