---
title: Docker options
description: "Typed container options for a service: hardening, hostname, DNS, sysctls, user, and devices, GPUs and host access on unrestricted servers."
section: Applications
order: 130
---

# Docker options

A service can set a fixed list of **typed** Docker container options. They exist for images that need them (a database that wants a bigger `/dev/shm`, a proxy that binds port 80 as a non-root user, RabbitMQ that stores its data by node name, a media server that needs the GPU) and for hardening (drop all capabilities, read-only root filesystem, a processes limit). There is no free-text `docker run` string: every option is a validated field, so nothing is silently ignored.

Where: the service's **Settings → Advanced → Docker options**, the CLI (`nebulactrl services docker`), or API `docker` inside the service config (`POST /environments/{id}/services`, `PATCH /services/{id}`). It is release config, so a change is **staged** and applies with the next deploy, appears in the pending changes as `config.docker.<field>` and is frozen into the release (a rollback restores the options of that release).

## Two tiers

The trust rule: **nobody below the instance admin can gain root on a host unless the instance admin made that server _unrestricted_.** Options therefore come in two tiers.

- **Every server.** Options that cannot hand over the host: they only restrict the container, or change something inside its own namespaces.
- **Unrestricted servers only.** Options that give a container the host's devices, GPUs, a different runtime or something equivalent to host root. An instance admin switches a server to unrestricted ([below](#the-unrestricted-switch-per-server)); on any other server these options are refused with `422 requires_unrestricted_server`, and the UI shows the group disabled with the reason.

## Options on every server

