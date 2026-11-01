#!/usr/bin/env bash
# NebulaCtrl uninstaller.
#
#   sudo bash /opt/nebulactrl/uninstall.sh [options]
#   curl -fsSL https://<host>/uninstall.sh | sudo bash -s -- [options]
#
# By default it stops NebulaCtrl and keeps everything that holds data:
#   - stops and removes the control plane, Postgres and the built-in S3, registry and
#     log store containers (their data, the database volume, /opt/nebulactrl with its
#     secrets, and the backups stay)
#   - stops and removes the agent service and binary (its identity stays)
#   - leaves the containers, volumes and proxy of the apps you deployed running
#   - never uninstalls Docker, the firewall rules, fail2ban, automatic updates or
#     the hardening fixes (--revert-host reverts what the installer added)
#
# Options:
#   --purge             Also delete the control-plane database volume, the built-in S3,
#                       registry and log store data (every team's backups and images),
#                       /opt/nebulactrl (including the secret key), the agent state and
#                       its identity. This destroys the control plane's data; the local
#                       control-plane backups are kept.
#   --remove-workloads  Also remove every container NebulaCtrl manages (your deployed
#                       apps, the proxy, helpers), the nc-vol-* volumes (YOUR APPS' DATA),
#                       the proxy certificates and nebulactrl-* networks. Asks to type
#                       the word "delete" unless --yes is given.
#   --remove-backups    Also delete the local backup directory.
#   --revert-host       Revert host changes the installer made: remove the ufw/firewalld
#                       rules it added, the fail2ban jail, the automatic-update settings
#                       it wrote and restore /etc/docker/daemon.json from its backup.
#   --yes               Do not ask for confirmation.
#   -h, --help          This text.
set -Eeuo pipefail

INSTALL_DIR=${NEBULACTRL_INSTALL_DIR:-/opt/nebulactrl}
BACKUP_HOST_DIR=/var/backups/nebulactrl
AGENT_STATE_DIR=/var/lib/nebulactrl-agent
PURGE=0
REMOVE_WORKLOADS=0
REMOVE_BACKUPS=0
REVERT_HOST=0
ASSUME_YES=0

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
usage() {
	cat <<'EOT'
Usage: uninstall.sh [--purge] [--remove-workloads] [--remove-backups] [--revert-host] [--yes]

  (default)           Stop NebulaCtrl; keep the database volume, the built-in S3, registry and
                      log store data, /opt/nebulactrl (secrets), the agent identity, the
                      backups, your deployed apps and their data.
  --purge             Also delete the control-plane database volume, the built-in S3, registry
                      and log store data (every team's backups and images), /opt/nebulactrl
                      (including the secret key) and the agent state. Local control-plane
                      backups are kept.
  --remove-workloads  Also remove every container NebulaCtrl manages (your apps, the proxy),
                      the nc-vol-* volumes (YOUR APPS' DATA), proxy certificates and networks.
  --remove-backups    Also delete the local backup directory.
  --revert-host       Revert what the installer added: firewall rules, the fail2ban jail,
                      and /etc/docker/daemon.json (restored from its backup).
  --yes               Do not ask for confirmation.
Docker itself, automatic updates and hardening fixes are never uninstalled.
EOT
}

while [ $# -gt 0 ]; do
	case "$1" in
	--purge) PURGE=1 ;;
	--remove-workloads) REMOVE_WORKLOADS=1 ;;
	--remove-backups) REMOVE_BACKUPS=1 ;;
	--revert-host) REVERT_HOST=1 ;;
	--yes | -y) ASSUME_YES=1 ;;
	--dir) shift; INSTALL_DIR=${1:?--dir needs a value} ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
	shift
done

[ "$(id -u)" = 0 ] || die "must run as root"
have() { command -v "$1" >/dev/null 2>&1; }
ENV_FILE=$INSTALL_DIR/.env
COMPOSE_FILE=$INSTALL_DIR/docker-compose.yml
STATE_DIR=$INSTALL_DIR/state
if [ -r "$INSTALL_DIR/install.conf" ]; then
	# shellcheck disable=SC1091
	. "$INSTALL_DIR/install.conf" 2>/dev/null || true
fi

say "NebulaCtrl uninstall"
say "  control plane + Postgres containers : removed"
say "  built-in S3, registry, log store     : removed (their data: $([ "$PURGE" = 1 ] && echo 'DELETED, every team backup and image included' || echo kept))"
say "  database volume + $INSTALL_DIR      : $([ "$PURGE" = 1 ] && echo 'DELETED' || echo kept)"
say "  agent service                        : removed ($([ "$PURGE" = 1 ] && echo 'identity DELETED' || echo 'identity kept'))"
say "  deployed apps, volumes, proxy        : $([ "$REMOVE_WORKLOADS" = 1 ] && echo 'DELETED (including app data)' || echo 'kept running')"
say "  local backups                        : $([ "$REMOVE_BACKUPS" = 1 ] && echo DELETED || echo kept)"
say "  host changes (firewall, fail2ban...) : $([ "$REVERT_HOST" = 1 ] && echo reverted || echo kept)"
if [ "$ASSUME_YES" = 0 ]; then
	if [ ! -t 0 ] && [ ! -r /dev/tty ]; then die "no terminal to confirm on; pass --yes"; fi
	if [ "$REMOVE_WORKLOADS" = 1 ] || [ "$PURGE" = 1 ]; then
		printf 'This cannot be undone. Type "delete" to continue: '
		read -r answer </dev/tty || answer=''
		[ "$answer" = delete ] || die "aborted"
	else
		printf 'Continue? [y/N] '
		read -r answer </dev/tty || answer=''
		case "$answer" in y | Y | yes) ;; *) die "aborted" ;; esac
	fi
