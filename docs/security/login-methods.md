---
title: Login methods
description: "Passwords, passkeys, two-step verification and single sign-on."
section: Teams and security
order: 20
---

# Login methods: passkeys, single sign-on and two-step verification

NebulaCtrl signs people in with a password, with a passkey, with a single-sign-on provider, or with a password or provider followed by a passkey or one-time code. This page covers what each does, how to set it up, and what it does and does not protect.

(The product calls a tenant a **team**; the code calls it a workspace. Everything below applies per user, per team or per instance as stated.)

## Summary

| | Who configures | Default |
|---|---|---|
| Password (argon2id) | each person | on |
| Forgot password (emailed reset link) | the instance administrator sets up SMTP; see [Forgot password](#forgot-password) | off until email can be sent |
| Passkeys (WebAuthn): sign-in without a password, or the second step | each person, in **Account settings → Security** | none registered |
| Two-step verification (TOTP) | each person, in **Account settings → Security** | off |
| **Require 2FA** for a team | an Owner or Admin of that team, in **Team settings → General** | off |
| OpenID Connect / GitHub sign-in | the instance administrator (the first user), in **Admin → Sign-in and teams** | no provider |
| Open sign-up through a provider | the instance administrator, same place | **off** (invite only) |
| Trust a provider's MFA | the instance administrator, per provider | **off** |

## Forgot password

The login page offers **Forgot your password?** when the instance can send email: it needs `NEBULACTRL_PUBLIC_URL` (the link in the email is absolute) and either the instance SMTP relay (**Admin → Email relay**) or an enabled email notification channel in one of the person's teams. The instance relay is tried first, then the person's teams in the order they joined them. Without that the login page says to ask a team admin, and the request endpoint answers `503 password_reset_unavailable`.

- **The link:** `…/reset-password?token=ncr_…`. It works **once** and expires after **30 minutes**. Only its SHA-256 is stored. Asking for another link does not cancel the first one, but using any link ends all the others of that account. A weak password (under 10 characters) is refused without spending the link.
- **Same answer for everyone:** `POST /api/v1/auth/password-reset/request` answers `202 {"status":"requested"}` whether or not the address belongs to an account, and the email is sent in the background so response time does not tell them apart. Accounts that sign in only through an identity provider (no password) get no email, and an admin cannot generate a link for them either (`409 no_password`).
- **Rate limits:** 10 requests per hour per client IP and 3 per hour per email address (answered `429` the same way for real and unknown addresses), and at most 3 emails per hour per account (further requests are dropped silently). Using or checking a link is limited to 10 attempts per minute per IP. The IP is the visitor's: see [Client addresses and limits](#client-addresses-and-rate-limits).
- **What the reset ends:** every browser session of the account is destroyed, including a sign-in waiting for its second factor, and every other open link of the account stops working. **API tokens are kept** unless the person ticks **Also revoke my API tokens** on the reset page (`revokeApiTokens`); the page then lists the tokens that still work so they can be reviewed in Account settings → API tokens. Two-step verification still applies at the next sign-in: a reset never bypasses it. The reset page checks its link when it opens (`POST /auth/password-reset/check`, which spends nothing).
- **Changing the password while signed in** (Account settings → Profile) ends every **other** session and keeps the current browser signed in under a new session token. API tokens are kept and listed afterwards, with **Also revoke my API tokens** to end them too. Wrong current passwords are limited to 10 per 10 minutes.
- **Admin-generated link:** an Owner or Admin opens **Team settings → Members → Reset password** (`POST /api/v1/teams/{teamId}/members/{userId}/password-reset`). The link is shown once and works without any SMTP setup. Admins cannot reset an Owner, nobody can generate one for themselves, and API tokens cannot generate links (a token must not be able to take over an account). **Only for people whose whole account is in this team:** a link for a member who belongs to any other team can be issued only by the instance admin (`409 member_of_other_teams`), and the instance admin's own password is reset on the server with the operator command below (`403 instance_admin_account`). The link signs in as that person, so a team must not be able to take over what its members hold elsewhere.
- **Operator link from the host:** the only Owner who is locked out (no SMTP, no other admin) runs `nebulactrl-server reset-password <email>` on the control-plane host. It prints the same one-time link (30 minutes, hash stored, no `created_by`), or with `--generate` sets a random password shown once, which also works for an account that has no password. It ends the account's sessions and audits `operator.password_reset_link` / `operator.password_set` as **system** actions (never the link or password). See [Troubleshooting → Locked out](../operations/troubleshooting.md#locked-out).
- **Audit:** `user.password_reset` (written in each of the person's teams, with the person as actor) and `member.password_reset_link` (the admin who generated one). Neither records the token. Requests for a link are not audited.

## Two-step verification (TOTP)

- Standard time-based one-time passwords: RFC 6238, HMAC-SHA1, 30-second steps, 6 digits, one step of clock-drift tolerance either way. Any authenticator app works (Google Authenticator, 1Password, Authy, Aegis …). There is no QR code: the setup screen shows the key to type or paste, and an `otpauth://` link for the phone's own app.
- **Enrol:** Account settings → Security → Two-step verification → Turn on. The secret is generated on the server and becomes active only after the first code verifies, so abandoning the dialog changes nothing. The secret is stored encrypted with the instance secret key.
- **Sign-in:** after a correct password the server answers `401 two_factor_required` and keeps the half-finished login in the session; nothing is signed in until the code is accepted (`POST /auth/2fa`). The pending login lasts 5 minutes and 5 wrong codes.
- **Replay and guessing:** an accepted time step can never be accepted again. Five wrong codes in a row lock the account's second-factor check for 5 minutes, from any session, in addition to the per-IP limit of 10 attempts a minute.
- **Recovery codes:** turning 2FA on issues **10 single-use codes** (`xxxx-xxxx-xxxx-xxxx`, 80 bits each). Only SHA-256 hashes are stored. Each is burned the moment it is used. They are shown once; Account settings → Security can replace all of them (that needs a current code). When three or fewer are left the page says so.
- **Turning it off** needs a current code (or recovery code) and, for accounts with a password, the password.
- **A lost phone and no recovery codes** cannot be fixed from the UI: there is deliberately no "reset my 2FA by email". An operator on the control-plane host clears it with `nebulactrl-server reset-2fa <email>` (it removes the authenticator app, passkeys and recovery codes after a typed confirmation, ends the account's sessions and audits `operator.2fa_reset` as a system action); see [Troubleshooting → Locked out](../operations/troubleshooting.md#locked-out).

## Passkeys (WebAuthn)

A passkey is a WebAuthn credential held by a device, a password manager or a security key. NebulaCtrl uses it two ways:

- **Passwordless sign-in.** The login page offers **Sign in with a passkey** (shown only where the browser supports WebAuthn). The browser lists the passkeys it holds for this site, the person verifies on the authenticator (fingerprint, face, PIN) and the session starts. Because the authenticator verified the person, the sign-in is already two factors: no code follows and the session counts as second-factor verified for "require 2FA".
- **Second factor.** After a correct password (or an identity-provider sign-in that is not trusted for MFA), an account with passkeys is asked for one on the second step (`401 two_factor_required`, then `POST /auth/2fa/passkey/options` and `POST /auth/2fa/passkey`). A code from an authenticator app or a recovery code still works from the same screen. `GET /auth/2fa` says which methods the pending login can use. The pending login lasts 5 minutes and 5 wrong answers, like the code step.

Rules:

- **Register several and name them** (Account settings → Security → Passkeys): a laptop, a phone, a security key. Up to 20 per account. They can be renamed and removed. The list shows whether each is **synced** (backed up by a platform password manager) or **this device only**, when it was added and last used.
- **What is required of the authenticator.** The credential is discoverable (a resident key, so passwordless sign-in works) and user verification is required at registration and at passwordless sign-in. Attestation is not requested (`none`): NebulaCtrl trusts any authenticator and records no hardware model. For the second step after a password, verification is "preferred", so a security key without a PIN still works.
- **Adding or removing** needs the **password** again when the account has one (so a stolen session cannot plant a passkey). The last passkey of an account that has no password and no linked identity cannot be removed (`409 last_sign_in_method`).
- **A passkey satisfies "require 2FA".** Members of a team that requires 2FA can enrol a passkey instead of an authenticator app, in the setup screen or in Account settings → Security. The team page counts them as enrolled.
- **Recovery codes still apply.** Adding the first passkey issues 10 recovery codes (shown once) when the account has no unused code left; they also exist for an account that has only passkeys. Account settings → Security can replace them: with a current code, or with the password for an account that has passkeys and no authenticator app. Removing the last passkey (with no authenticator app) deletes the codes.
- **Relying party.** The RP ID is the host of `NEBULACTRL_PUBLIC_URL` (without port) and the allowed origin is its scheme and host (and port). Without a public URL (development only) both come from the request. Passkeys are bound to that host: **changing the public hostname makes existing passkeys unusable** (they are not deleted; the sign-in falls back to password and a code). WebAuthn needs **HTTPS or `localhost`** and a **domain name**: on an install opened by IP address (`http://<ip>:8000`) the passkey screens say so (`422 passkeys_unavailable`) and the buttons are hidden or disabled.
- **Clone detection.** A passkey whose signature counter goes backwards is refused. Synced passkeys keep the counter at zero, which is accepted.
- **Limits.** Registration and sign-in ceremonies expire after 5 minutes and work once. The sign-in endpoints share the 10-per-minute-per-IP limit with the password login. The management endpoints need a browser session, never an API token or MCP.
- **Not yet:** conditional UI (autofilling a passkey in the email field), attestation or an allow-list of authenticator models, and an admin-initiated passkey reset from the dashboard (an operator on the host can run `nebulactrl-server reset-2fa <email>`, which also removes the account's passkeys).

## Requiring 2FA of a team

Team settings → General → **Require two-step verification** (Owners and Admins; `PUT /teams/{id}/security`).

- Members of that team who have not turned on 2FA see a setup screen instead of the team's pages, and the API answers `403 two_factor_enrollment_required` to everything except what enrolment needs: `/me*`, `/auth/*`, listing and switching teams, and invites. They can still switch to another team. **The requirement follows the team, not the session:** a team administration call for that team (members, invites, rename, delete, reset links) is refused the same way while the session acts in another team. The one exception is leaving the team (`DELETE /teams/{id}/members/{self}`).
- The person flipping the switch must already have 2FA (or a session verified by a trusted provider): `409 enrol_first`. This prevents locking the only administrator out.
- **API tokens follow their creator.** A token whose creator has not enrolled stops working in the team. The `/mcp` endpoint authenticates tokens separately and is **not** gated by this rule today.
- A session counts as verified when the user has TOTP or a passkey, or signed in through a provider that is trusted and asserted MFA (below).

## Single sign-on

Admin → **Sign-in and teams** (visible only to the instance administrator, who also gets a **Manage single sign-on** link in Account settings → Security). Two kinds of provider:

### OpenID Connect (any issuer with discovery)

Keycloak, Authentik, Okta, Microsoft Entra ID, Google, Auth0, Zitadel, Pocket ID, GitLab …

1. At the identity provider create a confidential **web** client for NebulaCtrl.
2. Register the **callback URL** NebulaCtrl shows (it is `https://<your public URL>/api/v1/auth/oidc/<provider id>/callback`). The dialog shows it **before you save**: it reserves the new provider's ID (the optional `id` of `POST /auth-providers`), so the address never changes between registering and saving. Set `NEBULACTRL_PUBLIC_URL` first: without it NebulaCtrl derives the URL from the request's `Host` header, which is only acceptable for local development.
3. In NebulaCtrl choose **Connect your identity provider**, pick the provider (or **Any OpenID Connect provider**): name (button text), **issuer URL** (the base of `/.well-known/openid-configuration`; the preset fills the pattern and highlights the `{placeholders}` to replace, and saving is blocked until none is left), client ID, client secret (encrypted; never shown again; leave empty on edit to keep it), optional extra scopes (`openid email profile` are always requested), optional allowed email domains. **Test connection** reads the issuer's discovery document without saving (`POST /instance/sign-in-providers/test`, instance admin only) and says what is wrong: unreachable host, untrusted certificate, no discovery document, an issuer that differs from the one entered, missing endpoints. Saving checks the same thing.

The flow is authorization code with **PKCE (S256)**, a per-attempt `state` and `nonce` bound to the browser session, and the ID token's signature, issuer, audience, expiry and nonce are verified against the issuer's keys. If the ID token carries no email, the userinfo endpoint is consulted.

### GitHub

Register an OAuth App on GitHub (or GitHub Enterprise Server: put its base URL in the provider's URL field), with the callback URL above, and add the client ID and secret. NebulaCtrl asks for `read:user user:email` and uses the **primary verified** email. A GitHub account's two-factor status cannot be verified, so "trust MFA" is never available for GitHub.

Step-by-step recipes for each provider (with the exact issuer format, console link and the field that takes the callback URL) are in [Single sign-on setup](sso-setup.md).

### How a person is matched to an account

1. By a previous link: the provider's stable subject (`sub`; numeric id for GitHub) is looked up first. An account created through the provider is linked from the start.
2. **Never by email.** A provider login whose verified email belongs to an existing account is refused with `account_exists`: it does not link and does not sign in. (An invite sign-up accepts any address, so matching on email would let whoever registered an address first share the account with its real owner.) The owner links the provider **themselves**, signed in: **Account settings → Security → Single sign-on → Link your SSO account**. That asks for the account's password again (`422 invalid_password`, 10 wrong ones per 10 minutes), sends the browser to the provider, and attaches the identity the person signs in with there to the account (`POST /me/identities/link`, then the usual callback). The same rules apply as at sign-in: a verified email, the provider's allowed domains, and an identity already linked to another account is refused (`identity_conflict`). **Unlink** is in the same card (never the last way in). A provider-asserted email that is *not* marked verified never creates an account either. "Verified" means the ID token's `email_verified` is true, or, for Microsoft Entra ID, which never sends that claim, the optional claim `xms_edov` (the tenant admin has verified the email's domain) is true.
3. Otherwise a new account is created, but **only** when
   - the person presents a team **invite** (the invite link carries a token through the sign-in round trip; an invite bound to an email must match the provider's verified email), or
   - the instance administrator turned on **Allow open sign-up** (off by default); the new user then gets a team through the teams module.
   Otherwise sign-in ends with `signup_disabled`.
4. **Allowed email domains** on the provider apply to every sign-in and sign-up through it; an account outside the list is refused with `domain_not_allowed`.

Accounts created through a provider have no password (the stored hash is empty and matches nothing), so the provider is their only way in; there is no "set a password" screen for them yet. For that reason the last sign-in method of such an account cannot be unlinked.

### Two-step verification after single sign-on

By default a person who has TOTP enabled still gets NebulaCtrl's code step after the provider signs them in (the provider's own MFA is not verifiable by NebulaCtrl). Per provider you can switch on **Trust the provider's MFA**: when the ID token's `amr` claim contains `mfa`, `otp` or `hwk`, the code step is skipped and the session counts as verified for team requirements. Only enable it for an issuer that enforces MFA and sets `amr` honestly. It is off by default and unavailable for GitHub.

## What this does not do

- **No QR codes, SMS or email codes.** Authenticator-app codes and passkeys only.
- **No SCIM, group-to-role mapping or just-in-time role assignment from claims.** Roles are assigned in the team.
- **No admin-initiated 2FA reset from the dashboard** (an operator on the host has `nebulactrl-server reset-2fa`, see the lost-phone note above).
- **The `/mcp` endpoint is not subject to the team 2FA requirement** (tokens authenticate there without the gate).
- **Logging out of NebulaCtrl does not log out of the provider**, and a provider session ending does not end a NebulaCtrl session (sessions last up to 30 days, 7 idle).

## Client addresses and rate limits

The sign-in, second-step, passkey, provider-start, setup, reset and invite limits are keyed on the **visitor's address**, which the control plane works out like this (the same rules the public status pages use):

- The TCP peer is normally the control-plane host's Traefik, so its forwarding headers are read **only from a loopback, private or link-local peer**. A peer on a public address reached the control plane directly, can write any header, and is keyed on its own address.
- From the proxy, the address is the **last** `X-Forwarded-For` entry (the address Traefik saw connect); entries to its left are whatever the visitor sent and are never read. `X-Real-IP` is used only when there is no `X-Forwarded-For`.
- When that last entry is one of Cloudflare's ranges (the instance's current list), the visitor is `CF-Connecting-IP`, which Cloudflare sets itself; without it, the entry before the edge address. `CF-Connecting-IP` from any other peer is ignored.

Besides the per-address limits, a password login is limited **per account**: 10 wrong passwords per 10 minutes (`429`, whatever the addresses), so a spray from many addresses is slowed too. Only wrong passwords count, and the same limit covers a wrong current password when changing it.

## Audit and storage

Audited, in the same transaction as the change: `auth.passkey.add`, `auth.passkey.rename`, `auth.passkey.remove` (name and id only, never key material), `auth.2fa.enable`, `auth.2fa.disable`, `auth.recovery_codes.regenerate`, `auth.identity.link`, `auth.identity.unlink`, `user.password_change` (whether the tokens were revoked, and how many), `auth.provider.create|update|delete` (never the secret; `secretChanged` only), `auth.settings.update`, `team.require_2fa`. Tables: `user_passkeys` (credential id, public key and sign counter as JSON, name; no secret), `user_totp` (sealed secret, last accepted step, failure counter), `user_recovery_codes` (hashes), `auth_providers` (sealed client secret), `user_identities`, `auth_settings`, and `workspaces.require_two_factor`.
