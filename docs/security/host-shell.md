---
title: Host shell
description: "An opt-in root shell on a server, for owners and admins with two-factor."
section: Servers
order: 100
---

# Host shell

An interactive shell **on the server itself**, from the browser (server page →
Settings → **Host shell**). It is not the [web terminal](../operations/terminal.md), which
runs inside one service's container: the host shell sits outside every
container, as the agent's user (root on a normal install). From it you can read
every volume, every container's environment and the Docker daemon's socket.
Treat it like SSH as root.

It is **off by default and opt-in per server**.

```
browser ⇄ WSS ⇄ control plane ⇄ agent WebSocket ⇄ a pty on the host (or a helper container)
```

## What has to be true before a shell opens

| Check | Refusal |
|---|---|
| An Owner or Admin **switched it on** for this server (**Server → Settings → Host shell → Allow a shell on this server**; audited as `server.shell.enable`) | `403 host_shell_disabled` |
| The person is an **Owner or Admin** of the team, with a signed-in browser session. Developers and Viewers never get it; an **API token or MCP principal never gets it**, whatever its scopes | `403 forbidden` / `403 session_required` |
| Their account has **two-factor authentication** (Account settings → Security) | `403 two_factor_required` |
| On the server the **control plane itself runs on**, the person is the **instance admin**. Team Owners and Admins are refused, both for the switch and for the shell | `403 instance_admin_required` |
| The server is online | `server_offline` |
| **Production**: a server that runs any service of a production environment needs the guarded confirm in the UI (hold to confirm) and `confirmProduction: true` in the open message | `confirmation_required` |
| At most **2** open host shells per person | `too_many_sessions` |

The first three are answered over plain HTTP before the WebSocket upgrade, so a
script sees the status and the problem code. The handshake also enforces the
`Origin`, like the service terminal: another website cannot open a shell with
your session cookie.

Switching the setting **off** ends the sessions that are open on that server and
refuses new ones.

## What is recorded

| Event | Audit action | Metadata |
|---|---|---|
| the setting changes | `server.shell.enable`, `server.shell.disable` | who (the audit actor), server |
| a session opens | `server.shell.open` | session ID, server, production flag, how it runs (`host` or the helper container's name) |
| it ends | `server.shell.close` | duration in seconds, why it ended (`exit`, `client_closed`, `idle`, `agent_disconnected`, `host_shell_disabled`, `error`), exit code |

**Keystrokes and output are never recorded, logged or stored**: not in the audit
log, not on the control plane, not on the agent. The open entry is written before
the shell is handed over: if the audit log is unavailable, the shell is killed and
not opened. A session with **no input for 15 minutes** is closed (a warning
appears one minute before).

## How the agent runs it

- **An agent installed on the host** (the supported production setup,
  `install-agent.sh` with systemd) spawns the shell itself on a pseudo
  terminal: `/bin/bash -l` when the host has bash, else `/bin/sh -l`, with
  `TERM=xterm-256color`, in the agent user's home directory. The shell gets a
  minimal environment (`TERM`, `HOME`, `USER`, `PATH`, `SHELL`, locale). **The
  agent's own variables, the enrolment token among them, are not passed on.**
  Closing the session hangs up the shell's process group (SIGHUP, then SIGKILL
  after 2 seconds); a process that detached itself with `setsid` or `nohup` keeps
  running, as it would after an SSH logout.
- **An agent that runs in a container** (the development stack, a compose setup)
  cannot see the host, so it starts a short-lived **helper container** from its
  own image: `--privileged --pid=host`, running
  `nsenter -t 1 -m -u -i -n -p -- /bin/sh -l`, which enters the namespaces of the
  host's PID 1. The helper is named `nebulactrl-hostshell-<session8>`, labelled
  `nebulactrl.managed=hostshell`, and **always removed**:
  - when the session ends (the shell exits, the person closes it, an admin
    switches the setting off, the connection drops or the session idles out);
  - if the agent itself dies mid-session, the Docker daemon removes it once its
    input is gone (`--rm` semantics with stdin closed on disconnect), and the
    agent's **orphan sweep** removes whatever is left on its next reconcile and
    every minute (a helper whose session no agent process knows).

  The agent's image must contain `nsenter` (util-linux or BusyBox). Docker's own
  cleanup never touches these containers.

## Limits and non-goals

- No recording or replay of sessions, no sharing of a live session, no file
  transfer (use the [file browser](../operations/terminal.md) for container
  files).
- The shell is not a sandbox: the person can do anything root can do on the
  machine, including stopping the agent and Docker.
- The team boundary applies: another team's servers are a 404. The host shell
  of the server NebulaCtrl itself runs on reaches the control plane's database
  and secret key, so only the instance admin can turn it on or open it.

See also: [web terminal](../operations/terminal.md), [login methods](login-methods.md)
(two-factor authentication), [threat model](threat-model.md).
