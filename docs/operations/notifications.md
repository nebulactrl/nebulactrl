---
title: Notifications
description: "Email, Slack, Discord, Telegram and signed webhooks for deploy and server events."
section: Observability
order: 40
---

# Notifications

NebulaCtrl tells you when something needs attention: a failed or rolled-back deployment, a crashed, unhealthy or restart-looping service, an offline server, a full disk, a failed or missed backup, built-in backup storage that is nearly full, a failed task, a blocked scale-out, an expiring certificate. **Team settings → Notifications** manages the channels; every channel has its own rule for which events it receives.

All endpoints and the UI need the `admin` scope (channels hold credentials, and the delivery log shows events of every project).

## Channels

| Kind | You provide | Notes |
|---|---|---|
| Email | SMTP host, port, connection (`starttls` default 587, `implicit` TLS 465, `none` 25), user name and password (optional), From, recipients | Standard library SMTP. `none` sends clear text: trusted relays only. "Accept a self-signed certificate" keeps encryption but skips identity checks. Multipart message: plain text plus HTML in the NebulaCtrl palette. |
| Slack | Incoming-webhook URL | Block Kit message with a coloured attachment, field grid and an "Open in NebulaCtrl" button. `<`, `>`, `&` are escaped so event text cannot ping `@channel`. |
| Discord | Webhook URL | One embed; `allowed_mentions` is empty so event text cannot ping anyone. |
| Telegram | Bot token, chat id or `@channelname` | `sendMessage` with MarkdownV2; every reserved character is escaped; text is kept under Telegram's 4096 limit. |
| Webhook | URL, signing secret (generated if omitted) | JSON body, signed (below). |

Webhook URLs, bot tokens, SMTP passwords and signing secrets are sealed with `NEBULACTRL_SECRET_KEY` (same box as variables), **never returned by any read** (the API shows a masked hint such as `https://hooks.slack.com/••••••`), never written to the audit log (which records "secrets changed", not values), and scrubbed from stored delivery errors and logs. Editing a channel with an empty credential field keeps the stored value. Losing the secret key makes stored credentials undecryptable: deliveries then fail with a clear message and you re-enter them.

Receivers on LAN addresses and `localhost` are allowed (homelab services live there). Link-local addresses, which hold the cloud metadata services (`169.254.0.0/16`, `fe80::/10`, `fd00:ec2::254`), are refused. Redirects are not followed.

## Rules

A channel subscribes to **event types**, with a **minimum severity** (`info`, `warning`, `error`) and a **scope**: the whole workspace, or selected projects. Server events (offline, disk, certificates) belong to no project, so only whole-workspace channels receive them.

A new channel starts with failures and alerts on and successes off:

| On by default | Off by default |
|---|---|
| `deploy.failed`, `deploy.rolled_back`, `server.offline`, `server.disk_alert`, `server.cleanup`, `server.reboot_required`, `backup.failed`, `backup.missed`, `storage.quota_warning`, `storage.quota_full`, `task.failed`, `scale.blocked`, `cert.expiring`, `dns.sync_failed`, `cloudflare.ips_stale`, `service.crashed`, `service.unhealthy`, `service.restart_loop`, `service.recovered`, `uptime.down`, `uptime.up` | `deploy.succeeded`, `server.online`, `backup.succeeded`, `preview.created`, `preview.deleted`, `maintenance.on`, `maintenance.off`, `cloudflare.ips_changed` |

`GET /api/v1/notifications/event-types` returns the catalogue (label, group, default severity, default on/off).

Where events come from: a deployment reaching `succeeded`, `failed`/`rollback_failed` (both are `deploy.failed`) or `rolled_back` (cancellations are not announced); a server going offline (30 s without heartbeat), connecting or coming back, and a disk crossing its alert threshold (once per crossing); a server's [Docker cleanup](docker-cleanup.md) that failed (error) or freed more than 1 GB (info), as `server.cleanup`; a database backup or restore finishing (a failed restore and a skipped scheduled backup are `backup.failed`, the latter as a warning); task failures (a failed post-deploy command is a `task.failed` warning; the deployment still succeeded); preview created/deleted; the autoscaler being blocked (once per block); a certificate crossing 30 days, 7 days or expiry (once per threshold); a Cloudflare DNS record that could not be updated, or a Cloudflare token that was rejected (`dns.sync_failed`, once per failure and at most every six hours; see [Cloudflare](cloudflare.md)); maintenance mode switched on or off.

Service health and backups: a crash, a failing health check or a restart loop is announced once per incident (`service.crashed`, `service.unhealthy`, `service.restart_loop`) and closed with one `service.recovered`; a flapping service is rate-limited; a scheduled backup that did not succeed within its window is `backup.missed`, once per streak. Thresholds are set in **Team settings → Notifications → Service alerts**. See [Service alerts](service-alerts.md).

Built-in backup storage: when a team's use of its [built-in backup storage](platform-services.md#quotas) reaches **80%** of its quota, one `storage.quota_warning` warning ("Built-in backup storage nearly full") is sent, and at **100%** one `storage.quota_full` error ("Built-in backup storage full"); each is sent once per crossing and re-arms when use falls below 75%. The team gets them, and so does the instance admin (the copy goes to the control-plane host's team). New backups fail while the storage is full; existing ones are kept.