| Option | Field | Rules |
|---|---|---|
| Capabilities to add | `capAdd` | Names from the [safe list](#capabilities), with or without the `CAP_` prefix, stored upper-case, no duplicates |
| Capabilities to drop | `capDrop` | `ALL` or Linux capability names; applied before `capAdd`, so `capDrop: [ALL]` with `capAdd: [NET_BIND_SERVICE]` is the hardening pattern. A name in both lists is refused |
| No new privileges | `noNewPrivileges` | sets the `no-new-privileges` security option. Programs that rely on setuid helpers such as `sudo` stop working |
| Read-only root filesystem | `readOnlyRootfs` | writes go only to volumes and `tmpfs`. **Config files cannot be written to a read-only root** (Docker refuses `docker cp` into it, and a tmpfs is mounted over whatever was copied), so every config file of the service must sit under a volume mount; otherwise `422 invalid_config` |
| Processes limit | `pidsLimit` | 0 (no limit) or 1-4194304 processes and threads for the container (cgroup). This is the real per-container limit; see [`nproc`](#ulimits) |
| Hostname | `hostname` | one RFC 1123 label (lowercase letters, digits and dashes, at most 63 characters). **Empty means the service slug**, for every service: the hostname does not change with each deploy, which software that stores data by node name (RabbitMQ) needs. **With several replicas every replica gets `<name>-<replica index>`** (`web-0`, `web-1`, ...; an explicit `rabbit` becomes `rabbit-0`, `rabbit-1`, ...), so replicas never share a hostname; a single replica keeps the plain name. The name for an index is the same on every deploy, and the base name is cut if needed to keep the result at most 63 characters. Scaling up adds replicas without restarting the running ones, so a replica that was alone keeps its plain name until the next deploy renames it `<name>-0` |
| DNS servers | `dns` | up to 3 resolver **IP addresses** (IPv4 or IPv6, no hostnames, no duplicates). Docker's embedded resolver still serves container names and forwards the rest to them |
| Sysctls | `sysctls` | up to 16 entries from the [curated list](#sysctls); the value is a number or numbers separated by single spaces |
| Host kernel requirements | `hostSysctls` | not applied: minimum values the **host** needs, for example `{"vm.max_map_count": 262144}`; the plan warns when a server has less ([below](#host-requirements)) |
| User | `user` | `uid[:gid]` as numbers (up to 2147483647) or user and group names |
| Supplementary groups | `groupAdd` | up to 8 numeric group IDs from 1 to 65535, for example the host's `render` group so a non-root process can open a GPU device |
| Resource limits | `ulimits` | `nofile` or `memlock`; `soft` and `hard` are `-1` (unlimited) or 0-2147483647; `soft` must not exceed `hard` unless `hard` is `-1`; each name once |
| Shared memory | `shmSizeMb` | `/dev/shm` size in MiB, 0 (Docker's default 64 MiB) to 65536 |
| Extra hosts | `extraHosts` | up to 20 `name:ip` (IPv4 or IPv6) or `name:host-gateway` entries |
| Labels | `labels` | up to 30; key `[A-Za-z0-9][A-Za-z0-9._/-]*` (127 characters at most), value at most 512 characters, no line breaks. **`com.docker.*`, `nebulactrl.*` and `traefik.*` are reserved** (case-insensitive): the managed labels and the proxy's own can never be set or overwritten |
| Init | `init` | run Docker's init (tini) as PID 1: reaps zombies and forwards signals |
| Stop signal | `stopSignal` | one of `SIGTERM`, `SIGINT`, `SIGQUIT`, `SIGHUP`, `SIGUSR1`, `SIGUSR2`, `SIGWINCH`, `SIGKILL`; the grace period (`stopGraceSeconds`) is unchanged |
| Entrypoint | `entrypoint` | replaces the image's `ENTRYPOINT`; an exec-form argv of up to 32 elements (each at most 4096 bytes, no NUL), the first one the program. The start command (`startCommand`) is passed to it as arguments. Like `docker run --entrypoint`, overriding the entrypoint **drops the image's default command**, so set `startCommand` when the program needs arguments. No shell is involved unless the first element is one. An empty entrypoint (clearing the image's) is not supported |
| tmpfs | `tmpfs` | up to 10 in-memory mounts: absolute, clean `path` (not `/`, not under `/proc`, `/sys`, `/dev`, `/run/docker`, not colliding with a config file mount), `sizeMb` (default 64, at most 65536), optional octal `mode` such as `1777` |

**Hardening is opt-in.** Nothing is switched on by default except the hostname: `no-new-privileges` breaks images that rely on setuid helpers and `capDrop: [ALL]` breaks images that chown their data directory at start, so you choose them per service. The Docker options section offers a one-click **Hardened** preset (`capDrop: [ALL]`, `noNewPrivileges`, `readOnlyRootfs`, `pidsLimit: 512` and a `/tmp` tmpfs) that you then adjust to what the image needs.

A violation is `422 invalid_config` with the reason. The agent validates the same rules again before it creates a container, against the server policy carried in the deploy payload, so an edited or old payload cannot slip past the control plane.

### Capabilities

Safe list (allowed on every server): `AUDIT_WRITE`, `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `FSETID`, `IPC_LOCK`, `KILL`, `MKNOD`, `NET_BIND_SERVICE`, `NET_RAW`, `SETFCAP`, `SETGID`, `SETPCAP`, `SETUID`, `SYS_CHROOT`, `SYS_NICE`, `SYS_PTRACE`. Docker grants most of these by default already; listing one is still useful after `capDrop: [ALL]` and for `IPC_LOCK` (with `memlock`), `NET_BIND_SERVICE`, `SYS_NICE` and `SYS_PTRACE`.

- `SYS_PTRACE` is allowed with a warning (the plan and the UI show it): processes in the container can inspect and tamper with each other.
- **`NET_ADMIN` and `SYS_ADMIN` need an unrestricted server.** They widen the container's power over the network stack and the kernel.
- Anything else is refused: `ALL`, `SYS_MODULE`, `SYS_RAWIO`, `SYS_TIME` and so on. (`capDrop` accepts `ALL` and every Linux capability name.)

### Sysctls

A container can only change **namespaced** kernel parameters. Docker supports the IPC namespace (`kernel.msg*`, `kernel.sem`, `kernel.shm*`, `fs.mqueue.*`) and the network namespace (`net.*`), and "does not support changing sysctls inside of a container that also modify the host system" ([docker run reference](https://docs.docker.com/reference/cli/docker/container/run/#sysctl)). Containers never use the host network or IPC namespace, so each entry below changes only the container's own namespace. `sysctls` accepts exactly:

- IPC: `kernel.msgmax`, `kernel.msgmnb`, `kernel.msgmni`, `kernel.sem`, `kernel.shm_rmid_forced`, `kernel.shmall`, `kernel.shmmax`, `kernel.shmmni`, `fs.mqueue.msg_default`, `fs.mqueue.msg_max`, `fs.mqueue.msgsize_default`, `fs.mqueue.msgsize_max`, `fs.mqueue.queues_max`.
- Network: `net.core.somaxconn` (listen backlog), `net.ipv4.ip_unprivileged_port_start` (bind below port 1024 without a capability), `net.ipv4.ip_forward`, `net.ipv4.conf.all.forwarding`, `net.ipv4.conf.default.forwarding`, `net.ipv4.conf.all.src_valid_mark` and `net.ipv4.conf.all.rp_filter`, `net.ipv4.conf.all.route_localnet` (VPN containers), `net.ipv4.ip_local_port_range`, `net.ipv4.ping_group_range`, `net.ipv4.tcp_fin_timeout`, `net.ipv4.tcp_keepalive_time`, `net.ipv4.tcp_keepalive_intvl`, `net.ipv4.tcp_keepalive_probes`, `net.ipv4.tcp_syncookies`, `net.ipv6.conf.all.disable_ipv6`, `net.ipv6.conf.all.forwarding`, `net.ipv6.conf.default.disable_ipv6`.

Host-wide parameters such as `vm.max_map_count` are refused in `sysctls` with a pointer to `hostSysctls`.

### Ulimits

`nofile` and `memlock` only. There is no `nproc` ulimit: the Linux `RLIMIT_NPROC` is counted per real user ID across the whole host and is not enforced for UID 0, so it would give false assurance. Use `pidsLimit`, a cgroup limit that applies to the container whoever runs in it.

### Host requirements

A container cannot raise a host sysctl, so an application that needs one (Elasticsearch needs `vm.max_map_count` of at least 262144 for its mmap-based stores) declares it in `hostSysctls`. Nothing is applied. The agent reports the current values of `vm.max_map_count`, `fs.file-max`, `fs.inotify.max_user_watches`, `fs.inotify.max_user_instances`, `kernel.pid_max`, `net.core.rmem_max` and `net.core.wmem_max` with its facts (read again whenever it reconnects), and the **deployment plan** adds a warning for each shortfall with the exact commands:

```
sysctl -w vm.max_map_count=262144
echo 'vm.max_map_count = 262144' | sudo tee -a /etc/sysctl.d/99-nebulactrl.conf
```

The warning does not block the deploy (some applications run without it, slower or with errors). An agent that has not reported the value yet gets a warning that says so.

## Options on unrestricted servers only

| Option | Field | What it does |
|---|---|---|
| Devices | `devices` | up to 16 host device nodes: `hostPath` below `/dev` (for example `/dev/dri/renderD128`, `/dev/kvm`, `/dev/net/tun`), optional `containerPath` (default: the same path), `permissions` a subset of `rwm` (default `rwm`) |
| GPUs | `gpus` | `count` (`-1` for all, or 1-16), `driver` `nvidia`, optional `capabilities` (`compute`, `utility`, `video`, `graphics`, `display`, `compat32`). Needs the NVIDIA Container Toolkit on the host |
| Runtime | `runtime` | an OCI runtime configured in the host's Docker daemon, such as `runsc` (gVisor), `kata` or `sysbox-runc`. A sandboxing runtime raises isolation |
| Privileged | `privileged` | all capabilities, all devices, no seccomp or AppArmor profile: **equivalent to root on the host** |
| Host PID namespace | `pidHost` | the container sees and can signal every process on the host |
| Seccomp unconfined | `seccompUnconfined` | removes the default syscall filter |
| AppArmor unconfined | `apparmorUnconfined` | removes the default AppArmor profile |
| NET_ADMIN, SYS_ADMIN | `capAdd` | see [Capabilities](#capabilities) |

On a restricted server each of them is refused when you save the service, when you move it to that server, when a template that needs it is installed, and again at every deploy, rollback and reconcile redeploy (the plan reports the check as blocking: `requires_unrestricted_server`). Use them only for images you trust: a bug in the NVIDIA toolkit, a driver behind a device node or a kernel exploit without the seccomp filter is a way out of the container.

### The unrestricted switch (per server)

One switch per server controls both these options and [unrestricted Compose stacks](compose-stacks.md#unrestricted-per-server): **Servers → the server → Settings → Danger zone → Unrestricted** (API `PUT /servers/{id}/unrestricted`, CLI `nebulactrl servers unrestricted`).

- **Instance admin only** (the earliest-created user, or an admin-scoped token acting for them). A team owner, a developer or a token without the admin scope gets 403.
- **Enabling needs the server's exact name typed**; the dialog warns that anyone who can deploy there can take the server over.
- **Audited** as `server.unrestricted.enable` or `server.unrestricted.disable`, with the server name, the number of stacks and the number of services that use unrestricted-only options.
- It is **read at every deploy, rollback and reconcile**. Disabling it does not stop running containers; the next deploy of a stack or service that needs it fails before anything changes, until the options are removed or the service is moved.
- A service that carries `NET_ADMIN` or `SYS_ADMIN` needs its server to be unrestricted; there is no instance-wide switch for them.

## What is never possible

Not configurable on any server, not on any list: `--ip`/`--ip6` (rolling deploys start the new container while the old one runs, so two containers would need the same address; use the private DNS name), host networking on a plain service (routes dial the container on the environment network; a [stack](compose-stacks.md) on an unrestricted server can use it), the restart policy and log options (the platform owns them; see [logs](logs.md)), the host IPC namespace (use `shmSizeMb`), host bind mounts and the Docker socket, `--device-cgroup-rule`, and the host user, UTS or cgroup namespaces. Each container still joins only its environment network (and shared networks you attach); memory and CPU limits stay under NebulaCtrl's control.

## Interactions

- **Replicas and scaling:** every replica gets the same options. The non-secret runtime spec stored in the container's `nebulactrl.spec` label carries them and the server policy they were validated against, so a replica the agent adds on its own (a reconcile or autoscaler scale-up) is created identically. Scaling re-reads the server's unrestricted switch.
- **Managed databases** accept the options too (a larger `shmSizeMb` is the usual one for PostgreSQL). Their image, port and volume stay derived from the engine.
- **Route services** have no container: options do not apply (`422 invalid_config`).
- **Tasks** (one-off, cron, pre- and post-deploy commands) run in their own containers and do not inherit these options.
- **Templates** can set them in a `docker:` block with the same field names ([Templates](templates.md)); a template that uses an unrestricted-only option declares `requiresUnrestricted: true` and installs only on an unrestricted server.
- **Compose import** maps only `cap_add`, `ulimits`, `shm_size`, `extra_hosts`, `init`, `stop_signal` and `tmpfs`; the other Compose keys are listed in [Compose import](compose-import.md).

## Verifying

`docker inspect <container>` on the host shows the result: `Config.Hostname`, `Config.User`, `HostConfig.ReadonlyRootfs`, `HostConfig.CapAdd`, `HostConfig.CapDrop`, `HostConfig.SecurityOpt`, `HostConfig.PidsLimit`, `HostConfig.Sysctls`, `HostConfig.GroupAdd`, `HostConfig.Dns`, `HostConfig.Devices`, `HostConfig.DeviceRequests`, `HostConfig.Runtime`, `HostConfig.Privileged`, `HostConfig.PidMode`, `HostConfig.ShmSize`, `HostConfig.Ulimits`, `HostConfig.Init`, `HostConfig.Tmpfs`, `HostConfig.ExtraHosts`, `Config.StopSignal`, `Config.Entrypoint` and `Config.Labels`. The integration tests `TestPublicDatabasePortAndDockerOptions` and `TestHardeningDockerOptions` assert that on real Docker.
