---
title: Single sign-on setup
description: "Connect your OpenID Connect provider, with a recipe for common ones."
section: Teams and security
order: 30
---

# Single sign-on setup

Single sign-on is **bring your own**: you connect an OpenID Connect provider you already use (or GitHub). NebulaCtrl does not run an identity provider for you and has none built in. If you have none, deploy one from the [template gallery](../operations/templates.md) (Pocket ID and authentik are in it) like any other app, then connect it here as an external provider.

Where: **Admin → Sign-in and teams** (instance administrator only; Account settings → Security links to it). What it does and does not protect is in [login methods](login-methods.md); this page is what NebulaCtrl needs from a provider and the recipe per provider.

Provider facts below come from each provider's documentation; the source is linked on each entry. They change: if a console has moved, the issuer and the redirect-URI rules still hold.

## What NebulaCtrl needs from a provider

| | |
|---|---|
| Callback URL | `https://<your public URL>/api/v1/auth/oidc/<provider id>/callback`. The provider calls it the redirect URI, callback URL or sign-in redirect URI. The dialog shows it with a Copy button **before you save**. |
| Client | A **confidential web client** (it has a client secret) using the authorization-code flow with **PKCE (S256)**. NebulaCtrl sends a random `state` and `nonce` and checks both. |
| Scopes | `openid`, `email` and `profile` are always requested. Extra scopes you enter under **Scopes** in the dialog are added to them (for example `groups` or `offline_access`); nothing else is asked for. A GitHub provider requests `read:user` and `user:email`. |
| Claims | `email` with `email_verified: true`, in the ID token or, when the ID token has no email, from the userinfo endpoint. Microsoft Entra ID has no `email_verified`; its `xms_edov` claim counts instead. `name` (else `preferred_username`) becomes the display name of a new account. `amr` is read only when **Trust the provider's MFA** is on ([login methods](login-methods.md)). |
| Issuer | The base address that serves `/.well-known/openid-configuration`, **identical** to the `issuer` the document states (including any trailing slash). |
| Reachability | The control plane itself calls the issuer, the token endpoint and the key set: they must be reachable from it over HTTPS with a certificate it trusts. The browser needs to reach the provider too; the provider does not need to reach NebulaCtrl, except to redirect the browser to the callback. |

## The same five steps everywhere

1. In NebulaCtrl click **Connect your identity provider** and pick the provider. The dialog fills in the name and the issuer pattern. Parts in `{braces}` are yours to replace (the first one is selected, so type over it); **saving is blocked while one is left**.
2. Copy the **Redirect URI** the dialog shows. It is `https://<your public URL>/api/v1/auth/oidc/<provider id>/callback` and you can register it **before you save**: the dialog reserves the new provider's ID, so the address does not change when you save. Set `NEBULACTRL_PUBLIC_URL` first; without it NebulaCtrl uses the address your browser is on, which is only right for local development.
3. At the provider create a **confidential web client** (it has a client secret), paste the redirect URI into the field named below, and copy the client ID and secret.
4. Back in NebulaCtrl paste the client ID and secret, and click **Test connection**. It reads `<issuer>/.well-known/openid-configuration` without saving anything and answers in a sentence:

   | Answer | Meaning |
   |---|---|
   | The host name does not resolve | The domain in the issuer is misspelt. |
   | Could not connect to … | Nothing answers there from the control plane (firewall, wrong port, private address). |
   | … did not answer in time | Same, slower. |
   | … presented a TLS certificate this server does not trust | A private CA or a self-signed certificate; the control plane does not accept it. |
   | … answered 404 for /.well-known/openid-configuration | The issuer is not the base address of an OpenID Connect provider. |
   | … did not return a JSON OpenID configuration | You entered a login page or a proxy answered. |
   | The provider says its issuer is "…", but you entered "…" | The issuer must be **identical**, including any trailing slash. Copy the value it quotes. |
   | The OpenID configuration has no … | The provider is not usable for sign-in (missing endpoints or key set). |

