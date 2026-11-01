#!/usr/bin/env bash
# Phases of tests/install/assert.sh for the side-by-side scenario (tests/install/sidebyside.sh):
# sidebyside, moveback, bind. Sourced by assert.sh, which provides check, api, eq, wait_for, ...

# Through NebulaCtrl's proxy on the given HTTPS port, as a client on this machine.
proxied() { local port=$1; shift; curl -sk --resolve "nebula.test:$port:127.0.0.1" "$@"; }
code_of() { local port=$1 path=$2; proxied "$port" -o /dev/null -w '%{http_code}' "https://nebula.test:$port$path"; }
want_code() { [ "$(code_of "$1" "$2")" = "$3" ]; }

export -f proxied code_of want_code

phase_sidebyside() {
	ensure_py
	export NC_CANARY=${NC_CANARY:?}
	BASE=http://127.0.0.1:8100
	local tok sid
	check ".env is 0600 root" bash -c "[ \"\$(stat -c '%a %U' $ENV)\" = '600 root' ]"
	check "the dashboard URL carries the HTTPS port" eq "$(env_get NEBULACTRL_PUBLIC_URL)" https://nebula.test:8443
	check "control plane: loopback only, on --port 8100 (8080 belongs to the proxy)" bash -c "[ \"\$(grep -m1 '^NEBULACTRL_BIND=' $ENV | cut -d= -f2-)\" = 127.0.0.1 ] && [ \"\$(grep -m1 '^NEBULACTRL_PORT=' $ENV | cut -d= -f2-)\" = 8100 ] && docker port nebulactrl-nebulactrl-1 | grep -qx '8080/tcp -> 127.0.0.1:8100'"
	check "the dashboard settings were passed to the control plane" bash -c "[ \"\$(grep -m1 '^NEBULACTRL_DASHBOARD_ALLOW_CIDRS=' $ENV | cut -d= -f2-)\" = '100.64.0.0/10,fd7a:115c:a1e0::/48' ] && [ \"\$(grep -m1 '^NEBULACTRL_DASHBOARD_DNS_RESOLVER=' $ENV | cut -d= -f2-)\" = cloudflare ] && [ -n \"\$(grep -m1 '^NEBULACTRL_DASHBOARD_DNS_TOKEN=' $ENV | cut -d= -f2-)\" ]"
	check "install.conf remembers the layout and never holds the token" bash -c "grep -q '^PROXY_HTTP_PORT=8080' $DIR/install.conf && grep -q '^PROXY_HTTPS_PORT=8443' $DIR/install.conf && grep -q '^DASHBOARD_DNS01=cloudflare' $DIR/install.conf && ! grep -rqF -- \"\$NC_CANARY\" $DIR/install.conf $DIR/INSTALL-SUMMARY.txt"
	check "the agent's unit carries the proxy ports and the loopback URL" wait_for 60 bash -c "grep -q -- '--proxy-http-port 8080 --proxy-https-port 8443' /etc/systemd/system/nebulactrl-agent.service && grep -q -- '--url http://127.0.0.1:8100' /etc/systemd/system/nebulactrl-agent.service && systemctl is-active --quiet nebulactrl-agent"
	check "the proxy runs on 8080/8443 and not on 80/443" wait_for 120 bash -c "docker inspect -f '{{.State.Running}}' nebulactrl-traefik | grep -q true && p=\$(docker port nebulactrl-traefik | awk '{print \$NF}') && echo \"\$p\" | grep -Eq ':8080\$' && echo \"\$p\" | grep -Eq ':8443\$' && ! echo \"\$p\" | grep -Eq ':(80|443)\$'"
	check "the other proxy still answers on 80, 443 and 8000" bash -c 'for p in 80 443 8000; do curl -fsS http://127.0.0.1:$p/ | grep -q coolify-proxy || exit 1; done'

	tok=$(env_get NEBULACTRL_SETUP_TOKEN)
	rm -f "$JAR"
	api POST /setup "{\"email\":\"admin@example.com\",\"password\":\"correct horse battery\",\"name\":\"Admin\",\"teamName\":\"Side By Side\",\"setupToken\":\"$tok\"}" >/dev/null
	check "setup through the loopback port creates the admin" eq "$STATUS" 201
	wait_for 150 bash -c "curl -s -b $JAR $BASE/api/v1/servers | python3 -c 'import json,sys; sys.exit(0 if any(s[\"controlPlane\"] and s[\"status\"]==\"online\" for s in json.load(sys.stdin)[\"items\"]) else 1)'"
	sid=$(api GET /servers | python3 -c 'import json,sys; print([s for s in json.load(sys.stdin)["items"] if s["controlPlane"]][0]["id"])')
	check "the host's agent enrolled over the loopback URL and is online" test -n "$sid"

	# The dashboard access settings the installer seeded.
	check "dashboard access: seeded allow list and DNS-01, resolver applied by the proxy" wait_for 300 bash -c "curl -s -b $JAR $BASE/api/v1/instance/dashboard-access | python3 -c '
import json,sys
d=json.load(sys.stdin)
assert d[\"enabled\"] and d[\"host\"]==\"nebula.test\", d
assert d[\"allowCidrs\"]==[\"100.64.0.0/10\",\"fd7a:115c:a1e0::/48\"], d[\"allowCidrs\"]
assert d[\"dnsResolver\"]==\"cloudflare\", d[\"dnsResolver\"]
assert any(r[\"provider\"]==\"cloudflare\" and r[\"applied\"] for r in d[\"resolverAvailable\"]), d[\"resolverAvailable\"]
assert not d[\"proxyDeployRequired\"], d
'"
	check "the dashboard route refuses a client outside the allow list (403)" wait_for 240 want_code 8443 /healthz 403
	check "plain HTTP on the spare port answers (the route needs no port 80)" bash -c "c=\$(curl -s -o /dev/null -w '%{http_code}' --resolve nebula.test:8080:127.0.0.1 http://nebula.test:8080/healthz); [ \"\$c\" != 000 ]"

	# Editing the allow list: effective at once, audited, guarded against locking yourself out.
	local open='["127.0.0.0/8","10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","100.64.0.0/10"]'
	api PUT /instance/dashboard-access "{\"allowCidrs\":$open,\"dnsResolver\":\"cloudflare\"}" >/dev/null
	check "the instance admin saves a new allow list" eq "$STATUS" 200
	check "the new list is live on the proxy without a restart" wait_for 90 want_code 8443 /healthz 200
	api GET "/audit-events?limit=100" >/dev/null
	check "the change is audited" bash -c "grep -q 'dashboard.access.update' /tmp/api.body"
	rm -f /tmp/cj3
	check "login works through the proxy" bash -c "[ \"\$(proxied 8443 -c /tmp/cj3 -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{\"email\":\"admin@example.com\",\"password\":\"correct horse battery\"}' https://nebula.test:8443/api/v1/auth/login)\" = 200 ]"
	check "through the route the API sees the real client address" bash -c "proxied 8443 -b /tmp/cj3 https://nebula.test:8443/api/v1/instance/dashboard-access | python3 -c '
import json,sys
d=json.load(sys.stdin)
assert d[\"viaDashboardRoute\"] is True, d
assert d[\"clientIpAllowed\"] is True, d
assert d[\"clientIp\"], d
print(d[\"clientIp\"])'"
	local lock='{"allowCidrs":["203.0.113.0/24"],"dnsResolver":"cloudflare"}'
	check "a list that excludes the caller is refused (409 dashboard_lockout)" bash -c "c=\$(proxied 8443 -b /tmp/cj3 -o /tmp/lock.body -w '%{http_code}' -X PUT -H 'Content-Type: application/json' -d '$lock' https://nebula.test:8443/api/v1/instance/dashboard-access); [ \"\$c\" = 409 ] && grep -q dashboard_lockout /tmp/lock.body"
	check "...and nothing changed" want_code 8443 /healthz 200
	check "with confirmLockout it is saved" bash -c "[ \"\$(proxied 8443 -b /tmp/cj3 -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: application/json' -d '{\"allowCidrs\":[\"203.0.113.0/24\"],\"dnsResolver\":\"cloudflare\",\"confirmLockout\":true}' https://nebula.test:8443/api/v1/instance/dashboard-access)\" = 200 ]"
	check "the locked-out client now gets 403" wait_for 90 want_code 8443 /healthz 403
	api PUT /instance/dashboard-access "{\"allowCidrs\":$open,\"dnsResolver\":\"cloudflare\"}" >/dev/null
	check "the loopback port still reaches the control plane to repair the list" eq "$STATUS" 200
	check "and the dashboard opens again" wait_for 90 want_code 8443 /healthz 200

	# DNS-01: the token is sealed in the credential store and shown nowhere.
	check "a sealed cloudflare credential exists" bash -c "[ \"\$(docker exec -e PGPASSWORD nebulactrl-postgres-1 psql -U nebulactrl -d nebulactrl -tA -c \"select count(*) from dns_credentials where provider='cloudflare'\")\" -ge 1 ]"
	check "the server's cloudflare resolver is applied" bash -c "curl -s -b $JAR $BASE/api/v1/servers/$sid/dns01 | python3 -c '
import json,sys
d=json.load(sys.stdin)
r=[x for x in d[\"resolvers\"] if x[\"provider\"]==\"cloudflare\"]
assert r and r[0][\"change\"]==\"none\", d[\"resolvers\"]'"
	check "the token is in no control-plane, agent or proxy log" bash -c "! docker logs nebulactrl-nebulactrl-1 2>&1 | grep -qF -- \"\$NC_CANARY\" && ! journalctl -u nebulactrl-agent --no-pager 2>&1 | grep -qF -- \"\$NC_CANARY\" && ! docker logs nebulactrl-traefik 2>&1 | grep -qF -- \"\$NC_CANARY\""
	check "the token is in no database row in clear (a full dump holds no trace)" bash -c "! docker exec -e PGPASSWORD nebulactrl-postgres-1 pg_dump -U nebulactrl nebulactrl | grep -qF -- \"\$NC_CANARY\""
	check "no API read returns the token" bash -c "for p in /dns/credentials '/audit-events?limit=200' /instance/dashboard-access /servers /servers/$sid/dns01 /servers/$sid/proxy; do curl -s -b $JAR $BASE/api/v1\$p | grep -qF -- \"\$NC_CANARY\" && { echo \"leak in \$p\"; exit 1; }; done; true"

	# An app next to the other proxy: answers on :8443, and the HTTP-01 limit is flagged.
	local pid env svc gen dep st
	pid=$(api POST /projects '{"name":"sbs-shop"}' | jq_ id)
	env=$(api GET "/projects/$pid/environments" | python3 -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["id"])')
	svc=$(api POST "/environments/$env/services" "{\"name\":\"whoami\",\"sourceType\":\"image\",\"serverId\":\"$sid\",\"config\":{\"image\":\"traefik/whoami:latest\",\"port\":80}}" | jq_ id)
	check "a service can be placed on this host" test -n "$svc"
	gen=$(api GET "/services/$svc" | python3 -c 'import json,sys; print(json.load(sys.stdin)["generation"])')
	IFMATCH="\"$gen\"" api POST "/services/$svc/domains" '{"hostname":"whoami.nebula.test"}' >/dev/null
	IFMATCH=''
	check "a domain on HTTP-01 is accepted" eq "$STATUS" 201
	check "the response warns that HTTP-01 cannot work off port 80" bash -c "grep -q 'HTTP-01' /tmp/api.body"
	check "the deployment plan warns too" bash -c "curl -s -b $JAR -X POST $BASE/api/v1/services/$svc/deployments/plan -H 'Content-Type: application/json' -d '{}' | grep -q 'HTTP-01'"
	gen=$(api GET "/services/$svc" | python3 -c 'import json,sys; print(json.load(sys.stdin)["generation"])')
	IFMATCH="\"$gen\"" api POST "/services/$svc/domains" '{"hostname":"dns01.nebula.test","dnsResolver":"cloudflare"}' >/dev/null
	IFMATCH=''
	check "a domain on the DNS-01 resolver is accepted" eq "$STATUS" 201
	check "...without an HTTP-01 warning" bash -c "! grep -q 'HTTP-01' /tmp/api.body"
	dep=$(api POST "/services/$svc/deployments" '{}' | jq_ id)
	check "deployment accepted" test -n "$dep"
	wait_for 240 bash -c "curl -s -b $JAR $BASE/api/v1/deployments/$dep | python3 -c 'import json,sys; s=json.load(sys.stdin)[\"status\"]; sys.exit(0 if s in (\"succeeded\",\"failed\",\"rolled_back\") else 1)'"
	st=$(api GET "/deployments/$dep" | jq_ status)
	if [ "$st" != succeeded ]; then
		echo "  INFO  deployment: $(api GET "/deployments/$dep" | head -c 1500)"
		echo "  INFO  deployment logs: $(api GET "/deployments/$dep/logs" | head -c 3000)"
	fi
	check "deployment succeeded" eq "$st" succeeded
	check "the app answers on the spare HTTPS port" wait_for 60 bash -c "curl -sk --resolve whoami.nebula.test:8443:127.0.0.1 https://whoami.nebula.test:8443/ | grep -q Hostname"
	check "the dashboard route survived the deploy's route push (allow list intact)" want_code 8443 /healthz 200
}

# After the other proxy stopped and the installer moved NebulaCtrl's proxy to 80/443.
phase_moveback() {
	ensure_py
	export NC_CANARY=${NC_CANARY:?}
	BASE=http://127.0.0.1:8100
	check "the dashboard URL lost its port" eq "$(env_get NEBULACTRL_PUBLIC_URL)" https://nebula.test
	check "install.conf holds the default ports" bash -c "grep -q '^PROXY_HTTP_PORT=80\$' $DIR/install.conf && grep -q '^PROXY_HTTPS_PORT=443\$' $DIR/install.conf"
	check "the unit no longer carries port flags" wait_for 30 bash -c "! grep -q -- '--proxy-http-port' /etc/systemd/system/nebulactrl-agent.service && systemctl is-active --quiet nebulactrl-agent"
	check "the proxy was recreated on 80/443" wait_for 120 bash -c "docker inspect -f '{{.State.Running}}' nebulactrl-traefik | grep -q true && p=\$(docker port nebulactrl-traefik | awk '{print \$NF}') && echo \"\$p\" | grep -Eq ':80\$' && echo \"\$p\" | grep -Eq ':443\$' && ! echo \"\$p\" | grep -Eq ':(8080|8443)\$'"
	check "the control plane stayed on 127.0.0.1:8100" bash -c "docker port nebulactrl-nebulactrl-1 | grep -qx '8080/tcp -> 127.0.0.1:8100'"
	check "the control plane and the admin survived" bash -c "curl -fsS $BASE/readyz && curl -fsS $BASE/api/v1/setup | grep -q '\"needsSetup\":false'"
	check "the server is online again" wait_for 120 bash -c "curl -s -b $JAR $BASE/api/v1/servers | python3 -c 'import json,sys; sys.exit(0 if any(s[\"controlPlane\"] and s[\"status\"]==\"online\" for s in json.load(sys.stdin)[\"items\"]) else 1)'"
	check "the dashboard answers on the standard HTTPS port" wait_for 150 want_code 443 /healthz 200
	check "the app answers on 443 and its container was not recreated" wait_for 90 bash -c "curl -sk --resolve whoami.nebula.test:443:127.0.0.1 https://whoami.nebula.test/ | grep -q Hostname && docker ps --format '{{.Names}}' | grep -q '^nc-whoami-'"
	check "the allow list and the DNS-01 certificate method survived the move" bash -c "curl -s -b $JAR $BASE/api/v1/instance/dashboard-access | python3 -c '
import json,sys
d=json.load(sys.stdin)
assert d[\"dnsResolver\"]==\"cloudflare\" and len(d[\"allowCidrs\"])==5, d'"
	check "the token still appears nowhere" bash -c "! docker logs nebulactrl-nebulactrl-1 2>&1 | grep -qF -- \"\$NC_CANARY\" && ! journalctl -u nebulactrl-agent --no-pager 2>&1 | grep -qF -- \"\$NC_CANARY\""
}

# IP mode bound to one address (the stand-in for the Tailscale IP).
phase_bind() {
	ensure_py
	BASE=http://127.0.0.1:8100
	check "the dashboard URL is the bind address" eq "$(env_get NEBULACTRL_PUBLIC_URL)" "http://$IP:8100"
	check ".env keeps the bind address" eq "$(env_get NEBULACTRL_BIND)" "$IP"
	check "install.conf remembers it" bash -c "grep -q \"^BIND=$IP\$\" $DIR/install.conf"
	check "published on the bind address and on loopback for the agent, not on 0.0.0.0" bash -c "p=\$(docker port nebulactrl-nebulactrl-1 | sort); echo \"\$p\" | grep -qx '8080/tcp -> $IP:8100' && echo \"\$p\" | grep -qx '8080/tcp -> 127.0.0.1:8100' && ! echo \"\$p\" | grep -q '0.0.0.0'"
	check "the override file and COMPOSE_FILE exist" bash -c "[ -f $DIR/docker-compose.loopback.yml ] && grep -q '^COMPOSE_FILE=docker-compose.yml:docker-compose.loopback.yml\$' $ENV"
	check "a plain docker compose in the install directory sees both" bash -c "cd $DIR && docker compose -p nebulactrl --env-file .env config | grep -c 'published: \"8100\"' | grep -qx 2"
	check "the dashboard answers on the bind address" bash -c "curl -fsS http://$IP:8100/readyz"
	check "the host's agent connects over loopback and is enrolled" wait_for 150 bash -c "grep -q -- '--url http://127.0.0.1:8100' /etc/systemd/system/nebulactrl-agent.service && systemctl is-active --quiet nebulactrl-agent && [ -s /var/lib/nebulactrl-agent/agent.db ]"
	check "no listener on another interface for the dashboard port" bash -c "! ss -H -ltn | awk '{print \$4}' | grep -Eq '^(0.0.0.0|\\*|\\[::\\]):8100\$'"
}
