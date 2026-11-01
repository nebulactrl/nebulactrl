# shellcheck shell=bash
# Host hardening step of the installer: install Lynis, audit the host, apply the
# curated safe fixes, audit again.
#
# This file only defines a function (no side effects when sourced, no exit) and is
# inlined into the install script the control plane serves. The work is done by
# the agent binary, so the installer and the dashboard's Security tab run exactly
# the same code:
#
#   nebulactrl-agent hardening run
#
# That installs Lynis from CISOfy's package repository (the distribution's package
# when the repository is not reachable), prints the hardening index, applies the
# default fixes of internal/lynis (sysctl hardening, core dumps off, umask 027,
# cron permissions, unused protocols, filesystems and FireWire blocked, a legal
# banner, password-ageing defaults for new accounts), audits again and prints the
# index after. Every fix is verified after it is made and undone by itself when it
# did not take effect, and can be undone later with
#
#   nebulactrl-agent hardening rollback --fix <id>[,<id>...]   (list: hardening list)
#
# It never touches SSH settings, firewall rules or user accounts: there is no fix
# for them in the catalog. The step is non-fatal; a failure only warns.
#
# Inputs (exported by install.sh): NC_AGENT_BIN (path of nebulactrl-agent),
# NC_NO_HARDENING (1 skips), NC_DRY_RUN (1 only says what would happen).
nc_hardening_step() {
	local bin=${NC_AGENT_BIN:-/usr/local/bin/nebulactrl-agent}
	_nc_h_say() {
		if [ "$(type -t nc_info 2>/dev/null)" = function ]; then nc_info "$*"; else printf '        %s\n' "$*"; fi
	}
	_nc_h_warn() {
		if [ "$(type -t nc_warn 2>/dev/null)" = function ]; then nc_warn "$*"; else printf 'warn %s\n' "$*" >&2; fi
	}

	if [ "${NC_NO_HARDENING:-0}" = 1 ]; then
		_nc_h_say "hardening skipped (--no-hardening)"
		return 0
	fi
	if [ "${NC_DRY_RUN:-0}" = 1 ]; then
		_nc_h_say "would run: $bin hardening run (Lynis audit, safe fixes, audit again)"
		return 0
	fi
	if [ "$(id -u)" != 0 ]; then
		_nc_h_warn "hardening needs root; skipped"
		return 1
	fi
	if [ ! -x "$bin" ]; then
		_nc_h_warn "the agent binary $bin is not installed; the hardening step was skipped (use the server's Security tab later)"
		return 1
	fi
	if ! "$bin" hardening run; then
		_nc_h_warn "the hardening step failed; a fix that did not complete was undone. Run '$bin hardening run' again, or use the server's Security tab"
		return 1
	fi
	_nc_h_say "undo any fix with: $bin hardening rollback --fix <id>   (ids: $bin hardening list)"
	return 0
}
