---
title: Uninstall
description: "Remove NebulaCtrl from a machine, with or without your data."
section: Installing
order: 60
---

# Uninstall

## The control-plane host

```sh
sudo bash /opt/nebulactrl/uninstall.sh                # stops NebulaCtrl, keeps everything that holds data
curl -fsSL https://nebula.example.com/uninstall.sh | sudo bash -s -- --purge   # the control plane serves the script too
```

The uninstaller prints what it will do and asks first (`--yes` skips the question; `--purge` and `--remove-workloads` ask you to type `delete`).

| Option | What it adds |
|---|---|
| *(none)* | Stops and removes the control-plane and Postgres containers and the **agent service and binary**. **Kept:** the database volume, `/opt/nebulactrl` (with the secret key), the agent identity, the local backups, and **your deployed apps, their volumes and the proxy, which keep running**. Re-run the installer to bring NebulaCtrl back with all its data |
| `--purge` | Also deletes the database volume, `/opt/nebulactrl` (**including `NEBULACTRL_SECRET_KEY`**) and the agent state. Backups stay |
| `--remove-workloads` | Also removes every container NebulaCtrl manages (your apps, the proxy, helpers), the `nc-vol-*` volumes (**your apps' data**), proxy certificates and `nebulactrl-*` networks |
| `--remove-backups` | Also deletes `/var/backups/nebulactrl` |
| `--revert-host` | Reverts what the installer added: its ufw/firewalld rules (the firewall itself stays on), the fail2ban jail, and `/etc/docker/daemon.json` restored from its backup |

It **never** uninstalls Docker, automatic updates or the hardening fixes, and never touches SSH. Before `--purge`, take a final backup if you may want the data back, and remember a dump is unreadable without the secret key.

Everything the uninstaller does is covered by the installer's test suite: default uninstall, reinstall over the kept data (same secrets, same data, the host re-enrolls with its kept identity), and `--purge --remove-workloads --remove-backups` leaving no container, network, volume or file behind.

## An additional server

```sh
# stops the service, removes the unit, env file and binary; keeps the identity and your containers
curl -fsSL https://nebula.example.com/install-agent.sh | sudo sh -s -- --uninstall

# additionally delete /var/lib/nebulactrl-agent (identity, build workspaces). The host must be re-enrolled to be used again.
curl -fsSL https://nebula.example.com/install-agent.sh | sudo sh -s -- --uninstall --purge
```

(If the control plane is already gone, take the agent installer from any other running control plane (`/install-agent.sh`), or remove the pieces by hand: `systemctl disable --now nebulactrl-agent; rm /etc/systemd/system/nebulactrl-agent.service /etc/nebulactrl-agent.env /usr/local/bin/nebulactrl-agent; systemctl daemon-reload`.)

Uninstalling the agent **does not remove** what it created: the proxy and your services keep running. To delete everything NebulaCtrl created on such a host (this **destroys the data in service volumes**; export what you need first):

```sh
docker ps -aq --filter label=nebulactrl.managed=true | xargs -r docker rm -f
docker images --filter label=nebulactrl.managed=true -q | xargs -r docker rmi -f
docker rm -f nebulactrl-traefik nebulactrl-pages
docker network ls --format '{{.Name}}' | grep '^nebulactrl-' | xargs -r docker network rm
docker volume ls -q | grep -E '^(nc-vol-|nebulactrl-)' | xargs -r docker volume rm
```

Review each list (`docker ps -a`, `docker volume ls`) first; the commands match by label or name prefix, not by what you consider yours. Deleting a service in the UI with *delete volumes* ticked removes that service's volumes only. Revoking the server in the control plane first is optional; it only stops the agent and cancels its pending commands.

Your agents' services keep running after the control plane is gone, but nothing manages them, and agents keep retrying the connection until they are uninstalled.
