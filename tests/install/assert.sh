#!/usr/bin/env bash
# Assertions for tests/install/run.sh. Runs as root inside the machine under test:
#   assert.sh <phase> <machine-ip>
# phases: fresh rerun firstrun domain apps uninstalled reinstalled purged sidebyside moveback bind
set -uo pipefail

PHASE=${1:?phase}
IP=${2:?ip}
DIR=/opt/nebulactrl
ENV=$DIR/.env
JAR=/tmp/nc-cookies
FAILS=0
PASSES=0

ok() { PASSES=$((PASSES + 1)); printf '  PASS  %s\n' "$*"; }
bad() { FAILS=$((FAILS + 1)); printf '  FAIL  %s\n' "$*"; }
check() { # check <description> <command...>
	local d=$1; shift
	if "$@" >/tmp/assert.out 2>&1; then ok "$d"; else bad "$d: $(head -c 300 /tmp/assert.out | tr '\n' ' ')"; fi
}
env_get() { grep -m1 "^$1=" "$ENV" 2>/dev/null | cut -d= -f2-; }
eq() { [ "$1" = "$2" ] || { echo "got '$1', want '$2'"; return 1; }; }
wait_for() { # wait_for <seconds> <command...>
	local n=$1; shift
	local i
	for i in $(seq 1 "$n"); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
	"$@"
}
api() { # api <method> <path> [json] -> body; status in $STATUS
	local m=$1 p=$2 body=${3:-} out
	if [ -n "$body" ]; then
		out=$(curl -s -o /tmp/api.body -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$m" -H 'Content-Type: application/json' ${IFMATCH:+-H "If-Match: $IFMATCH"} -d "$body" "$BASE/api/v1$p")
	else
		out=$(curl -s -o /tmp/api.body -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$m" ${IFMATCH:+-H "If-Match: $IFMATCH"} "$BASE/api/v1$p")
	fi
	STATUS=$out
	cat /tmp/api.body
}
jq_() { # tiny JSON field reader without jq: jq_ <field> reads stdin
	python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get(sys.argv[1], ""))' "$1" 2>/dev/null
}
have_py() { command -v python3 >/dev/null; }
ensure_py() { have_py || { (apt-get install -y -qq python3 >/dev/null 2>&1 || dnf install -y -q python3 >/dev/null 2>&1); }; }

PORT=$(env_get NEBULACTRL_PORT)
export PGPASSWORD=$(env_get POSTGRES_PASSWORD)
BASE=http://127.0.0.1:${PORT:-8000}

phase_fresh() {
	[ "$(id -u)" = 0 ] || bad "not root"
	check "install dir files" bash -c "[ -f $DIR/docker-compose.yml ] && [ -f $ENV ] && [ -x $DIR/uninstall.sh ] && [ -f $DIR/install.conf ]"
	check ".env is mode 0600 root" bash -c "[ \"\$(stat -c '%a %U' $ENV)\" = '600 root' ]"
	check "secret key is 32 base64 bytes" bash -c "[ \"\$(grep -m1 '^NEBULACTRL_SECRET_KEY=' $ENV | cut -d= -f2- | base64 -d | wc -c)\" = 32 ]"
	check "postgres password is 48 hex" bash -c "grep -Eq '^POSTGRES_PASSWORD=[0-9a-f]{48}\$' $ENV"
	check "local enrollment token shape" bash -c "grep -Eq '^NEBULACTRL_LOCAL_ENROLL_TOKEN=nct_enroll_[a-z2-7]{52}\$' $ENV"
	check "setup token present" bash -c "[ -n \"\$(grep -m1 '^NEBULACTRL_SETUP_TOKEN=' $ENV | cut -d= -f2-)\" ]"

	check "docker >= 24 and compose plugin" bash -c "docker version -f '{{.Server.Version}}' | awk -F. '{exit !(\$1>=24)}' && docker compose version"
	check "both containers healthy" bash -c "[ \"\$(docker inspect -f '{{.State.Health.Status}}' nebulactrl-postgres-1)\" = healthy ] && [ \"\$(docker inspect -f '{{.State.Health.Status}}' nebulactrl-nebulactrl-1)\" = healthy ]"
	check "postgres publishes no port" bash -c "[ \"\$(docker inspect -f '{{json .NetworkSettings.Ports}}' nebulactrl-postgres-1 | tr -d ' ')\" = '{}' ] || docker inspect -f '{{range \$p,\$b := .NetworkSettings.Ports}}{{if \$b}}X{{end}}{{end}}' nebulactrl-postgres-1 | grep -qv X"
	check "postgres is not listening on the host" bash -c "! ss -H -ltn | awk '{print \$4}' | grep -Eq '(^|:)5432\$'"
	check "postgres is only on the internal backend network" bash -c "[ \"\$(docker inspect -f '{{range \$n,\$_ := .NetworkSettings.Networks}}{{\$n}} {{end}}' nebulactrl-postgres-1)\" = 'nebulactrl_backend ' ] && [ \"\$(docker network inspect -f '{{.Internal}}' nebulactrl_backend)\" = true ]"
	check "control plane is on the edge network (alias nebulactrl)" bash -c "docker inspect -f '{{json .NetworkSettings.Networks}}' nebulactrl-nebulactrl-1 | grep -q nebulactrl-edge"
	check "edge network exists with the managed label" bash -c "[ \"\$(docker network inspect -f '{{index .Labels \"nebulactrl.managed\"}}' nebulactrl-edge)\" = true ]"
	check "control plane has no capabilities, read-only root" bash -c "[ \"\$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}}' nebulactrl-nebulactrl-1)\" = 'true [ALL]' ]"
	check "SCRAM password encryption" bash -c "[ \"\$(docker exec -e PGPASSWORD nebulactrl-postgres-1 psql -U nebulactrl -d nebulactrl -tA -c 'show password_encryption')\" = scram-sha-256 ]"
	check "pg_hba has no trust/md5/password rule" bash -c "[ \"\$(docker exec -e PGPASSWORD nebulactrl-postgres-1 psql -U nebulactrl -d nebulactrl -tA -c \"select count(*) from pg_hba_file_rules where auth_method in ('trust','md5','password')\")\" = 0 ]"
	check "postgres tuned (shared_buffers from .env)" bash -c "want=\$(grep -m1 '^PG_SHARED_BUFFERS=' $ENV | cut -d= -f2-); got=\$(docker exec -e PGPASSWORD nebulactrl-postgres-1 psql -U nebulactrl -d nebulactrl -tA -c 'show shared_buffers'); [ \"\$got\" = \"\$(echo \$want | sed 's/MB\$/MB/')\" ] || [ \"\$got\" = \"\$(( \${want%MB} / 1024 ))GB\" ]"
	check "data checksums enabled" bash -c "[ \"\$(docker exec -e PGPASSWORD nebulactrl-postgres-1 psql -U nebulactrl -d nebulactrl -tA -c 'show data_checksums')\" = on ]"

	check "dashboard answers /readyz on the IP" bash -c "curl -fsS http://$IP:$PORT/readyz"
	check "IP mode public URL" bash -c "[ \"\$(grep -m1 '^NEBULACTRL_PUBLIC_URL=' $ENV | cut -d= -f2-)\" = http://$IP:$PORT ]"
	check "setup status says a token is required" bash -c "curl -fsS $BASE/api/v1/setup | grep -q '\"tokenRequired\":true'"
	check "setup without the token is refused (403)" bash -c "[ \"\$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{\"email\":\"a@example.com\",\"password\":\"correct horse battery\",\"name\":\"A\"}' $BASE/api/v1/setup)\" = 403 ]"

	check "daemon.json has the safe defaults" bash -c "grep -q '\"live-restore\"' /etc/docker/daemon.json && grep -q 'userland-proxy' /etc/docker/daemon.json && grep -q 'max-size' /etc/docker/daemon.json && grep -q '10.212.0.0/16' /etc/docker/daemon.json"
	check "docker runs with live-restore and json-file logging" bash -c "[ \"\$(docker info -f '{{.LiveRestoreEnabled}} {{.LoggingDriver}}')\" = 'true json-file' ]"
	check "docker compose network uses the default pool" bash -c "docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' nebulactrl_backend | grep -q '^10.212\\.'"

	check "agent installed and running" bash -c "[ -x /usr/local/bin/nebulactrl-agent ] && systemctl is-active --quiet nebulactrl-agent && [ -f /etc/systemd/system/nebulactrl-agent.service.d/10-installer.conf ]"
	check "agent dials the loopback address" bash -c "grep -q -- '--url http://127.0.0.1:$PORT' /etc/systemd/system/nebulactrl-agent.service"
	check "agent is enrolled (identity stored, bootstrap team created)" wait_for 90 test -s /var/lib/nebulactrl-agent/agent.db
	check "proxy container runs on 80/443" wait_for 90 bash -c "docker inspect -f '{{.State.Running}}' nebulactrl-traefik | grep -q true && ss -H -ltn | awk '{print \$4}' | grep -Eq '(^|:)443\$'"

	check "backup directory is private to the control plane user" bash -c "[ \"\$(stat -c '%a %u' /var/backups/nebulactrl)\" = '700 65532' ]"
	check "daily control-plane backup is scheduled in the database" bash -c "[ \"\$(docker exec -e PGPASSWORD nebulactrl-postgres-1 psql -U nebulactrl -d nebulactrl -tA -c \"select enabled::text||dest_kind||local_dir||schedule from instance_backup_settings\")\" = 'truelocalcontrol-plane30 3 * * *' ]"
	check "first backup written within 3 minutes" wait_for 180 bash -c "find /var/backups/nebulactrl/control-plane -name '*.dump' | grep -q . && find /var/backups/nebulactrl/control-plane -name '*.manifest.json' | grep -q ."

	if [ -f /etc/debian_version ]; then
		check "ufw active: ssh, 80, 443/tcp, 443/udp, dashboard port" bash -c "s=\$(ufw status); echo \"\$s\" | grep -q 'Status: active' && echo \"\$s\" | grep -Eq '^22/tcp +ALLOW' && echo \"\$s\" | grep -Eq '^80/tcp +ALLOW' && echo \"\$s\" | grep -Eq '^443/tcp +ALLOW' && echo \"\$s\" | grep -Eq '^443/udp +ALLOW' && echo \"\$s\" | grep -Eq '^$PORT/tcp +ALLOW'"
		check "unattended-upgrades enabled" bash -c "grep -q 'Unattended-Upgrade \"1\"' /etc/apt/apt.conf.d/20auto-upgrades"
	else
		check "firewalld: ssh, 80, 443/tcp, 443/udp, dashboard port" bash -c "p=\$(firewall-cmd --list-ports); echo \"\$p\" | grep -q '80/tcp' && echo \"\$p\" | grep -q '443/tcp' && echo \"\$p\" | grep -q '443/udp' && echo \"\$p\" | grep -q '$PORT/tcp' && firewall-cmd --list-all | grep -Eq 'ssh|22/tcp'"
		check "dnf-automatic timer enabled for security updates" bash -c "systemctl is-enabled dnf-automatic.timer && grep -Eq '^upgrade_type *= *security' /etc/dnf/automatic.conf && grep -Eq '^apply_updates *= *yes' /etc/dnf/automatic.conf"
	fi
	check "fail2ban watches sshd" bash -c "systemctl is-active --quiet fail2ban && fail2ban-client status sshd | grep -q 'Currently failed'"
}

# After the installer's Lynis step: Docker networking, Postgres and the app path survived the
# sysctl, module and permission changes, and the fixes are recorded.
phase_hardened() {
	check "the fixes are on disk (sysctl and modprobe drop-ins, loaded sysctls)" bash -c "ls /etc/sysctl.d/90-nebulactrl-*.conf >/dev/null && ls /etc/modprobe.d/nebulactrl-*.conf >/dev/null && [ \"\$(sysctl -n kernel.dmesg_restrict)\" = 1 ]"
	check "the agent can list and roll back fixes" bash -c "/usr/local/bin/nebulactrl-agent hardening list | grep -q '^FIX'"
	check "ip forwarding is still on (containers keep their network)" bash -c "[ \"\$(sysctl -n net.ipv4.ip_forward)\" = 1 ]"
	check "bridge traffic still passes (br_netfilter/iptables chains intact)" bash -c "docker network inspect nebulactrl-edge >/dev/null && docker run --rm --pull=never --network nebulactrl-edge nebulactrl:instlocal /usr/local/bin/nebulactrl-server --help >/dev/null 2>&1 || true"
	check "control plane reaches Postgres (readyz checks the database)" bash -c "curl -fsS $BASE/readyz"
	check "control plane container still healthy" bash -c "[ \"\$(docker inspect -f '{{.State.Health.Status}}' nebulactrl-nebulactrl-1)\" = healthy ]"
	check "a fresh container reaches the internet and another container by name" bash -c "docker run --rm --pull=never --network nebulactrl_backend --entrypoint /bin/sh nebulactrl:instlocal -c 'true' && getent hosts download.docker.com >/dev/null"
	check "containers on the internal network still reach Postgres" bash -c "docker exec nebulactrl-nebulactrl-1 /usr/local/bin/nebulactrl-server healthcheck"
	check "the agent is online and the proxy runs" wait_for 60 bash -c "systemctl is-active --quiet nebulactrl-agent && docker inspect -f '{{.State.Running}}' nebulactrl-traefik | grep -q true"
	check "docker daemon.json is still the installer's" bash -c "grep -q live-restore /etc/docker/daemon.json && docker info >/dev/null"
	check "the ssh service state is what it was before the installer" bash -c "now=\$(for u in ssh sshd ssh.socket; do echo \$u=\$(systemctl is-active \$u 2>&1 | head -1); done); [ \"\$now\" = \"\$(cat /tmp/ssh.state)\" ]"
}

phase_rerun() {
	check ".env unchanged by a re-run" cmp /tmp/env.before "$ENV"
	check "no container was recreated" bash -c "[ \"\$(docker inspect -f '{{.Id}}' nebulactrl-nebulactrl-1 nebulactrl-postgres-1)\" = \"\$(cat /tmp/cids.before)\" ]"
	check "still healthy" bash -c "curl -fsS $BASE/readyz"
	check "agent still enrolled and running" bash -c "systemctl is-active --quiet nebulactrl-agent && [ -s /var/lib/nebulactrl-agent/agent.db ]"
}

phase_firstrun() {
	ensure_py
	local tok; tok=$(env_get NEBULACTRL_SETUP_TOKEN)
	rm -f "$JAR"
	check "setup with a wrong token is refused" bash -c "[ \"\$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{\"email\":\"a@example.com\",\"password\":\"correct horse battery\",\"name\":\"A\",\"setupToken\":\"nope\"}' $BASE/api/v1/setup)\" = 403 ]"
	api POST /setup "{\"email\":\"admin@example.com\",\"password\":\"correct horse battery\",\"name\":\"Admin\",\"teamName\":\"Install Test\",\"setupToken\":\"$tok\"}" >/dev/null
	check "setup with the token creates the admin (201)" eq "$STATUS" 201
	api GET /setup >/dev/null
	check "setup is closed afterwards" bash -c "grep -q '\"needsSetup\":false' /tmp/api.body"
	check "setup again is 409" bash -c "[ \"\$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{\"email\":\"b@example.com\",\"password\":\"correct horse battery\",\"name\":\"B\"}' $BASE/api/v1/setup)\" = 409 ]"
	# The local agent enrolled with the bootstrap team: the admin sees exactly one team and the server in it.
	local servers=
	wait_for 120 bash -c "curl -s -b $JAR $BASE/api/v1/servers | python3 -c 'import json,sys; d=json.load(sys.stdin)[\"items\"]; sys.exit(0 if any(s[\"controlPlane\"] and s[\"status\"]==\"online\" for s in d) else 1)'"
	servers=$(api GET /servers)
	echo "$servers" | python3 -c '
import json,sys
d=json.load(sys.stdin)["items"]
cp=[s for s in d if s["controlPlane"]]
assert len(cp)==1, d
s=cp[0]
assert s["status"]=="online", s["status"]
assert s["acceptWorkloads"] is True
assert s["facts"] and s["facts"]["dockerVersion"], s["facts"]
print("server", s["name"], s["status"], "proxy", s["proxy"]["status"], "agent", s["agentVersion"])
' >/tmp/assert.out 2>&1 && ok "the control-plane host is the first server: online, controlPlane, acceptWorkloads ($(tail -1 /tmp/assert.out))" || bad "control-plane server: $(cat /tmp/assert.out)"
	echo "$servers" | python3 -c 'import json,sys; s=[x for x in json.load(sys.stdin)["items"] if x["controlPlane"]][0]; print(s["id"])' >/tmp/cp-server-id 2>/dev/null
	api GET /me >/dev/null
	check "the admin acts in the adopted, renamed team" bash -c "grep -q 'Install Test\\|\"role\":\"owner\"' /tmp/api.body"
}

phase_domain() {
	ensure_py
	local url; url=$(env_get NEBULACTRL_PUBLIC_URL)
	check "public URL is https://nebula.test" eq "$url" https://nebula.test
	check "dashboard upstream set" eq "$(env_get NEBULACTRL_DASHBOARD_UPSTREAM)" nebulactrl:8080
	check "secrets unchanged by the switch" bash -c "for k in POSTGRES_PASSWORD NEBULACTRL_SECRET_KEY NEBULACTRL_LOCAL_ENROLL_TOKEN NEBULACTRL_SETUP_TOKEN; do [ \"\$(grep -m1 \"^\$k=\" /tmp/env.before)\" = \"\$(grep -m1 \"^\$k=\" $ENV)\" ] || { echo \$k changed; exit 1; }; done"
	check "control plane published on loopback 8080 only" bash -c "ss -H -ltn | awk '{print \$4}' | grep -qx '127.0.0.1:8080' && ! ss -H -ltn | awk '{print \$4}' | grep -Eq '(^|[:*])8000\$'"
	check "status upstream set" eq "$(env_get NEBULACTRL_STATUS_UPSTREAM)" nebulactrl:8081
	check "status listener (8081) is not published on the host" bash -c "! ss -H -ltn | awk '{print \$4}' | grep -Eq '(^|[:*])8081\$'"
	check "agent now dials 127.0.0.1:8080" wait_for 30 bash -c "grep -q -- '--url http://127.0.0.1:8080' /etc/systemd/system/nebulactrl-agent.service && systemctl is-active --quiet nebulactrl-agent"
	check "the server is online again" wait_for 120 bash -c "curl -s -b $JAR http://127.0.0.1:8080/api/v1/servers | python3 -c 'import json,sys; sys.exit(0 if any(s[\"controlPlane\"] and s[\"status\"]==\"online\" for s in json.load(sys.stdin)[\"items\"]) else 1)'"
	check "the dashboard is served by the host proxy at https://nebula.test" wait_for 150 bash -c "[ \"\$(curl -sk --resolve nebula.test:443:127.0.0.1 -o /dev/null -w '%{http_code}' https://nebula.test/healthz)\" = 200 ]"
	check "the SPA is served through the proxy" bash -c "curl -sk --resolve nebula.test:443:127.0.0.1 https://nebula.test/ | grep -qi '<html'"
	check "http redirects to https" bash -c "c=\$(curl -s --resolve nebula.test:80:127.0.0.1 -o /dev/null -w '%{http_code}' http://nebula.test/); [ \"\$c\" = 301 ] || [ \"\$c\" = 302 ] || [ \"\$c\" = 307 ] || [ \"\$c\" = 308 ]"
	check "the API works through the proxy (login)" bash -c "curl -sk --resolve nebula.test:443:127.0.0.1 -c /tmp/cj2 -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{\"email\":\"admin@example.com\",\"password\":\"correct horse battery\"}' https://nebula.test/api/v1/auth/login | grep -q 200"
	check "the agent websocket endpoint upgrades through the proxy" bash -c "[ \"\$(curl -sk --resolve nebula.test:443:127.0.0.1 -o /dev/null -w '%{http_code}' -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' https://nebula.test/agent/v1/connect)\" != 502 ]"
	BASE=http://127.0.0.1:8080
	local sid; sid=$(cat /tmp/cp-server-id 2>/dev/null)
	check "the route table names the dashboard route" bash -c "curl -s -b $JAR $BASE/api/v1/servers/$sid/proxy/routes | grep -q 'NebulaCtrl dashboard'"
}

phase_apps() {
	ensure_py
	BASE=http://127.0.0.1:8080
	local sid; sid=$(cat /tmp/cp-server-id)
	local pid env svc gen dep st
	pid=$(api POST /projects '{"name":"assert-shop"}' | jq_ id)
	env=$(api GET "/projects/$pid/environments" | python3 -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["id"])')
	svc=$(api POST "/environments/$env/services" "{\"name\":\"whoami\",\"sourceType\":\"image\",\"serverId\":\"$sid\",\"config\":{\"image\":\"traefik/whoami:latest\",\"port\":80}}" | jq_ id)
	check "a service can be placed on the control-plane host" test -n "$svc"
	gen=$(api GET "/services/$svc" | python3 -c 'import json,sys; print(json.load(sys.stdin)["generation"])')
	IFMATCH="\"$gen\"" api POST "/services/$svc/domains" '{"hostname":"whoami.nebula.test"}' >/dev/null
	IFMATCH=''
	check "domain added" eq "$STATUS" 201
	dep=$(api POST "/services/$svc/deployments" '{}' | jq_ id)
	check "deployment accepted" test -n "$dep"
	wait_for 240 bash -c "curl -s -b $JAR $BASE/api/v1/deployments/$dep | python3 -c 'import json,sys; s=json.load(sys.stdin)[\"status\"]; sys.exit(0 if s in (\"succeeded\",\"failed\",\"rolled_back\") else 1)'"
	st=$(api GET "/deployments/$dep" | jq_ status)
	check "deployment succeeded on this host" eq "$st" succeeded
	check "the app answers through the same proxy" wait_for 60 bash -c "curl -sk --resolve whoami.nebula.test:443:127.0.0.1 https://whoami.nebula.test/ | grep -q Hostname"
	check "the dashboard route survived the deploy's route push" bash -c "[ \"\$(curl -sk --resolve nebula.test:443:127.0.0.1 -o /dev/null -w '%{http_code}' https://nebula.test/healthz)\" = 200 ]"

	api PATCH "/servers/$sid" '{"acceptWorkloads":false}' >/dev/null
	check "turn Accept workloads off" eq "$STATUS" 200
	api POST "/environments/$env/services" "{\"name\":\"second\",\"sourceType\":\"image\",\"serverId\":\"$sid\",\"config\":{\"image\":\"traefik/whoami:latest\",\"port\":80}}" >/dev/null
	check "a new service on it is refused (422)" eq "$STATUS" 422
	check "refusal names the setting" bash -c "grep -q server_not_accepting_workloads /tmp/api.body"
	check "what already runs keeps serving" bash -c "curl -sk --resolve whoami.nebula.test:443:127.0.0.1 https://whoami.nebula.test/ | grep -q Hostname"
	api PATCH "/servers/$sid" '{"acceptWorkloads":true}' >/dev/null
	check "turn it back on" eq "$STATUS" 200
}

phase_uninstalled() {
	check "control plane and database containers are gone" bash -c "! docker ps -a --format '{{.Names}}' | grep -Eq '^nebulactrl-(nebulactrl|postgres)-1\$'"
	check "agent service, binary and drop-in are gone" bash -c "[ ! -e /usr/local/bin/nebulactrl-agent ] && [ ! -e /etc/systemd/system/nebulactrl-agent.service ] && [ ! -e /etc/systemd/system/nebulactrl-agent.service.d ]"
	check "the agent identity is kept" test -s /var/lib/nebulactrl-agent/agent.db
	check "the database volume and the secrets are kept" bash -c "docker volume inspect nebulactrl_postgres-data >/dev/null && [ -f $ENV ]"
	check "deployed apps keep running" bash -c "docker ps --format '{{.Names}}' | grep -q '^nc-whoami-'"
	check "the proxy keeps running" bash -c "docker inspect -f '{{.State.Running}}' nebulactrl-traefik | grep -q true"
	check "the installer's firewall rules were reverted" bash -c "if command -v ufw >/dev/null; then ! ufw status | grep -q NebulaCtrl; else ! firewall-cmd --list-ports | grep -q 443/udp; fi"
	check "the fail2ban jail was removed" bash -c "[ ! -e /etc/fail2ban/jail.d/nebulactrl-sshd.local ]"
	check "local backups are kept" bash -c "find /var/backups/nebulactrl -name '*.dump' | grep -q ."
	check "Docker itself is untouched" bash -c "docker info >/dev/null"
}

phase_reinstalled() {
	ensure_py
	BASE=http://127.0.0.1:8080
	check "secrets are the ones from before" bash -c "for k in POSTGRES_PASSWORD NEBULACTRL_SECRET_KEY NEBULACTRL_LOCAL_ENROLL_TOKEN; do [ \"\$(grep -m1 \"^\$k=\" /tmp/env.before)\" = \"\$(grep -m1 \"^\$k=\" $ENV)\" ] || { echo \$k changed; exit 1; }; done"
	check "the data survived: setup is done" bash -c "curl -fsS $BASE/api/v1/setup | grep -q '\"needsSetup\":false'"
	check "the data survived: the project is still there" bash -c "curl -s -b $JAR $BASE/api/v1/projects | grep -q assert-shop"
	check "the kept agent identity enrolls again as the same server" wait_for 120 bash -c "curl -s -b $JAR $BASE/api/v1/servers | python3 -c 'import json,sys; d=json.load(sys.stdin)[\"items\"]; sys.exit(0 if len(d)==1 and d[0][\"controlPlane\"] and d[0][\"status\"]==\"online\" else 1)'"
	check "the dashboard is served by the host proxy again" wait_for 90 bash -c "[ \"\$(curl -sk --resolve nebula.test:443:127.0.0.1 -o /dev/null -w '%{http_code}' https://nebula.test/healthz)\" = 200 ]"
	check "the app still answers" bash -c "curl -sk --resolve whoami.nebula.test:443:127.0.0.1 https://whoami.nebula.test/ | grep -q Hostname"
}

phase_purged() {
	check "installation directory is gone" bash -c "[ ! -e $DIR ]"
	check "database volume is gone" bash -c "! docker volume inspect nebulactrl_postgres-data >/dev/null 2>&1"
	check "no managed container is left" bash -c "[ -z \"\$(docker ps -aq --filter label=nebulactrl.managed=true)\" ] && ! docker ps -a --format '{{.Names}}' | grep -q nebulactrl-"
	check "no NebulaCtrl network, nc-vol volume or proxy volume is left" bash -c "! docker network ls --format '{{.Name}}' | grep -q '^nebulactrl-' && ! docker volume ls -q | grep -Eq '^(nc-vol-|nebulactrl-)'"
	check "agent state is gone" bash -c "[ ! -e /var/lib/nebulactrl-agent ]"
	check "backups are gone" bash -c "[ ! -e /var/backups/nebulactrl ]"
	check "Docker is still installed and running" bash -c "docker info >/dev/null"
}

# shellcheck source=assert-sidebyside.sh
. "$(dirname "${BASH_SOURCE[0]}")/assert-sidebyside.sh"

"phase_$PHASE"
printf '\n%s: %d passed, %d failed\n' "$PHASE" "$PASSES" "$FAILS"
[ "$FAILS" = 0 ]
