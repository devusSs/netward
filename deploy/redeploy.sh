#!/usr/bin/env bash
# Updates an installed netward from this checkout: pulls the latest changes,
# builds netward, installs it to /usr/local/bin and restarts the systemd
# service. Run it as your normal user on the machine that runs netward; it
# asks for sudo only to install the binary and restart the service.
#
# Usage: deploy/redeploy.sh [--no-pull]
#
#   --no-pull   build the checkout as it is, without running git pull
#
# The script does not touch /etc/netward/netward.yaml or the installed
# systemd unit. It warns when deploy/systemd/netward.service differs from the
# installed unit, so that you can review and copy it yourself.

set -euo pipefail

readonly BINARY=/usr/local/bin/netward
readonly UNIT=/etc/systemd/system/netward.service
readonly SERVICE=netward
# Seconds to wait after the restart before checking that netward still runs.
readonly STARTUP_WAIT=3

pull=true
for arg in "$@"; do
	case "$arg" in
	--no-pull) pull=false ;;
	-h | --help)
		sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "error: unknown argument: $arg" >&2
		exit 2
		;;
	esac
done

if [[ $EUID -eq 0 ]]; then
	echo "error: run this script as your normal user, it uses sudo where needed" >&2
	exit 1
fi

if ! command -v go >/dev/null 2>&1; then
	echo "error: go is not installed or not in PATH, see https://go.dev/doc/install" >&2
	exit 1
fi

cd "$(dirname "$0")/.."

if [[ $pull == true ]]; then
	echo "==> git pull"
	git pull --ff-only
fi

echo "==> building netward $(git describe --always --dirty)"
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$build_dir/netward" ./cmd/netward

echo "==> installing $BINARY"
sudo install -m 0755 "$build_dir/netward" "$BINARY"

if [[ -f $UNIT ]] && ! cmp -s deploy/systemd/netward.service "$UNIT"; then
	echo "warning: deploy/systemd/netward.service differs from $UNIT" >&2
	echo "         review it with: diff -u $UNIT deploy/systemd/netward.service" >&2
	echo "         then copy it and run: sudo systemctl daemon-reload" >&2
fi

echo "==> restarting $SERVICE"
sudo systemctl restart "$SERVICE"
sleep "$STARTUP_WAIT"

if ! systemctl is-active --quiet "$SERVICE"; then
	echo "error: $SERVICE is not running after the restart, its last log lines:" >&2
	sudo journalctl -u "$SERVICE" -n 30 --no-pager >&2
	exit 1
fi

echo "==> $SERVICE is running, follow its logs with: journalctl -u $SERVICE -f"
