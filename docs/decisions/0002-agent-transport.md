---
title: 0002 Agent transport
description: "Why agents dial out over WebSocket with an Ed25519 identity."
section: Internals
order: 2
---

# 0002. Agent transport: outbound WebSocket, Ed25519 identity

- Status: accepted
- Date: 2026-10-25

## Context

The agent connection should be agent-initiated and strongly authenticated, with explicit identity, revocation, replay protection, protocol versioning, durable commands and honest handling of uncertain state. Homelab hosts are often behind NAT, so the control plane cannot dial them. The open choice was a gRPC stream versus WebSocket over TLS.

## Decision

**Transport.** The agent always dials outbound to `wss://<control-plane>/agent/v1/connect` using `github.com/coder/websocket`. A plain `ws://` URL is refused unless the agent runs with `--insecure-dev`. One socket per server carries commands, results, heartbeats, events, stats and log streams in both directions. A newer connection from the same server replaces the older one.

**Enrollment.**
1. The UI creates a one-time token `nct_enroll_<32 random bytes, base32>`. Only its SHA-256 hash is stored; it expires after 1 hour, is single-use and revocable. The server row is created at this point with status `pending`.
2. The agent generates an Ed25519 keypair and calls `POST /agent/v1/enroll {token, publicKey, hostname, facts}`; the response is `{serverId, agentId}`.
3. The agent stores its identity in bbolt at `<state-dir>/agent.db` (mode 0600). The `--token` is used only while no identity is stored.

**Connect handshake.**
1. The server sends `challenge{nonce, serverTime}`.
2. The agent replies `auth{agentId, signature}` where the signature covers `nonce || agentId || serverTime`.
3. The server verifies the signature against the stored public key, rejects a nonce older than 30 seconds (replay protection), and rejects any identity with `revoked_at` set. Then the agent sends `hello`.
4. A revoked agent is closed with status **4401**; the agent exits non-zero with "agent revoked; re-enroll". An unsupported protocol major version is closed with **4400**.

**Envelope.** Every frame is JSON `{"v":1,"type":…,"id":…,…}`. `pkg/agentprotocol` exports `ProtocolVersion = 1`. Agent to server: `hello`, `heartbeat` (every 10 s), `ack`, `result`, `event`, `log`, `log_end`, `stats`, `observed`. Server to agent: `challenge`, `command`, `result_ack`, `reconcile`. Command kinds are a closed set: `deploy`, `restart_service`, `stop_service`, `remove_service`, `sync_routes`, `logs_subscribe`, `logs_unsubscribe`. The agent rejects anything else; it never runs a shell string from the wire.

**Command durability and uncertainty.**
- The control plane inserts an `agent_commands` row (status `pending`) **before** sending, with a unique idempotency key and an `expires_at` 15 minutes ahead. It sends immediately when the server is online and on every connect.
- The agent ACKs on receipt and records the command ID in bbolt. A redelivered command returns the stored result instead of running again, which makes commands idempotent.
- The agent keeps each result until it receives `result_ack` and resends unacknowledged results in `hello.unackedResults` after a reconnect.
- A periodic job moves `pending`/`sent` commands past `expires_at` to `expired`; the deployment then fails with "Not delivered: server offline. No changes were made." This is safe because the agent never saw the command.
- **Acked commands never time out to a terminal state.** After an ACK the outcome is genuinely unknown while the agent is offline. The deployment stays active and the UI shows "Waiting for <server> (offline since …)" until the result arrives or an operator revokes the server.
- Secrets travel only inside the outgoing frame (`secrets`), attached at send time and never persisted in `agent_commands.payload`.

**Liveness.** A server is `offline` after 30 s without a heartbeat; observations older than 30 s render as stale. On `hello` the control plane sends `reconcile` with the active deployments for that server.

## Consequences

- Works behind NAT and strict firewalls: nothing listens on the agent host for the control plane. Log streaming and commands share one authenticated, encrypted connection.
- Agent identity is a keypair rather than a bearer secret; compromise of the control-plane database does not yield agent credentials, and revocation is immediate (close 4401).
- Authentication happens at the application layer inside TLS, not mTLS. It gives equivalent identity properties without managing a CA; certificate rotation is replaced by re-enrollment.
- The control plane must keep one live socket per server in process memory (see ADR 0001 on single-instance scope).
- The protocol is hand-rolled JSON over WebSocket, so versioning and frame compatibility are our responsibility. The `v` field and close code 4400 are the compatibility gate.

## Alternatives considered

- **gRPC bidirectional stream**: strong typing and flow control, but heavier toolchain, more friction through HTTP proxies, and no extra capability needed for this message set.
- **Control plane dials agents (SSH or HTTPS)**: needs inbound ports on every host; breaks homelab NAT; SSH is a general shell, contradicting the closed command set.
- **Polling agent over HTTPS**: simple, but no push for logs and higher command latency.
- **mTLS with a private CA**: rejected to avoid certificate lifecycle code; the Ed25519 challenge provides identity, replay protection and revocation.
