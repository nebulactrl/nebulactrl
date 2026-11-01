---
title: Secret managers and team variables
description: "Reference Vault, OpenBao, Infisical or Doppler secrets without storing them, limit who may use them, and see what was read."
section: Teams and security
order: 40
---

# Secret managers and team variables

A variable can point at a secret that lives in your secret manager instead of holding it. NebulaCtrl reads the value **when a deploy starts**, sends it to the server with the command and forgets it. The variable stores only the reference.

```text
DATABASE_PASSWORD = ${{vault:bao/apps/shop/db#password}}
DATABASE_URL      = postgres://shop:${{urlencode(vault:bao/apps/shop/db#password)}}@db/app
API_KEY           = ${{doppler:dp/backend/prd#API_KEY}}
```

Supported: **HashiCorp Vault and OpenBao** (KV v2, token or AppRole sign-in), **Infisical** (machine identity, universal auth) and **Doppler** (service token, or a service account or personal token). Bitwarden Secrets Manager is not supported: its SDK licence forbids redistribution and its protocol is undocumented.

NebulaCtrl's own sealed store (the encrypted variables in its database) stays the default. A secret manager is for teams that already keep their secrets elsewhere; nothing here replaces `NEBULACTRL_SECRET_KEY`.

## Reference syntax

| Manager | Reference |
|---|---|
| Vault, OpenBao, Infisical | `${{vault:<integration>/<path>#<key>}}` |
| Doppler | `${{doppler:<integration>/<project>/<config>#<key>}}` |
| Doppler, with a default or a service token | `${{doppler:<integration>#<key>}}` |

Usable anywhere a reference is: service variables, shared and team variables, inside `urlencode()` and `base64()`.

| Part | Vault / OpenBao | Infisical | Doppler |
|---|---|---|---|
| `<integration>` | the name you gave the integration (lowercase letters, digits, dashes) | same | same |
| `<path>` | the secret's path under the KV v2 mount, e.g. `apps/shop/db` (the mount is set on the integration) | the environment slug, then the folder: `prod/backend`; `prod` alone is the root folder | `<project>/<config>`, e.g. `backend/prd`; left out when the integration's default (or its service token) fixes it |
| `<key>` | the field to read, e.g. `password`. Strings, numbers and booleans are read as text | the secret's name, e.g. `DATABASE_URL` | the secret's name, e.g. `DATABASE_URL` |

A Vault or Infisical path may contain letters, digits and `. _ - / ~ @ + = :`, at most 512 characters, with no empty, `.` or `..` segment. A Doppler project or config is 1 to 64 letters, digits and `. _ -`. The key is letters, digits and `. _ - @ :`. To write the text literally, escape it: `$${{vault:…}}`.

A `vault:` reference needs a Vault, OpenBao or Infisical integration and a `doppler:` reference a Doppler one. Anything else is refused when you save the variable and again when you deploy: `the integration "bao" is a vault integration, which does not take doppler: references`. A service called `doppler` is unaffected: `${{doppler.KEY}}` (a dot) is an ordinary service reference.

An external value is always a secret: variables that contain one are secret too, are never returned by the API and are masked in logs.

## Add an integration

Team settings → Integrations → Secret managers → Add secret manager (admin scope; owners and admins). Each form has the credential help: where to create the credential and the exact commands or clicks.

**Vault / OpenBao.** Address, KV v2 mount (default `secret`), optional namespace, optional CA certificate for a private CA, and one of:

- **Token.** A token with a policy that can read the mount:

  ```hcl
  path "secret/data/*" {
    capabilities = ["read"]
  }
  ```

  Tokens expire and NebulaCtrl does not renew them: use a periodic token (`vault token create -policy=nebulactrl -period=720h`) or, better, AppRole.
- **AppRole.** Role id plus secret id (and the AppRole mount, default `approle`). NebulaCtrl signs in once per deploy. Create the role with `token_type=batch` so tokens expire on their own:

  ```sh
  vault policy write nebulactrl nebulactrl.hcl
  vault auth enable approle
  vault write auth/approle/role/nebulactrl token_policies=nebulactrl token_type=batch token_ttl=10m secret_id_ttl=0
  vault read auth/approle/role/nebulactrl/role-id
  vault write -f auth/approle/role/nebulactrl/secret-id
  ```

Narrow the policy path to what the team needs. In KV v2 the data lives under `<mount>/data/…`.

**Infisical.** Infisical URL (empty is Infisical Cloud US; `https://eu.infisical.com` or your own instance otherwise), the machine identity's client id and client secret, the project id and optionally an organization slug. Create the identity under Access Control → Machine Identities with universal auth, add a client secret, and add the identity to the project with a role that can read secrets (Viewer is enough).

**Doppler.** The token, optionally the Doppler API URL (empty is Doppler Cloud; only a private Doppler deployment sets it) and optionally a **default project and config** (both or neither).

