set -e
LC_ALL=C
export LC_ALL
PATH="$PATH:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/lynis"
export PATH

if command -v lynis >/dev/null 2>&1; then
  echo "Lynis is already installed ($(lynis show version 2>/dev/null | head -n1))"
  exit 0
fi
[ "$(id -u)" = 0 ] || { echo "installing Lynis needs root"; exit 1; }

# CISOfy's own repository carries the current Lynis; the distributions ship older
# releases. Key and repository: https://packages.cisofy.com/community/
KEY_URL="https://packages.cisofy.com/keys/cisofy-software-public.key"

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  KEYRING=/usr/share/keyrings/cisofy-lynis.asc
  LIST=/etc/apt/sources.list.d/cisofy-lynis.list
  from_cisofy() {
    command -v curl >/dev/null 2>&1 || return 1
    tmp=$(mktemp)
    if ! curl -fsSL --max-time 60 "$KEY_URL" -o "$tmp" || ! head -n1 "$tmp" | grep -q 'BEGIN PGP PUBLIC KEY BLOCK'; then rm -f "$tmp"; return 1; fi
    mkdir -p /usr/share/keyrings
    cat "$tmp" > "$KEYRING"; rm -f "$tmp"; chmod 644 "$KEYRING"
    echo "deb [signed-by=$KEYRING] https://packages.cisofy.com/community/lynis/deb/ stable main" > "$LIST"
    # Refresh only this source: other repositories are none of our business here.
    apt-get -o Dir::Etc::sourcelist="$LIST" -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0 update -qq >/dev/null || return 1
    apt-get install -y -qq lynis >/dev/null || return 1
  }
  if from_cisofy; then
    echo "Installed Lynis from CISOfy's repository"
  else
    rm -f "$LIST" "$KEYRING"
    echo "CISOfy's repository was not reachable; installing the distribution's Lynis package"
    apt-get update -qq >/dev/null
    apt-get install -y -qq lynis >/dev/null
  fi
elif command -v dnf >/dev/null 2>&1; then
  cat > /etc/yum.repos.d/cisofy-lynis.repo <<EOF
[lynis]
name=CISOfy Software - Lynis package
baseurl=https://packages.cisofy.com/community/lynis/rpm/
enabled=1
gpgkey=https://packages.cisofy.com/keys/cisofy-software-rpms-public.key
gpgcheck=1
priority=2
EOF
  if dnf install -y -q lynis >/dev/null; then
    echo "Installed Lynis from CISOfy's repository"
  else
    rm -f /etc/yum.repos.d/cisofy-lynis.repo
    echo "CISOfy's repository was not reachable and this release has no Lynis package of its own"
    exit 1
  fi
else
  echo "no supported package manager (apt-get or dnf) to install Lynis"
  exit 1
fi

command -v lynis >/dev/null 2>&1 || { echo "Lynis is not on the PATH after installing"; exit 1; }
echo "Lynis $(lynis show version 2>/dev/null | head -n1)"
