# Installer tests

`run.sh` runs the real `deploy/install/install.sh` in disposable systemd machines and checks what it did. It needs macOS with [OrbStack](https://orbstack.dev) (`orbctl`), Docker, and about 5 GB free; it creates machines named `nc-inst-<target>` and **deletes them at the end** (`--keep` leaves one).

```sh
tests/install/run.sh                         # debian12, ubuntu2404, rocky9
tests/install/run.sh rocky9                  # one target (also: debian13, ubuntu2204, ubuntu2604, alma9)
tests/install/run.sh --archive image.tar …   # reuse a `docker save` of the control-plane image
NC_TEST_HARDENING=1 tests/install/run.sh …   # also run the Lynis hardening step (needs network)
```

The image is built once from the working tree (`docker build`, tag `nebulactrl:instlocal`), saved, and loaded into each machine with `--image-archive`; OrbStack mounts the macOS home directory into every machine, so scratch files live in `~/.nc-inst-tmp` (override with `NC_TEST_LOGDIR`; delete it afterwards). The apps phase pulls `traefik/whoami` from Docker Hub.

| Phase | What it checks |
|---|---|
| install | the **assembled, piped** script (`go run ./tests/install/assemble | bash`, like `curl | bash`); no secret in its output; Docker installed from Docker's repository |
| `assert fresh` | file modes, generated secrets, Postgres private (internal network, no published port, SCRAM only, tuned, checksums), container hardening, daemon defaults, the daily backup scheduled and written, agent running and enrolled, ufw/firewalld, fail2ban, automatic updates |
| `assert rerun` | a second run changes nothing: same `.env`, same container IDs |
| `assert firstrun` | setup refused without/with a wrong token, accepted with it, closed afterwards; the host's agent enrolled before the admin existed and shows as the online control-plane server |
| `assert domain` | `--domain` switch: secrets kept, 8000 unpublished, the dashboard served by the host proxy (HTTPS, HTTP→HTTPS, WebSocket upgrade, login through it), the route listed in the proxy table |
| `assert apps` | an app deployed on the control-plane host answers through the same proxy and the dashboard route survives the deploy; **Accept workloads** off refuses a new service (422) and keeps the old one serving |
| `assert uninstalled` / `reinstalled` / `purged` | default uninstall keeps data, apps and the proxy running and reverts host changes; reinstall over the kept data restores everything; `--purge --remove-workloads --remove-backups` leaves nothing; SSH config is byte-identical throughout |

## Side-by-side scenario

`tests/install/sidebyside.sh [--keep] [--archive image.tar] [ubuntu2604]` runs the side-by-side layout in one machine: a busybox container plays Coolify's proxy on 80/443/8000, then the installer runs with `--no-firewall --no-fail2ban --no-hardening --port 8100 --proxy-http-port 8080 --proxy-https-port 8443 --dashboard-allow <tailnet> --dashboard-dns01 cloudflare --dns-token-file …` (a canary string as the token).

| Phase | What it checks |
|---|---|
| `assert sidebyside` | the other proxy untouched on 80/443/8000; NebulaCtrl's proxy on 8080/8443; the dashboard URL with its port and the control plane on loopback 8100; the allow list enforced (403 from outside) and editable at once (audited, 409 `dashboard_lockout` through the route, the loopback port repairs it); the DNS-01 credential sealed and the token in no log, dump or API read; an app answering on `:8443`; HTTP-01 warned on a domain without a resolver |
| re-runs | the same flags and a plain re-run change nothing; asking for 80/443 while the other proxy holds them is refused and changes nothing |
| `assert moveback` | with the other proxy stopped, a re-run on the default ports recreates the proxy on 80/443, drops the port from the dashboard URL, keeps the app container, the allow list and the DNS-01 method |
| `assert bind` | after an uninstall, IP mode with `--bind <ip>`: published on that address and on loopback only, remembered by a plain re-run, the agent connects over loopback |

Unit tests of the option parsing and persistence are in `deploy/install/flags_test.go` (no machine needed).

Logs per target are in `$NC_TEST_LOGDIR/<target>.log`. Unit and integration tests of the pieces the installer relies on live in `internal/app/installer_test.go` (Accept workloads, setup token, local-server enrollment and team adoption, the dashboard system route), `internal/agent/conn` (loopback plain HTTP) and `deploy/install/embed_test.go` (the assembled script).