- **Service token (recommended).** In Doppler open the project, choose a config, Access → Generate. The token is read-only and bound to that one config, so references need no project or config (`${{doppler:dp#API_KEY}}`) and it can reach nothing else. Create one integration per config you need. See [Service Tokens](https://docs.doppler.com/docs/service-tokens).
- **Service account token.** One token for several projects, with project access granted per environment (Team and Enterprise plans). Name the project and config in each reference, or set a default. See [Service Accounts](https://docs.doppler.com/docs/service-accounts).
- **Personal or CLI token.** It works, but it carries the write access of the person who made it, which Doppler advises against in live environments. If you must, set the project and config as above.

Doppler is read through its REST API v3 ([API reference](https://docs.doppler.com/reference/api)): `Authorization: Bearer <token>` over HTTPS. For each `(project, config)` NebulaCtrl makes **one** `GET /v3/configs/config/secrets/download?format=json&include_dynamic_secrets=false` per operation ([Download](https://docs.doppler.com/reference/secrets-download)) and serves every reference to that config from it, so a deploy with many references stays far below Doppler's secret read limit (120 a minute on the Developer plan, 240 on Team, 480 on Enterprise, per the API reference). Dynamic secrets are never requested, so no lease is issued by accident. The values are the config's *computed* values (Doppler's own references expanded). A secret that has no text value is treated as missing. The connection test uses `GET /v3/me` ([Me](https://docs.doppler.com/reference/auth-me)).

The credential is sealed with `NEBULACTRL_SECRET_KEY` and is write-only: the API returns `secretSet` only. Changing the address, auth method or identity (for Doppler, the API URL) requires entering the credential again, so a stored credential can never be redirected to another server. The default project and config and the scope are not an identity and can change freely. The name and kind cannot change (references use the name).

**Test** signs in and, if you give a path and key, reads that secret to check the path and permissions (for Doppler the path is the optional `project/config`). The value is discarded and never shown. The result is stored on the integration, and the reference that was read is recorded in the audit log.

Plain `http://` addresses work but send the credential unencrypted; the form warns about every address that is not `localhost`. Redirects are never followed (a standby node would otherwise receive the token): point the integration at the active node.

## Who may use an integration

Every integration is either **available to the whole team** (the default) or **limited to selected projects and environments**. Without a limit, any member who can write variables can reference anything the integration's credential can read. Limit it when the credential reaches more than one project needs.

In Team settings → Secret managers → Add or Edit, choose **Available to → Selected projects** and tick:

- a **project**: every environment of it, including environments created later, such as previews;
- or only some of its **environments**. Ticking every environment folds back into the whole project.

At least one must be chosen; changing the scope back to the whole team drops the list. The API takes `scope` (`team` or `selected`) and `grants` (`{projectId, environmentId?}`) on create and update; omitting `scope` on update keeps the stored one.

The limit is enforced in two places, before any request leaves for the manager:

1. When a variable is saved, planned or resolved, a reference to an integration the project and environment may not use is an unresolved reference: `reference ${{vault:locked/apps/db#password}}: the secret manager integration "locked" is not available to this project and environment (an admin can allow it under Team settings → Secret managers)`.
2. When a command is sent, the same check runs again. That is the check that matters: it also catches a variable that was valid before the integration was narrowed. The command is then not delivered and the deployment fails with `integration_not_allowed`, naming the reference and the project and environment. Nothing was read.

A scope with no project and environment, such as a caller that did not say which service it reads for, is refused for a limited integration (it fails closed). Narrowing an integration does not touch existing variables: those in projects that lost access fail at the next plan or deploy until someone changes the reference or the scope. Scope changes are audited (`secret_manager.update`, with the previous and new scope and the grants' ids).

The admin test button is not subject to scoping: it checks the credential itself.

## What happens at deploy time

1. Saving a variable, the deploy plan and every listing handle only the reference. Nothing contacts the secret manager.
2. A deploy stores the reference in the release (`env_enc` holds `${{vault:…}}`, never the value) and the command payload carries no value either.
3. When the server's command is **sent** (the first time and again after a reconnect), the control plane checks each integration's scope, signs in to each integration once, reads each secret once and attaches the values to the command frame as send-time secrets, like every other secret. They are never written to the database, logs or audit trail.
4. The values stay in memory only: per operation for the fetch itself, and for log masking in the last three deploys of the service. After a control-plane restart they are fetched again, at most once a minute per service, to keep masking running.

Because the value is read at send time, a **rollback** or **redeploy** of an old release reads the secret's current value, not the value of the original deploy. A change in the secret manager reaches a service on its next deploy; nothing is pushed.

If a secret cannot be read, the command is **not delivered** and the deployment fails with "Not delivered: could not resolve the command's secrets: …. No changes were made." The message names the reference and the reason, never a value:

```text
reference ${{vault:bao/apps/shop/db#password}}: permission denied
reference ${{vault:bao/apps/shop/db#password}}: no secret at apps/shop/db in mount "secret" (or the token cannot see it)
reference ${{doppler:dp/backend/prd#API_KEY}}: no secret "API_KEY" in Doppler project "backend", config "prd" (or the token cannot see it)
reference ${{doppler:dp#API_KEY}}: authentication failed (invalid or expired credential): Invalid Auth token
reference ${{vault:other/a#b}}: no secret manager integration named "other" (add it under Team settings → Secret managers)
```

A reference to an integration that does not exist, or that the project may not use, is shown as a warning when you save it and blocks the deploy (`unresolved_reference`). Database credentials and backup hooks that use an external value read it the same way, when the operation starts.

An integration cannot be removed while a variable references it (`409 integration_in_use`, with the variables listed).

## The secret access log

Every operation that reads external secrets leaves **one** audit event, written when the operation ends and kept whatever its outcome:

| Action | When | Metadata (never a value) |
|---|---|---|
| `secret.fetch` | a deploy command is sent, a task starts, a database credential or backup hook is resolved, or logs are re-masked after a restart | `service`, `project`, `environment`, `purpose` (`deploy`, `task`, `database`, `backup`, `log-mask`), `references` (the references read, sorted) and `count`; for a deploy also `deployment` and `startedBy` (the user, token or "System" that started it); `failed`: `{reference, error}` for the ones that could not be read |
| `secret.fetch.denied` | the same, when an integration's scope refused at least one reference | the same, with `denied`: the refused references |
| `secret_manager.test` | an admin tests an integration | `name`, `ok`, `read` and the `reference` that was read |
| `secret.reveal` | someone reveals a database connection string (private or public) | the service and key, `public` for the external one |
| `volume_backup.recovery_key.reveal` | an admin reveals a volume backup's repository password | the service |

The actor is the signed-in user or token when the operation runs for one (a database reveal, a test). A deploy command is sent by the control plane, so its actor is `system` and `startedBy` carries who started the deployment. The resource is the service, so the event also shows under that project.

**Volume.** A deploy that reads N references writes one row, not N, and only deploys of services that use external secrets write one: one extra row next to the `deployment.*` events a deploy already writes. A log-masking re-fetch happens at most once a minute per service, and only after a control-plane restart. There is no separate access-log table: the rows live in the audit log, which the team already lists and filters by prefix.

Where to look: Team settings → Audit log → **Secrets** (every `secret.` and `secret_manager.` action), or `GET /audit-events?type=secret`. A `secret.fetch.denied` event is the signal that a reference points at an integration its project may not use.

What is *not* in the log: values, credentials, and the fetched computed text. Reveals that return a value are always audited; every other read of a secret variable returns `{key, isSecret: true, set: true}` and is not a reveal.

## Run OpenBao yourself

NebulaCtrl does not run OpenBao for you as a platform service: it starts sealed, a single node is what upstream calls highly discouraged, and it would not remove the need for `NEBULACTRL_SECRET_KEY`. Bring your own, or install the **OpenBao** template from the gallery (Security), which starts one node on a volume.

The template is an ordinary service: `openbao/openbao` pinned by digest, integrated Raft storage on a single node (the `file` storage backend was development-only and is removed in OpenBao 2.7, so it cannot be used with a current release), the listener on port 8200 behind the platform proxy's TLS, and the data on the volume `data`.

- **Unsealing is manual after every restart.** OpenBao starts sealed. After the first start, open the service's terminal and run `bao operator init` once: it prints the unseal keys and the initial root token. Keep them somewhere safe and separate; NebulaCtrl never sees them and nobody can recover them. Then `bao operator unseal` with three of the five keys.
- **Every restart seals it again.** A redeploy, a server reboot or a crash leaves OpenBao sealed until someone unseals it. While it is sealed, a deploy whose variables reference its secrets is not delivered (`Not delivered … No changes were made.`); running services keep the values they have.
- Take snapshots (`bao operator raft snapshot save <file>`) and back the volume up. A lost volume or lost unseal keys cannot be recovered, even from backups.
- To read from it, create a KV v2 mount, a read-only policy and an AppRole (see above), then add a Vault / OpenBao integration. The control plane calls OpenBao itself, so use the service's public https address, not its private one, and limit the integration to the projects that need it.

The health check is "the listener is up", because a sealed or uninitialized OpenBao answers `/v1/sys/health` with 503 or 501. The unseal state is yours to watch.

## Team variables

`${{team.KEY}}` is a shared variable of the whole team, next to the project and environment layers (see [variables](variables.md#team-variables)). A team variable may itself point at a secret manager: `API_KEY = ${{vault:bao/shared/api#key}}`, referenced from every service with `${{team.API_KEY}}`. The integration's scope is checked against the project and environment of the service that is being deployed, not against the team variable.

## API and audit

`GET|POST /secret-managers`, `PUT|DELETE /secret-managers/{id}`, `POST /secret-managers/{id}/test`. Reading needs `projects:read`; everything else the `admin` scope. Audit actions: `secret_manager.create|update|delete|test` (name, kind, which fields changed, the scope and the grants' ids; never the credential), and the access log above.

## Limits

- Vault / OpenBao KV **v2** only (not KV v1, dynamic secrets or transit).
- Doppler: secrets of one config at a time, no dynamic secrets, no secret versions.
- No secret versions: the latest version is read.
- Values are read as text. A secret that is a JSON object must be split into fields.
- A secret that changes in the manager is not pushed: deploy to apply it.
- Bitwarden Secrets Manager is not supported (SDK licence forbids redistribution, undocumented protocol).