fi

DAEMON_MODIFIED=0
[ -e "$STATE_DIR/daemon-json-modified" ] && DAEMON_MODIFIED=1
dc() { docker compose -p nebulactrl -f "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@"; }

# 1. The control plane and its database.
if have docker && [ -r "$COMPOSE_FILE" ] && [ -r "$ENV_FILE" ]; then
	say "Stopping the control plane ..."
	if [ "$PURGE" = 1 ]; then dc down --volumes --remove-orphans -t 40 >/dev/null 2>&1 || true; else dc down --remove-orphans -t 40 >/dev/null 2>&1 || true; fi
elif have docker; then
	docker rm -f nebulactrl-nebulactrl-1 nebulactrl-postgres-1 nebulactrl-seaweedfs-1 nebulactrl-registry-1 nebulactrl-loki-1 >/dev/null 2>&1 || true
	[ "$PURGE" = 1 ] && docker volume rm nebulactrl_postgres-data nebulactrl_s3-data nebulactrl_registry-data nebulactrl_loki-data >/dev/null 2>&1 || true
fi
# The built-in services' data in --instance-dir is a host directory: compose does not own it.
if [ "$PURGE" = 1 ] && [ -n "${INSTANCE_DIR:-}" ] && [ -d "$INSTANCE_DIR" ]; then
	rm -rf "$INSTANCE_DIR/s3" "$INSTANCE_DIR/registry" "$INSTANCE_DIR/loki"
fi

# 2. The agent on this host.
if have systemctl; then
	systemctl disable --now nebulactrl-agent >/dev/null 2>&1 || true
fi
rm -rf /etc/systemd/system/nebulactrl-agent.service /etc/systemd/system/nebulactrl-agent.service.d /etc/nebulactrl-agent.env /usr/local/bin/nebulactrl-agent
have systemctl && systemctl daemon-reload >/dev/null 2>&1 || true
say "Agent removed."
if [ "$PURGE" = 1 ]; then rm -rf "$AGENT_STATE_DIR"; fi

# 3. Workloads.
if [ "$REMOVE_WORKLOADS" = 1 ] && have docker; then
	say "Removing managed containers, volumes and networks ..."
	ids=$(docker ps -aq --filter label=nebulactrl.managed=true 2>/dev/null || true)
	[ -z "$ids" ] || docker rm -f $ids >/dev/null 2>&1 || true
	docker rm -f nebulactrl-traefik nebulactrl-pages nebulactrl-cloudflared nebulactrl-tailscale >/dev/null 2>&1 || true
	vols=$(docker volume ls -q 2>/dev/null | grep -E '^(nc-vol-|nebulactrl-)' || true)
	[ -z "$vols" ] || docker volume rm $vols >/dev/null 2>&1 || true
	nets=$(docker network ls --format '{{.Name}}' 2>/dev/null | grep -E '^nebulactrl-' || true)
	[ -z "$nets" ] || docker network rm $nets >/dev/null 2>&1 || true
fi

# 4. Files.
if [ "$PURGE" = 1 ]; then
	cd / && rm -rf "$INSTALL_DIR"
	say "Deleted $INSTALL_DIR."
else
	rm -f "$STATE_DIR/backup-seeded" 2>/dev/null || true
	say "Kept $INSTALL_DIR (secrets, compose file). Re-run the installer to bring NebulaCtrl back."
fi
if [ "$REMOVE_BACKUPS" = 1 ]; then rm -rf "$BACKUP_HOST_DIR"; say "Deleted $BACKUP_HOST_DIR."; fi

# 5. Host changes.
if [ "$REVERT_HOST" = 1 ]; then
	say "Reverting host changes ..."
	if have ufw && ufw status 2>/dev/null | grep -q '^Status: active'; then
		ufw status numbered 2>/dev/null | grep -F 'NebulaCtrl' | sed -E 's/^\[ *([0-9]+)\].*/\1/' | sort -rn | while read -r n; do
			ufw --force delete "$n" >/dev/null 2>&1 || true
		done
		say "  removed the ufw rules the installer added (ufw itself stays enabled)"
	fi
	if have firewall-cmd && firewall-cmd --state >/dev/null 2>&1; then
		for p in 80/tcp 443/tcp 443/udp; do firewall-cmd -q --permanent --remove-port="$p" 2>/dev/null || true; done
		firewall-cmd -q --reload 2>/dev/null || true
		say "  removed the firewalld ports 80/443"
	fi
	if [ -e /etc/fail2ban/jail.d/nebulactrl-sshd.local ]; then
		rm -f /etc/fail2ban/jail.d/nebulactrl-sshd.local
		systemctl restart fail2ban >/dev/null 2>&1 || true
		say "  removed the fail2ban jail"
	fi
	if [ -e /etc/docker/daemon.json.nebulactrl-bak ]; then
		cp -p /etc/docker/daemon.json.nebulactrl-bak /etc/docker/daemon.json
		rm -f /etc/docker/daemon.json.nebulactrl-bak
		say "  restored /etc/docker/daemon.json (restart Docker to apply: systemctl restart docker)"
	elif [ "$DAEMON_MODIFIED" = 1 ] && [ -e /etc/docker/daemon.json ]; then
		say "  /etc/docker/daemon.json was created by the installer; remove it yourself if you do not want it"
	fi
fi

say "Done. Docker, your apps' images and Docker's own data are untouched."
