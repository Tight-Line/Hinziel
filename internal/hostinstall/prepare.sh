# Prepares a host for a factory: Docker, Sysbox if SYSBOX=1, the hinziel user
# and the factory's directories. Safe to run again. Set by the caller:
# FACTORY, SYSBOX, SYSBOX_VERSION, FORCE (1 = restart Docker even with
# containers running).
set -eu
export DEBIAN_FRONTEND=noninteractive

if ! command -v docker >/dev/null 2>&1; then
	echo "installing Docker" >&2
	curl -fsSL https://get.docker.com | sh >/dev/null
fi
if [ "$SYSBOX" = 1 ] && ! docker info --format '{{range $name, $_ := .Runtimes}}{{$name}} {{end}}' | grep -qw sysbox-runc; then
	running=$(docker ps -q | wc -l)
	if [ "$running" -gt 0 ] && [ "$FORCE" != 1 ]; then
		echo "Sysbox isn't installed, and installing it restarts Docker, which stops the $running container(s) running here. Run again with --force to go ahead." >&2
		exit 3
	fi
	. /etc/os-release
	if [ "$ID" != ubuntu ] && [ "$ID" != debian ]; then
		echo "Sysbox isn't installed, and only Ubuntu and Debian get it automatically; install it yourself: https://github.com/nestybox/sysbox" >&2
		exit 3
	fi
	echo "installing Sysbox $SYSBOX_VERSION" >&2
	deb=/tmp/sysbox-ce_$SYSBOX_VERSION.deb
	curl -fsSL -o "$deb" "https://github.com/nestybox/sysbox/releases/download/v$SYSBOX_VERSION/sysbox-ce_$SYSBOX_VERSION.linux_$(dpkg --print-architecture).deb"
	apt-get install -y -qq "$deb" >/dev/null
	rm -f "$deb"
fi

id hinziel >/dev/null 2>&1 || useradd --system --user-group --create-home --home-dir /var/lib/hinziel --shell /usr/sbin/nologin hinziel
# The configuration is readable only by root and the factory; the state is
# the factory's alone. The agent's sandboxes, if any, get none of it but
# their run's checkouts.
install -d -o root -g hinziel -m 750 /etc/hinziel /etc/hinziel/"$FACTORY" /etc/hinziel/"$FACTORY"/files
install -d -o hinziel -g hinziel -m 750 /var/lib/hinziel
install -d -o hinziel -g hinziel -m 700 /var/lib/hinziel/"$FACTORY" /var/lib/hinziel/"$FACTORY"/bin /var/lib/hinziel/"$FACTORY"/home /var/lib/hinziel/"$FACTORY"/state
echo "$(id -u hinziel) $(id -g hinziel) $(stat -c %g /var/run/docker.sock) $(uname -m)"