5. Set **Allowed email domains** (an empty list lets **any** verified email through), save, and sign in from a private window to try it. People are matched to accounts by verified email; a person with no account needs an invite or **Allow open sign-up** ([login methods](login-methods.md#how-a-person-is-matched-to-an-account)).

## Providers

### Google

- Issuer: `https://accounts.google.com`
- Console: [Clients page](https://console.developers.google.com/auth/clients) → Create client → **Web application**. Field: **Authorized redirect URIs**.
- Any Google account can reach the sign-in; put your Workspace domain under allowed email domains.
- Source: <https://developers.google.com/identity/openid-connect/openid-connect>

### Microsoft Entra ID

- Issuer: `https://login.microsoftonline.com/{tenant}/v2.0`, with `{tenant}` your **Directory (tenant) ID** (a GUID, on the app's Overview page). Not `common`, `organizations` or `consumers`: their discovery document reports a tenant-specific issuer, so the exact-match check fails (NebulaCtrl warns about it).
- Console: [Entra admin center](https://entra.microsoft.com) → Entra ID → App registrations → New registration. Add a **Web** platform; field: **Redirect URIs**. Create a client secret under Certificates & secrets.
- Entra never sends `email_verified`. Under **Token configuration → Add optional claim → ID** add `email` and `xms_edov` ("the email's domain owner is verified"). NebulaCtrl treats a true `xms_edov` as a verified email; without it every sign-in ends with `email_not_verified`.
- Sources: <https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc>, <https://learn.microsoft.com/en-us/entra/identity-platform/optional-claims-reference>

### Okta

- Issuer: `https://{domain}/oauth2/default` (the pre-configured custom authorization server; `{domain}` is your Okta domain, for example `dev-123456.okta.com`). To use the org authorization server instead, the issuer is just `https://{domain}`.
- Admin Console → Applications → Applications → **Create App Integration** → OIDC → **Web Application**. Field: **Sign-in redirect URIs**.
- Sources: <https://developer.okta.com/docs/concepts/auth-servers/>, <https://help.okta.com/oie/en-us/content/topics/apps/apps_app_integration_wizard_oidc.htm>

### Auth0

- Issuer: `https://{domain}/` with `{domain}` the tenant domain from the application's Settings (or your custom domain). **Keep the trailing slash**: Auth0 reports its issuer with one.
- [Dashboard → Applications](https://manage.auth0.com/#/applications) → Create Application → **Regular Web Applications**. Field: **Allowed Callback URLs**.
- Source: <https://auth0.com/docs/get-started/applications/application-settings>

### authentik

- Issuer: `https://{domain}/application/o/{slug}/`, per application, **with the trailing slash** (the default issuer mode).
- Admin interface → Applications → Applications → New Application, with an OAuth2/OpenID Provider. Field: **Redirect URIs** (an exact URL is fine; authentik also accepts regular expressions).
- Source: <https://docs.goauthentik.io/add-secure-apps/providers/oauth2/>

### Keycloak

- Issuer: `https://{domain}/realms/{realm}`. A Keycloak older than 17, or one started with a relative path, serves it under `/auth/realms/{realm}`.
- In the realm: Clients → Create client → OpenID Connect, **Client authentication on**, standard flow on. Field: **Valid redirect URIs** (compared as an exact string).
- Source: <https://www.keycloak.org/securing-apps/oidc-layers>

### Zitadel

- Issuer: `https://{domain}`, your instance domain (for example `my-instance-abc123.zitadel.cloud`, or a custom domain), no path.
- Console → a project → New application → **Web**, authentication method **Code** (client ID and secret). Field: **Redirect URIs**.
- Sources: <https://zitadel.com/docs/apis/openidoauth/endpoints>, <https://zitadel.com/docs/guides/manage/console/applications>

### Pocket ID

- Issuer: `https://{domain}`, the address you open Pocket ID at, no path.
- Add an OIDC client in Pocket ID and paste the redirect URI under **Callback URLs**. The `email` scope returns `email` and `email_verified`.
- Sources: <https://pocket-id.org/docs/client-examples>, <https://pocket-id.org/docs/guides/scopes-and-claims>

### GitLab (gitlab.com or self-managed)

- Issuer: `https://gitlab.com`, or the base URL of your GitLab.
- Profile → Edit profile → Access → [Applications](https://gitlab.com/-/user_settings/applications) → Add new application, **Confidential** on, scopes **openid, profile, email**. Field: **Redirect URI**.
- Sources: <https://docs.gitlab.com/integration/openid_connect_provider/>, <https://docs.gitlab.com/integration/oauth_provider/>

### GitHub (OAuth, not OpenID Connect)

- There is nothing to discover, so no **Test connection** and no issuer: leave the address empty for github.com, or enter the base URL of GitHub Enterprise Server.
- Settings → [Developer settings](https://github.com/settings/developers) → OAuth Apps → New OAuth App. Field: **Authorization callback URL**.
- Only the account's primary **verified** email signs in, and GitHub's own 2FA cannot be verified, so "Trust the provider's MFA" is never offered.
- Source: <https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app>

### Any other OpenID Connect provider

Pick **Any OpenID Connect provider** and enter the issuer exactly as the provider's discovery document states it (<https://openid.net/specs/openid-connect-discovery-1_0.html>: the issuer in the document must be identical to the one used to fetch it). Requirements: a confidential web client, the authorization-code flow with PKCE, and an ID token that carries `email` and `email_verified: true` (or the userinfo endpoint does).

## If sign-in fails afterwards

The sign-in page shows the code; the control-plane log has the detail.

| Code | Usual cause |
|---|---|
| `oidc_failed` | The provider is disabled or its discovery document is no longer reachable. Run **Test connection** from Edit. |
| `oidc_exchange_failed` | Wrong client secret, or the redirect URI at the provider differs from the one NebulaCtrl sent. |
| `oidc_invalid_token` | The ID token's audience, issuer or nonce did not check out (a wrong client ID). |
| `email_not_verified` | The provider sent no verified email (Entra: add `xms_edov`; GitHub: verify the primary email). |
| `domain_not_allowed` | The email's domain is not in the provider's allowed list. |
| `signup_disabled` | The person has no account and no invite; turn on **Allow open sign-up** or invite them. |
