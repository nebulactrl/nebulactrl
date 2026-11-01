---
title: Host hardening
description: "A Lynis audit of each server and a curated set of safe fixes."
section: Servers
order: 80
---

# Host hardening

NebulaCtrl audits each server with [Lynis](https://cisofy.com/lynis/) (CISOfy's free host auditing tool), shows the result on the server's **Security** tab, and offers a short, curated list of **safe fixes** it can apply for you and undo again.

It is deliberately conservative. A fix is in the list only if it is reversible, changes nothing a service or a login depends on in normal use, and cannot cut you off from the host. Everything else Lynis suggests is shown with an explanation and the commands to do it yourself.

**NebulaCtrl never changes SSH settings, firewall rules or user accounts.** There is no fix for them in the catalog, a test pins it, and the Security tab says so next to those findings. The installer's own ufw rules (SSH, 80, 443) are the only firewall change NebulaCtrl ever makes.

## What runs where

| Where | What |
|---|---|
| Installer (`install.sh`) | The last step runs `nebulactrl-agent hardening run` on the control-plane host: installs Lynis, audits, applies the default fixes, audits again and prints the index before and after. `--no-hardening` skips it. |
| Agent | Runs everything on the host. An agent installed on the host runs the scripts directly as root. An agent that runs in a container (the dev stack) starts a short-lived privileged helper container from its own image whose command enters PID 1's namespaces with `nsenter`, the same mechanism as the [host shell](host-shell.md). The helper is removed by the daemon when the script ends. |
| Control plane | Schedules the weekly audit, starts applies and undos, keeps the parsed report and the state of every fix, audits who did what. |
| Dashboard | Server → **Security**: the index, the fixes, what is left, the history. |

The agent advertises the capability `host.hardening`; a server whose agent is older gets 422 `agent_too_old` and the tab says to update it.

Operating system package updates and reboots are a separate feature with their own checks and actions: see [Server patching](../operations/server-patching.md). A hardening fix that only takes effect after a reboot sets the same *reboot required* note.

## The audit

`lynis audit system --quick --no-colors --cronjob`, run from `/` (Lynis treats a working directory that has a `db/` or `include/` folder as its own installation). The agent reads `/var/log/lynis-report.dat` ([report files](https://cisofy.com/documentation/lynis/get-started/#report-files): `key=value` lines, repeated keys end in `[]`) and keeps these keys: `lynis_version`, `hostname`, `os_name`, `os_version`, `report_datetime_end`, `hardening_index`, `lynis_tests_done`, `vulnerable_packages_found`, `warning[]`, `suggestion[]` (`ID|text|details|solution|`) and `details[]` (`ID|section|desc:…;field:…;prefval:…;value:…;|`, which is where the per-key sysctl values and sshd options of KRNL-6000 and SSH-7408 live). A report without `hardening_index` means the audit did not finish and is rejected. The audit only reads the host.

Lynis is installed on request, never silently:

* The installer installs it on the control-plane host.
* The Security tab shows **Install Lynis and run audit** on a server that does not have it. Clicking it is the consent.
* The weekly audit never installs anything; on a server without Lynis it records a *skipped* run.

The installer prefers CISOfy's own repository, because the distributions ship older Lynis releases: it fetches the signing key from `packages.cisofy.com`, checks that it is an armored PGP key, installs it to `/usr/share/keyrings/cisofy-lynis.asc`, adds `deb [signed-by=…] https://packages.cisofy.com/community/lynis/deb/ stable main` and refreshes only that source (RHEL family: `/etc/yum.repos.d/cisofy-lynis.repo` with `gpgcheck=1`). If the repository is not reachable it falls back to the distribution's `lynis` package on Debian and Ubuntu. To remove the repository: delete `/etc/apt/sources.list.d/cisofy-lynis.list` and the keyring file. Sources: [Lynis get started](https://cisofy.com/documentation/lynis/get-started/), [CISOfy package repository](https://packages.cisofy.com/community/).

The weekly audit is a control-plane job that sends a `host_hardening` command once a week to every online server (switch it off per server on the tab). Why the control plane and not a timer on the host: the result, the history and the audit log live in one place, and a server that was offline simply gets its audit when it comes back.

## The safe fixes

Lynis test IDs link to CISOfy's page for the control: `https://cisofy.com/lynis/controls/<ID>/`. "Default" fixes are what **Apply safe fixes** and the installer apply; the others are offered one by one.

| Fix | Default | Lynis test | Check (already satisfied when …) | Fix | Rollback | Downside |
|---|---|---|---|---|---|---|
| `sysctl-kernel` | yes | [KRNL-6000](https://cisofy.com/lynis/controls/KRNL-6000/) | each key has the value, or is stricter, or does not exist | `kernel.dmesg_restrict=1`, `kernel.kptr_restrict=2`, `kernel.sysrq=0`, `kernel.ctrl-alt-del=0`, `kernel.yama.ptrace_scope≥1`, `dev.tty.ldisc_autoload=0`, `net.core.bpf_jit_harden=2`, `kernel.core_uses_pid=1`; written live to `/proc/sys` and to `/etc/sysctl.d/90-nebulactrl-sysctl-kernel.conf` | previous values written back, drop-in deleted | `kptr_restrict=2` hides kernel addresses from root-run profilers; `ptrace_scope=1` stops non-root users attaching a debugger to processes that are not their children |
| `sysctl-fs` | yes | KRNL-6000, [KRNL-5820](https://cisofy.com/lynis/controls/KRNL-5820/) | as above | `fs.suid_dumpable=0`, `fs.protected_hardlinks=1`, `fs.protected_symlinks=1` | as above | none known |
| `sysctl-net` | yes | KRNL-6000 | as above | no ICMP redirects accepted or sent (v4, v6), no source routing, `log_martians=1`, `tcp_syncookies=1`, ignore broadcast pings and bogus ICMP errors | as above | `log_martians` is noisy on an internet-facing host (the kernel rate-limits it) |
| `sysctl-fs-protected` | no | KRNL-6000 | as above | `fs.protected_fifos=2`, `fs.protected_regular=2` | as above | a root job that opens another user's lock file in `/tmp` fails; audit your jobs first |
| `core-dumps` | yes | KRNL-5820 | both drop-ins exist | `/etc/systemd/coredump.conf.d/nebulactrl-hardening.conf` (`Storage=none`, `ProcessSizeMax=0`) and `/etc/security/limits.d/nebulactrl-hardening.conf` (`* hard core 0`) | both files deleted | no core files for post-mortem debugging |
| `umask` | yes | [AUTH-9328](https://cisofy.com/lynis/controls/AUTH-9328/) | `UMASK` in `/etc/login.defs` is 027, 0027, 077 or 0077 | `UMASK 027` | the original `UMASK` line restored verbatim | files you create by hand (also with `sudo`, because `pam_umask` applies) are 640/750; a service running as another user cannot read a config you wrote with `sudo tee` until you `chmod` it |
| `password-ageing` | yes | [AUTH-9286](https://cisofy.com/lynis/controls/AUTH-9286/), AUTH-9230 | `PASS_MAX_DAYS` set and not 99999, `PASS_MIN_DAYS`≥1, `SHA_CRYPT_MIN_ROUNDS` and `MAX_ROUNDS`≥5000 | sets the missing ones to 365 / 1 / 5000 / 10000 in `/etc/login.defs`; a stricter value of yours is kept | the original lines restored verbatim | new human accounts must change their password yearly and not twice in a day. **Defaults for new accounts only**: existing accounts, root and system accounts are never changed (no `chage`) |
| `cron-permissions` | yes | [FILE-7524](https://cisofy.com/lynis/controls/FILE-7524/) | `/etc/crontab` ≤600 and `/etc/cron.{d,hourly,daily,weekly,monthly}` ≤700 (missing ones are ignored) | `chmod 600` / `chmod 700`, tighten only, previous modes recorded | previous modes restored | other local users cannot read the system crontabs. `cron.allow`/`deny` and `at.allow`/`deny` are left alone (on Debian and Ubuntu `crontab(1)` reads them through its setgid group) |
| `protocols` | yes | [NETW-3200](https://cisofy.com/lynis/controls/NETW-3200/) | each module is blocked or loaded right now | `install <m> /bin/false` and `blacklist <m>` for `dccp sctp rds tipc` in `/etc/modprobe.d/nebulactrl-protocols.conf` | file deleted | software that really uses SCTP, RDS or TIPC stops working. A module loaded right now is left alone |
| `firewire` | yes | [STRG-1846](https://cisofy.com/lynis/controls/STRG-1846/) | as above | `firewire-core firewire-ohci ohci1394` | file deleted | none unless a FireWire device is attached |
| `filesystems` | yes | FILE-6430 | as above | `cramfs freevxfs jffs2 hfs hfsplus`. **`squashfs` (snap) and `udf` (cloud config drives) are deliberately not blocked.** | file deleted | a host that mounts one of those filesystems can no longer |
| `legal-banner` | yes | [BANN-7126](https://cisofy.com/lynis/controls/BANN-7126/), [BANN-7130](https://cisofy.com/lynis/controls/BANN-7130/) | the file has at least five of Lynis's legal key words | puts a two-line banner above the existing text of `/etc/issue` and `/etc/issue.net` | the original restored, unless you edited the file since (then it is left as it is) | cosmetic; shown on the console login. Over SSH only if sshd's `Banner` points at it, which NebulaCtrl never sets |
| `usb-storage` | no | [USB-1000](https://cisofy.com/lynis/controls/USB-1000/) | blocked or loaded | `usb-storage` blocked | file deleted | USB disks, backup drives included, stop working |
| `tmp-mount` | no | [FILE-6310](https://cisofy.com/lynis/controls/FILE-6310/) | `/tmp` is mounted `nosuid,nodev`, or the drop-in exists and `tmp.mount` is enabled | a systemd drop-in `Options=mode=1777,strictatime,nosuid,nodev` for `tmp.mount` and `systemctl enable tmp.mount`; **takes effect at the next boot** | drop-in deleted, unit disabled | `/tmp` becomes RAM-backed. Only offered when systemd is init, the host is not a container, has ≥2 GB RAM and `/tmp` is not already a mount (Debian 13 already is). `noexec` is deliberately not set: package scripts run from `/tmp` |
| `auditd` | no | [ACCT-9628](https://cisofy.com/lynis/controls/ACCT-9628/), ACCT-9630 | `auditd` is active and the rule file exists | installs `auditd`/`audit` if missing, rules that watch `/etc/passwd`, `group`, `shadow`, `gshadow`, `sudoers` and `sudoers.d` | rule file deleted; `auditd` stopped only if NebulaCtrl started it; the package stays | runs a daemon; logs grow (8 MB files, 5 kept). Unavailable in containers |

### How a fix is applied

Each fix is POSIX shell (`internal/lynis/catalog.go`) with four parts: a *precondition* (is it possible here), a *check*, the *apply* and the *rollback*. The same text is shown on the Security tab as the commands to do it by hand.

1. If the check already passes, the outcome is `unchanged` and nothing is touched (and NebulaCtrl will not claim it as its own).
2. If the precondition fails (a container, no systemd, too little memory, no `/etc/login.defs`), the outcome is `skipped` with the reason.
3. Apply runs with `set -e`. If it fails halfway, its rollback runs and the outcome is `failed` with the message.
4. After apply the check runs again. If it does not pass, the fix is rolled back and the outcome is `failed` "did not take effect". A fix is therefore in place or not at all.
5. State is kept in `/var/lib/nebulactrl/hardening/` (previous sysctl values, original `login.defs` lines, previous file modes, backups, a marker per applied fix). **Rollback only undoes fixes it applied**: for a fix that was already compliant it says so and does nothing. A later edit of yours (another line in `login.defs`) survives a rollback.

Sysctl values are only ever tightened: a value that is already stricter than the target (for example `ptrace_scope=3`) is kept, and a key the kernel does not have, or will not let the agent write (some containers), is left alone and not reported as a failure.

### Deliberately not in the catalog

Lynis's KRNL-6000 profile asks for several values NebulaCtrl will not set. The Security tab lists each with the reason:

| Lynis wants | Why not |
|---|---|
| `net.ipv4.conf.all.forwarding=0` (also `ip_forward`) | Docker routes container traffic through it; turning it off cuts every container off the network |
| `net.ipv4.conf.all.rp_filter=1` | strict reverse-path filtering drops legitimate asymmetric traffic and breaks Tailscale, WireGuard policy routing and multi-homed hosts. Loose mode (2) is the safe choice if you want it |
| `kernel.modules_disabled=1` | one-way until reboot, and Docker loads kernel modules |
| `kernel.unprivileged_bpf_disabled=1` | cannot be rolled back without a reboot; the common value 2 already blocks unprivileged BPF |
| SSH-7408 (sshd options), SSH key or root-login settings, `chmod 600 /etc/ssh/sshd_config` | SSH is how you reach the host; a wrong value locks you out |
| Firewall rules (FIRE-*) | specific to what runs on the host |
| Installing PAM password-strength modules, AIDE, rkhunter, compilers' permissions, repartitioning for `/home` and `/var` | each needs a decision or can break a login or a build; the tab explains and gives the commands |

## Who may do what

* **Reading** the Security tab needs the `servers:read` scope.
* **Audit now, Apply safe fixes, Undo and the weekly switch** need `servers:write` **and** the Owner or Admin role (403 otherwise). They are recorded in the audit log: `server.hardening.audit`, `server.hardening.apply`, `server.hardening.rollback` and `server.hardening.update` with who, the server and the fix IDs; when a run finishes, `server.hardening.run` records the index before and after and the outcome of every fix. The weekly run is audited when it finishes (actor: system).
* One run at a time per server (409 `hardening_running`). An offline server gives 409 `server_offline`.

## API

All under `/api/v1/servers/{serverId}/hardening` (see `openapi/openapi.json`):

| Method and path | Purpose |
|---|---|
| `GET /hardening` | The Security view: index (now and at the first audit), Lynis version, every fix with its state, the remaining findings with explanation and commands, recent runs |
| `PUT /hardening` | `{weekly: bool}` |
| `POST /hardening/audit` | `{installLynis?: bool}` → 202 with the run |
| `POST /hardening/apply` | `{fixIds?: []}` (empty: the default set) → 202 with the run |
| `POST /hardening/rollback` | `{fixIds: [...]}` → 202 with the run |

Fix states: `applied` (NebulaCtrl applied it), `compliant` (already in place), `pending`, `unavailable` (with the reason) and `unknown` (no audit yet). After an apply or an undo the agent audits again, so the index on the page is the one after the change.

## Command line on the host

`nebulactrl-agent hardening` works directly on the machine as root, with no control plane:

```sh
sudo nebulactrl-agent hardening list                       # the fixes and which are default
sudo nebulactrl-agent hardening audit [--install]          # Lynis audit, read-only
sudo nebulactrl-agent hardening run                        # install Lynis, audit, apply defaults, audit again
sudo nebulactrl-agent hardening apply --fix umask,core-dumps
sudo nebulactrl-agent hardening rollback --fix umask       # undo
```

Undo what the installer applied before you uninstall NebulaCtrl, because `uninstall.sh` removes the agent binary: `hardening rollback --fix sysctl-kernel,sysctl-fs,sysctl-net,core-dumps,umask,password-ageing,cron-permissions,protocols,firewire,filesystems,legal-banner`.

## Data

Three tables: `server_hardening_settings` (weekly audit on/off; no row means on), `server_hardening_state` (the newest report, the state of every fix, the first index, the reboot flag) and `server_hardening_runs` (30 runs per server).

## Sources

* Lynis: [Get started](https://cisofy.com/documentation/lynis/get-started/) (installation, `--cronjob`, report files), [CISOfy package repository](https://packages.cisofy.com/community/), [control pages](https://cisofy.com/lynis/controls/) for each test ID above.
* Kernel settings: [kernel.txt](https://www.kernel.org/doc/Documentation/sysctl/kernel.txt), [fs.txt](https://www.kernel.org/doc/Documentation/sysctl/fs.txt) (the pages Lynis's own profile links to).
* What each Lynis test accepts (for example the five legal key words of BANN-7126, the exact `install <module> /bin/false` lines of NETW-3200, STRG-1846 and USB-1000, the `UMASK` values of AUTH-9328) was read from the test sources in `/usr/share/lynis/include/` of the installed Lynis 3.1.7.
