---
title: Web terminal and file browser
description: "Open a shell or browse files inside a running container."
section: Applications
order: 120
---

# Web terminal and file browser

A shell and a file browser inside a running replica container, from the browser
(inspector header ⋯, the canvas dock's **More** menu, ⌘K) and from the CLI.
Nothing is installed in your image: the agent starts the shell with Docker's
exec API, over the connection the agent already holds to the control plane.

```
browser ⇄ WSS ⇄ control plane ⇄ agent WebSocket ⇄ Docker exec / archive API ⇄ container
```

This is a shell **inside a container**. A shell on the server itself is a separate,
opt-in feature with stricter rules: see the [host shell](../security/host-shell.md).

## Who can use it

- **Owner, Admin and Developer** of the team. **Viewers never see it** (no
  terminal, no files). A per-project restriction on the membership applies:
  outside it the service does not exist (404).
- An API token needs the `deployments:write` scope **and** its creator must still
  hold one of those roles. Browser sessions and tokens are treated the same.
- **Production** environments need an explicit confirmation: the UI asks with a
  hold-to-confirm dialog, and the server refuses the session unless the open
  message says `confirmProduction: true`. A script or the CLI must pass it
  deliberately (`nebulactrl services shell --production`).

## The terminal

- The shell is `bash` when the image has it, else `sh`. Pick another command in
  the dialog (for example `psql -U postgres`); it runs as an argv, with the
  container's own user and working directory, and `TERM=xterm-256color`.
- Choose the **replica** when the service runs several. A stopped replica cannot
  be opened.
- The terminal resizes with its panel, copies and pastes like any terminal, and
  shows the exit code when the shell ends.
- **Limits.** 3 concurrent sessions per person (or token), shared across
  services. A session with **no keyboard input for 15 minutes** is closed (a
  warning appears one minute before). Closing the browser tab ends the session
  and sends SIGHUP to the shell; a process that ignores SIGHUP (`nohup`) keeps
  running in the container.
- Output is flow controlled: a runaway `cat /dev/urandom` slows down instead of
  filling queues that also carry heartbeats and deployment events.

### What is recorded

| Event | Audit action | Metadata |
|---|---|---|
| a session opens | `terminal.open` | service, replica, container, **the executable only** (never its arguments, which may carry a password), production flag, server |
| it ends | `terminal.close` | duration in seconds, why it ended (`exit`, `client_closed`, `idle`, `agent_disconnected`, `error`), exit code |

**Keystrokes and output are never recorded, logged or stored**, anywhere. The
audit entry for a session is written before the shell starts: if the audit log is
unavailable the session is not opened.

Be aware that a shell in a container sees that container's environment,
including secret variables, and can change the container's filesystem. The
changes disappear on the next deployment, which creates a fresh container. Treat
terminal access like SSH access to that container.

In a service with [processes](processes.md) pick the process first, then the replica (`?process=` in the API, `services shell --process`). Without a process the default process is used.

## The file browser

- Lists directories (directories first, up to 2000 entries), previews text files
  up to 1 MiB, downloads files of any size (streamed, never buffered whole) and
  uploads **one file at a time, up to 50 MB**.
- Paths are absolute paths **inside the container**. The agent reads and writes
  through the Docker API, so the host filesystem is out of reach; symlinks are
  resolved inside the container's root.
- Listings use the image's `sh` (`stat`/`readlink` when present). Images without
  a shell (distroless, `scratch`) fall back to reading the directory's archive,
  which is slower on big trees and may report the listing as truncated.
- Uploaded files are created with mode `0644` and owned by root. Use the
  terminal to `chown` them when the app runs as another user.
- **Production is read-only.** An Owner or Admin can allow writes per project
  (project settings → Terminal & files, or
  `PUT /api/v1/projects/{id}/terminal-settings {"allowProductionFileWrites": true}`).
  Reads and downloads always work for the roles above.
- Every download or preview is audited as `file.read` (path, size), every upload
  as `file.upload` (path, size). File contents are never recorded.
- Volumes: files written into a mounted volume persist, files elsewhere vanish
  with the container on the next deployment. The browser shows both without
  telling them apart; check the service's volume list.

## API

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/services/{id}/terminal` | availability, replicas, production flag, whether file writes are allowed |
| `GET /api/v1/services/{id}/terminal/ws` | WebSocket (browser session, same origin). First message `{"type":"open","replica":0,"command":[…],"cols":80,"rows":24,"confirmProduction":false}`; binary frames are input/output; `{"type":"resize"}`, `{"type":"close"}`; the server sends `opened`, `idle_warning`, `exit`, `error`, `closed` |
| `GET /api/v1/services/{id}/files?replica=&path=` | list a directory |
| `GET /api/v1/services/{id}/files/content?replica=&path=&preview=` | download or preview a file |
| `POST /api/v1/services/{id}/files/upload?replica=&path=<dir>&name=<file>` | upload the request body (`Content-Length` required) |
| `GET/PUT /api/v1/projects/{id}/terminal-settings` | the production-writes switch (admins) |

A cross-origin page cannot open a terminal with your session cookie: the WebSocket
handshake checks the `Origin` header against the request host and
`NEBULACTRL_PUBLIC_URL`. Behind a reverse proxy, forward `Upgrade` and
`Connection` headers and the original `Host`, and allow long-lived connections
(an idle terminal sends a ping every 30 seconds).

## CLI

```
nebulactrl services shell <service> [--replica 0] [--production] [-- command args…]
```

Needs an interactive terminal on both ends. It opens the same WebSocket with
your token, puts your terminal in raw mode, forwards resizes and restores the
terminal when the session ends. The exit code is the shell's.

## Troubleshooting

| Symptom | Cause |
|---|---|
| "The server is offline" | the agent is not connected; terminals need the live connection and are never queued |
| "Replica N is not running" | the container exited or restarts in a loop; check the logs, then redeploy |
| "could not start a shell: … executable file not found" | the image has no `sh`. Open a command that exists (for example `/app/server`) or debug with the file browser |
| "You already have 3 terminal sessions open" | close one; sessions are per person across all services |
| "production" confirmation fails | the server did not receive `confirmProduction: true` (stale browser tab: reload) |
| the terminal drops after minutes behind a proxy | the proxy's idle timeout is shorter than the 30 s ping interval allows; raise it for `/api/v1/services/*/terminal/ws` |
