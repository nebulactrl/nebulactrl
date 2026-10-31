---
title: Secrets at rest
description: "How NebulaCtrl seals stored secrets: key ring, ciphertext format, key rotation with rekey, release retention and what is and is not protected."
section: Teams and security
order: 55
---

# Secrets at rest

NebulaCtrl stores every secret it can read back (variable values, git tokens, registry and cloud credentials, SSH deploy keys, TOTP seeds, backup passwords) encrypted in Postgres with AES-256-GCM. This page says what that protects, how the format works, how to rotate the key, and what the sealing does **not** do. The wider picture is in the [threat model](threat-model.md); external secret managers (`${{vault:…}}`, Doppler, Infisical) that keep values out of the database altogether are in [secret managers](../operations/secret-managers.md).

## What it protects, and what it does not

| Situation | What an attacker gets |
|---|---|
| A Postgres dump, replica, backup or volume snapshot **without** the key | Ciphertext for every sealed column. Not protected: variable names, secret flags, topology, domains, git URLs, the audit log, argon2id password hashes |
| The same **plus** the key | Everything. Rotate every secret |
| Write access to Postgres without the key (SQL injection, a bad restore script) | Cannot read secrets, and cannot move one: a ciphertext is bound to its table, column and row, so copying it into another row or column fails to decrypt (below) |
| Root on the control-plane host, or anyone with Docker access there | The key (`docker inspect` shows an environment variable; a key file does not appear there) and the database. The key and the database live on the same host in the default install, so sealing protects against **off-host** leaks, not against a compromised control-plane host |
| Root, or Docker access, on an app host | The plaintext environment of every container there. Environment variables are how containers receive secrets; they are visible in the Docker daemon on that host |