Uptime: when the probes that run from each server (every 30 seconds) find a service down several times in a row, one `uptime.down` error is sent (and, if the monitor sets a reminder interval, again every that many minutes while the outage lasts); when it passes again, one `uptime.up` closes it, with the outage's duration. Maintenance and deployments never alert. A monitor's `alerts` switch turns all of it off for that service. See [Uptime monitoring](uptime.md).

Server patching: when a server's host starts needing a reboot (the first [check](server-patching.md) that sees the flag, or an upgrade that sets it), one `server.reboot_required` warning is sent; it is not repeated while the flag stays set, and a new streak is announced again. Pending updates alone send nothing.

## Delivery

Delivery is asynchronous: the event is recorded per matching channel and a River job sends it, so a slow or broken channel never delays a deployment.

- **Retries:** up to 6 attempts, 1, 3, 8, 15 and 30 minutes apart (about an hour). A permanent failure is not retried: HTTP 4xx other than 408/425/429, SMTP 5xx (bad password, unknown recipient), STARTTLS demanded but not offered, undecryptable credentials.
- **Rate limit:** per channel, a burst of 10 then one message every 3 seconds. Over the limit a delivery waits (it does not use an attempt and is not dropped).
- **Deduplication:** an identical event (same type, subjects, text and fields) is not sent again to the same channel within 5 minutes.
- **Delivery log:** per channel, 30 days: status (`pending`, `retrying`, `delivered`, `failed`), attempts, response code (HTTP status or SMTP reply), last error, next attempt. Open it with **Delivery log** on the card or `GET /api/v1/notifications/channels/{id}/deliveries?status=&cursor=`.
- **Test send:** `POST /api/v1/notifications/channels/{id}/test` (the **Send test** button) sends now, bypassing the queue, retries, rate limit and dedupe, and returns the delivery with the result. It works on a paused channel and updates the card's "last test".

## Signed webhooks

Every request carries:

```
X-NebulaCtrl-Signature: t=1793023655,v1=<hex>
X-NebulaCtrl-Event: deploy.failed
X-NebulaCtrl-Delivery: <delivery id>
```

`v1` is the hex HMAC-SHA256, keyed with the channel's signing secret, of `"<t>." + raw request body`. Verify the **raw bytes**, compare in constant time and reject timestamps more than five minutes off. The body:

```json
{"id":"…","type":"deploy.failed","severity":"error","title":"api failed to deploy","body":"…",
 "url":"https://ctrl.example.com/p/…","fields":{"Project":"Shop","Service":"api"},
 "projectId":"…","serverId":"","serviceId":"…","timestamp":"2026-10-26T14:30:05Z","test":false}
```

Node.js:

```js
import { createHmac, timingSafeEqual } from "node:crypto";
export function verify(secret, header, rawBody, toleranceSec = 300) {
  const parts = Object.fromEntries(header.split(",").map((p) => p.split("=", 2)));
  const t = Number(parts.t);
  if (!parts.v1 || !Number.isFinite(t)) return false;
  if (Math.abs(Date.now() / 1000 - t) > toleranceSec) return false;
  const expected = createHmac("sha256", secret).update(`${t}.`).update(rawBody).digest("hex");
  const a = Buffer.from(expected), b = Buffer.from(parts.v1);
  return a.length === b.length && timingSafeEqual(a, b);
}
```

Python:

```python
import hashlib, hmac, time
def verify(secret, header, raw_body, tolerance=300):
    parts = dict(p.split("=", 1) for p in header.split(","))
    try:
        t = int(parts["t"])
    except (KeyError, ValueError):
        return False
    if abs(time.time() - t) > tolerance:
        return False
    expected = hmac.new(secret.encode(), f"{t}.".encode() + raw_body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, parts.get("v1", ""))
```

Go: `notifications.VerifySignature(secret, header, body, now, tolerance)` (`internal/notifications/webhook.go`) implements the same check; the UI shows Go, Node.js and Python snippets.

The signing secret is shown **once**, on creation (when generated) and when rotated (`POST …/rotate-secret`, **Rotate secret** on the card). After a rotation the old secret stops signing immediately: update the receiver first.

## For other domains

Code that wants to announce something calls `notifications.Emit(ctx, emitter, notifications.Event{…})` (`internal/notifications/emit.go`). It is nil-safe, never fails and never blocks. Fill the type, the project/service/server ids and a title and body; names, the environment and the link are looked up. A `URL` starting with `?` is added to the service link (`?tab=deployments`). Titles and bodies go to third parties: no secrets, no log excerpts.

## Troubleshooting

- **Test fails with `HTTP 404: no_service` / `invalid_token`:** the Slack/Discord URL or Telegram token is wrong or revoked; edit the channel and paste a new one.
- **Email `authentication failed` (535):** wrong user/password, or the relay wants an app password. `the server does not offer STARTTLS`: choose `TLS` (465) or `None` for a trusted relay.
- **Nothing arrives, log is empty:** the event type or severity is not in the rule, the channel is paused, or (project-scoped channels) the event belongs to no project.
- **`address is link-local or a cloud metadata service`:** the target resolves to a blocked address.
