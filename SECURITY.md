# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem. Use the repository's private vulnerability reporting (GitHub Security Advisories: *Security → Report a vulnerability*) and include affected version or commit, a description, reproduction steps and the impact you expect. If that option is unavailable, contact the maintainers privately through the address listed on the repository owner's profile.

You can expect an acknowledgement, an assessment, and a fix timeline from the maintainers; no fixed response SLA is promised yet. NebulaCtrl is on 0.x and has no long-term-support branches: only the latest 0.x release (and `main`) is supported, and fixes ship in a new release.

## What the security boundary is

### Docker access is root on the host

The NebulaCtrl agent drives the Docker Engine on each enrolled server. **Access to the Docker daemon is equivalent to root on that host.** Anyone who can make the agent do something (a control-plane administrator, a stolen API token with deploy scope, a compromised control plane database plus secret key) can run arbitrary containers, including ones that read the host filesystem. Treat the agent host, the control plane and its Postgres database as one trust domain.

What the platform does to limit the blast radius:
- The agent accepts a closed set of typed commands and never runs a shell string from the wire.
- Managed workloads run unprivileged, without host networking and without the Docker socket mounted; the platform never adds those. Privileged mode, devices, GPUs and runtimes are refused unless an instance admin switches that server to **Unrestricted** (typed confirmation, audited; [threat model](docs/security/threat-model.md)).
- Agents authenticate with an Ed25519 identity, connect outbound only, and can be revoked immediately.
- Variable values, git tokens, registry credentials, SSH keys and integration credentials are encrypted at rest with AES-256-GCM (key ring, ciphertext bound to its table, column and row, `rekey` to rotate) and are not stored in command payloads or returned by the API ([secrets at rest](docs/security/secrets-at-rest.md)).

What it does not do: build-time and run-time code from your repositories and images is untrusted but is executed by the host's Docker daemon. A malicious Dockerfile can run arbitrary commands during `docker build`, and a container escape is a host compromise.

### Teams and tenancy

An instance can host several teams (workspaces). Teams are an authorization boundary enforced in the control plane: a user, token or resource of one team gets a 404 on another's. Teams share one control plane, one Postgres and **one secret key**, so a database dump together with the key, a compromised control-plane host or an authorization bug exposes every team. Within a team, members and tokens with deploy rights are trusted to run code on the team's hosts. Projects, environments and per-environment Docker networks organize work and prevent accidents; they are **not** isolation between hostile tenants. Docker on a shared host is not a hardened multi-tenant boundary. Do not host mutually untrusted customers on one NebulaCtrl installation or on one enrolled server; give each its own instance.

### Secret key

`NEBULACTRL_SECRET_KEY` decrypts every stored secret of every team. Keep it out of version control, deliver it as a file (`NEBULACTRL_SECRET_KEY_FILE`) where you can, back it up apart from the database dumps, and rotate it with `nebulactrl-server rekey` (never by just replacing it). Losing every copy makes stored secrets unrecoverable. Details and the rotation runbook: [secrets at rest](docs/security/secrets-at-rest.md).

### Development setup

`deploy/dev/docker-compose.yml` mounts the host Docker socket into the dev agent container and uses well-known passwords (`nebulactrl`). It is for local development only. Never expose its ports to a network you do not control.

More detail: [`docs/security/`](docs/security/README.md).