**One key serves the whole instance**, every team included. Teams are an authorization boundary, not a cryptographic one ([threat model](threat-model.md#security-boundary-in-one-paragraph)).

## Ciphertext format

Every sealed value starts with a version byte.

```
v2 (current)         0x02 ‖ keyID (4 bytes) ‖ nonce (12 bytes) ‖ AES-256-GCM(ciphertext ‖ tag)
                     AAD = 0x02 ‖ keyID ‖ "<table>:<column>:<rowID>"

v1 (legacy, readable) 0x01 ‖ nonce (12 bytes) ‖ AES-256-GCM(ciphertext ‖ tag)
                     AAD = 0x01
```

- **Key id.** The first four bytes of an HKDF-SHA-256 derivation of the key (label `nebulactrl secret-key-id`). It tells `Open` which key of the ring to use and cannot be reversed to the key. Two keys with the same id are refused at start-up.
- **Context binding (AAD).** The additional authenticated data contains the version, the key id and `table:column:rowID`. The row id is the row's uuid, or for tables keyed otherwise a composite (`service_id/KEY` for service variables, `domain_id/username` for domain basic-auth users, `workspace_id` for the per-team integrations, a constant for the instance-wide singletons). Move a ciphertext to another row, column or table and decryption fails with the same `decryption failed` error every failure returns.
- **v1 values** (the earlier format) still open: under any key of the ring, with no context check. `rekey` rewrites them as v2.
- **Nonces** are 96 random bits per seal. At the volumes of a control plane that is far below the invocation limit NIST SP 800-38D sets per key.
- Fingerprints and digests (variable change detection, the build-reuse hash) use a **separate** HKDF-derived HMAC key, never the encryption key itself. After a rotation, stored fingerprints are matched against every key of the ring; the build-reuse hash changes once with the key (one extra build).

## The sealed columns

A central registry lists every sealed column (`internal/secretbox/registry.go`); `rekey` walks exactly that list, and a test fails when a migration adds a `bytea` `*_enc` column that is not registered.

| Group | Columns |
|---|---|
| Variables | `service_variables.value_enc`, `.template_enc`; `shared_variables.value_enc`, `.template_enc`; `variable_versions.value_enc` (non-secret versions only) |
| Releases | `releases.env_enc` (the resolved environment a release was deployed with) |
| Service credentials | `services.git_token_enc`, `.registry_auth_enc`; `service_ssh_keys.private_key_enc`; `registries.push_auth_enc`, `.pull_auth_enc` |
| TLS and proxy | `certificates.key_enc`; `domain_auth_users.hash_enc`; `dns_credentials.secrets_enc` |
| Integrations | `cloudflare_connections.token_enc`; `git_connections.credentials_enc`, `.webhook_secret_enc`; `tailnet_integrations.secret_enc`; `secret_integrations.credentials_enc`; `notification_channels.secrets_enc`; `log_drains.secrets_enc`; `platform_services.secrets_enc` (the team's Headscale); `geo_credentials.maxmind_key_enc` |
| Built-in services | `instance_services.secrets_enc` (the system bucket's key and the registry's token-signing key); `team_service_slices.secret_enc` (the secret of each team's bucket key) |
| Backups | `backup_destinations.access_key_enc`, `.secret_key_enc`; `instance_backup_settings.access_key_enc`, `.secret_key_enc`; `volume_repos.password_enc` (the restic repository password) |
| Other | `auth_providers.client_secret_enc`; `instance_settings.smtp_password_enc`; `database_rotations.new_password_enc`; `user_totp.secret_enc` |

`NEBULACTRL_SECRET_KEY` is therefore also the key to your restic backup repositories, every user's TOTP seed, the single sign-on client secret and the keys of every team's slice of the [built-in services](../operations/platform-services.md#credentials), not only to app variables: without it the sealed slice keys cannot be read, which would strand every team's built-in backups. Teams never see a slice key; the store's own admin key is not sealed in the database, the installer writes it to `.env`.

## Configuring keys

| Variable | Meaning |
|---|---|
| `NEBULACTRL_SECRET_KEY` | The **current** key: base64 of 32 random bytes (`nebulactrl-server keygen`). New values are sealed with it |
| `NEBULACTRL_SECRET_KEY_FILE` | Path to a file holding the current key (a Docker or systemd secret; surrounding whitespace is ignored). Setting both this and `NEBULACTRL_SECRET_KEY` is an error |
| `NEBULACTRL_SECRET_KEY_PREVIOUS` | Optional list of earlier keys (comma, space or newline separated). They only **open** existing values |
| `NEBULACTRL_SECRET_KEY_PREVIOUS_FILE` | The same, read from a file |

A key file keeps the key out of `docker inspect` and `/proc/<pid>/environ` of the control-plane container. Mount it read-only, mode 0400, owned by the container user; with Compose use a [`secrets:`](https://docs.docker.com/compose/how-tos/use-secrets/) entry and set `NEBULACTRL_SECRET_KEY_FILE=/run/secrets/nebulactrl_secret_key`. It does not protect against root on the host. The installer still writes the key to `/opt/nebulactrl/.env` (0600); switching an installed instance to a file is a manual step.

At start-up the control plane logs the ring (`secret key ring`, with the current key id) and, when values are not on the current key or still in the v1 format, a warning with their number and the hint to run `rekey`.

## Rotating the key

```
nebulactrl-server keygen                          # 1. a new key
# 2. NEBULACTRL_SECRET_KEY=<new>   NEBULACTRL_SECRET_KEY_PREVIOUS=<old>   → restart the control plane
nebulactrl-server rekey --dry-run                 # 3. what would change, and whether everything decrypts
nebulactrl-server rekey                           # 4. re-encrypt everything to the new key
# 5. remove NEBULACTRL_SECRET_KEY_PREVIOUS → restart
```

Run `rekey` with the same environment as the server (it reads the same variables, plus `NEBULACTRL_DATABASE_URL`). In the standard install: `docker compose exec control-plane nebulactrl-server rekey`.

What `rekey` does:

- Walks every registered column and rewrites each value that is not already a v2 value on the current key. That includes **v1 values**: running it once upgrades them under the existing key even if you never rotate.
- Works in batches (`--batch`, default 200 rows), each in its own transaction that locks its rows. It **resumes**: rows already on the current key are skipped, so an interrupted run is finished by running it again, and a second run changes nothing. It can run next to a live control plane (rows are locked while rewritten, and the update re-checks the old ciphertext). A session advisory lock stops two runs at once.
- Rebuilds the AAD from the row's own key columns, so a ciphertext someone copied into the wrong row is **not** re-sealed (that would launder it): it is reported and left untouched.
- Finishes with a verify pass that opens every value with the **current key alone**. Only when that passes does it print that the previous keys can be removed.
- `--dry-run` decrypts everything that needs rewriting and writes nothing.
- Exit code: non-zero when any value could not be decrypted with the keys in the ring (a key was removed too early, or data is corrupt). The report lists the columns and row ids, never values.

Things to know:

- **Keep the old key** as long as database backups from before the rotation might be restored: those backups hold ciphertext on the old key. The backup of the control plane never contains a key ([backup and restore](../operations/backup-restore.md)).
- Old keys remain valid forever in `NEBULACTRL_SECRET_KEY_PREVIOUS`; the list only costs a few microseconds per open. Remove them when you are done.
- Rotating the key does **not** rotate the secrets themselves. If the old key leaked together with a database copy, every secret in that copy must be rotated at its source.
- Variable version history stores fingerprints only for secrets (never values); old fingerprints stay valid because they are matched against every key of the ring.
- If the key is lost and no backup exists, the sealed values are gone: variables, git and registry credentials, SSH keys, integration tokens, TOTP seeds and the restic password of volume backups. There is no recovery. Back the key up first.

## Release secret retention

Every release stores the sealed environment it was deployed with (`releases.env_enc`), secret values included, so a rollback restores exactly what ran. Without limits, a variable you deleted or rotated would stay in the database, and in every dump, as long as the service exists.

An hourly job therefore clears `env_enc` from every release **outside its service's rollback window**: the newest `imageRetention` succeeded releases (default 5, the set a rollback can target), plus the release of the running deployment and the release of any deployment in flight. The release row, its image reference, source and configuration stay.

Rolling back to a release whose environment was cleared **does not start the old image with an empty environment**. The deployment resolves the service's **current variables** instead, records that in the deployment timeline, and the rollback response carries the same notice. If the current variables do not resolve (an unresolved reference, an unreachable secret manager), the rollback fails with the reason and changes nothing. After a successful rollback the release records the environment it was deployed with and is inside the window again until it ages out.

Consequence: rolling back past the window restores the old code and configuration, **not** old secret values. Values inside the window are restored as they were. Database backups taken before a release was pruned still contain its sealed environment until they expire.

## Process hygiene

- The control plane sets `RLIMIT_CORE=0` and (Linux) `PR_SET_DUMPABLE=0` at start-up: no core dumps, and no `ptrace` or `/proc/<pid>/mem` access for other processes of the same user. `rekey` does the same.
- Key bytes read from the environment or a key file are zeroed after the key ring is built. Decrypted plaintext buffers are zeroed after use where the code holds them as bytes. Go strings cannot be wiped, so a decrypted value that becomes a string lives until it is collected.
- Values fetched from an external secret manager are kept in memory only to mask them in logs, for at most 30 minutes (re-fetched on demand), never longer than the process.
- Secrets are never written to logs: the structured logs name keys, ids and counts, not values; log lines from your apps are redacted for every secret of six or more characters (plus its URL-escaped and base64 forms) before they are stored or shown.
- There is no `mlock` and no TPM or cloud KMS sealing of the key; these were weighed and are not built.

## Who can read secrets, and what is audited

Secret variables are write-only: no API, UI, CLI or MCP read returns them, and a Viewer never sees any value. The paths that do reveal a value are audited without it: a managed database's connection string (`secret.reveal`) and a volume backup's recovery key (`volume_backup.recovery_key.reveal`). Nothing reveals the built-in services' keys: there is no action for it. Reads from an external secret manager are audited per operation as `secret.fetch` / `secret.fetch.denied` (references, service and who started it, never values), and an integration can be limited to selected projects and environments ([secret managers](../operations/secret-managers.md)). Sealing changes record the key name only.

## See also

- [Threat model](threat-model.md): trust boundaries, including teams.
- [Variables](../operations/variables.md): secret flags, write-only values, send-time delivery.
- [Backup and restore](../operations/backup-restore.md): the key is never in a backup.
- [Secret managers](../operations/secret-managers.md): keep values out of the database.
