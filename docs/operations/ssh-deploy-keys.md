---
title: SSH deploy keys
description: "Clone private repositories over SSH with generated or uploaded keys."
section: Applications
order: 40
---

# SSH deploy keys for git sources

A git service can clone over SSH instead of HTTPS: **Settings → Source → Repository** accepts `ssh://git@host[:port]/org/repo.git` and the scp form `git@host:org/repo.git`. An SSH URL needs a **deploy key** and a **pinned host key**; both are managed per service under Source. They apply immediately and are never staged.

## Deploy key

- **Use SSH: generate a key** creates an Ed25519 key pair on the control plane. The private key is stored encrypted (AES-256-GCM, like every secret), is **write-only** (no endpoint returns it) and travels to the agent as a send-time secret, never in a command payload, release or log. The public key is shown with a copy button, its fingerprint, and, for github.com, gitlab.com, bitbucket.org, codeberg.org and gitea.com, a link to the repository's "add deploy key" page. Read-only access is enough.
- **Upload your own key** stores an unencrypted OpenSSH or PEM private key (Ed25519, ECDSA, or RSA of at least 2048 bits). Passphrase-protected keys are rejected.
- **Generate new key** / **Upload a key** on an existing key is a rotation: the old key stops being used on the next deploy. Add the new public key to the repository first.
- **Remove key** also drops the pinned host keys. A service with an SSH URL and no key fails its deploys with "has no deploy key".
- A pull-request preview of a service copies its key and pins (same repository). Previews from forks do not get them.

## Host key pinning (trust on first use)

The first fetch (a plan, a deploy, **Test connection**) reads the git host's SSH host key and records it per service and host (`host` or `[host]:port`). The control plane and the agent then only accept that key:

- The agent trusts nothing else: it is sent the pinned `known_hosts` line with every deploy and never falls back to `ssh-agent` or `~/.ssh/known_hosts`. A host that is not pinned fails the clone.
- If the host later presents a different key, the deploy fails with "SSH host key for … changed", the refused fingerprint is recorded and shown under **Host key** with **Accept new host key**. Accepting asks for confirmation and pins exactly the fingerprint you reviewed; if the server presents yet another key in between, nothing is pinned (409).
- Accepting is audited (`service.ssh_host.accept`), as are first-use pins (`service.ssh_host.pin`), refused keys (`service.ssh_host.mismatch`) and key changes (`service.ssh_key.create`, `.rotate`, `.delete`).

A mismatch seen only by the agent (for example, interception on the agent's network path only) fails that deploy with the same message in the build log; the control plane's own view of the key is unchanged, so the **Host key** row does not show it.

## Submodules

The deploy key and the pinned host key are also used for `ssh://` and `git@host:` **submodules on the same host and port** as the repository. A submodule on another host or port is not fetched (no key, no pinned host key for it). Hosts that scope a deploy key to one repository (GitHub) need the key added to the submodule repository as well, or a machine-user key that can read both. See [git submodules](builds.md#git-submodules).

## API

`GET /services/{id}/ssh` (`projects:read`), `POST /services/{id}/ssh/key` (generate, or upload with `privateKey`; `rotate: true` to replace), `DELETE /services/{id}/ssh/key`, `POST /services/{id}/ssh/test`, `POST /services/{id}/ssh/host-keys/accept {fingerprint}` (all `projects:write`).

## Limitations

- One key per service; there is no team-level key shared by several services (upload the same key to each).
- One pinned key per host and service. SSH certificates, `@cert-authority` and hashed known_hosts entries are not supported.
- Only `ssh://` and scp-style URLs; passwords inside the URL are rejected. SSH through a proxy or jump host is not supported.
- Webhook-driven auto-deploy and commit statuses need a git connection (HTTPS API); an SSH service can still be deployed by hand, by CLI or API.
