---
title: Security overview
description: "The two things to remember, and where each security topic lives."
section: Teams and security
order: 5
---

# Security documentation

- **[`threat-model.md`](threat-model.md)**: trust boundaries, what the platform protects and what it does not, teams, secret handling, agent identity, MCP, and a hardening checklist.
- **[`secrets-at-rest.md`](secrets-at-rest.md)**: how stored secrets are sealed (key ring, ciphertext format, binding to table/column/row), the key rotation runbook with `nebulactrl-server rekey`, release secret retention, process hygiene, and what sealing does not protect.
- **[`login-methods.md`](login-methods.md)**: password, passkeys (WebAuthn), TOTP two-step verification and recovery codes, requiring 2FA per team, OpenID Connect and GitHub single sign-on (setup, account matching, sign-up rules, trusting a provider's MFA).
- **[`sso-setup.md`](sso-setup.md)**: the bring-your-own guide to single sign-on (Admin → Sign-in and teams): what NebulaCtrl needs from a provider (callback URL, scopes, claims, PKCE), the five setup steps, the redirect URI you can copy before saving, **Test connection**, and a recipe with the issuer format and console link for Google, Microsoft Entra ID, Okta, Auth0, authentik, Keycloak, Zitadel, Pocket ID, GitLab, GitHub and any other OpenID Connect provider.
- **[`host-shell.md`](host-shell.md)**: the opt-in root shell on a server (off by default; Owners and Admins with two-factor authentication; production confirm; 2 sessions per person, 15 minutes idle; audited, I/O never recorded), how the agent runs it (a pty on the host, or a helper container that is always removed) and its limits.
- **[`host-hardening.md`](host-hardening.md)**: the Lynis audit of each server and its Security tab, the curated safe fixes (Lynis test ID, check, fix, rollback, downside), what is deliberately never changed (SSH, the firewall, accounts, Docker's forwarding), who may apply them (Owners and Admins, audited), the weekly audit and the `nebulactrl-agent hardening` command.

Also see:

- [`SECURITY.md`](../../SECURITY.md): how to report a vulnerability; the Docker-is-root warning and the tenancy boundary.
- [ADR 0002 Agent transport](../decisions/0002-agent-transport.md): agent identity, replay protection, revocation.
- [ADR 0004 Build on the target host](../decisions/0004-build-on-target-host.md): what building untrusted code on the target host implies.
- [Backup and restore](../operations/backup-restore.md): the secret key and the database.

## The two things to remember

1. **Docker access is root on the host.** A NebulaCtrl administrator is a root administrator of every enrolled server.
2. **Teams are an authorization boundary, not isolation between hostile tenants.** Everyone in a team is trusted to deploy to the team's hosts; teams share one control plane, database and secret key. Projects and Docker networks are not isolation between hostile tenants.
