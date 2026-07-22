#!/bin/sh
# End-to-end check of the systemd integration, run from a host with Docker:
# autostart on writes and enables the user unit, the daemon auto-commits,
# stop/start go through systemctl, and autostart off cleans up.
set -eu

here=$(CDPATH= cd "$(dirname "$0")" && pwd)
arch=$(docker version --format '{{.Server.Arch}}')
case "$arch" in
  amd64) goarch=amd64 ;;
  arm64) goarch=arm64 ;;
  *) echo "unsupported docker arch $arch" >&2; exit 1 ;;
esac

echo "== building gitwatchd for linux/$goarch"
(cd "$here/.." && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -o /tmp/gitwatchd-systemd-test .)

echo "== building the systemd image"
docker build -q -t gitwatchd-systemd -f "$here/Dockerfile.systemd" "$here"

docker rm -f gw-systemd >/dev/null 2>&1 || true
docker run -d --name gw-systemd --privileged --cgroupns=host \
  --tmpfs /run --tmpfs /run/lock -v /sys/fs/cgroup:/sys/fs/cgroup \
  -v /tmp/gitwatchd-systemd-test:/usr/local/bin/gitwatchd:ro \
  gitwatchd-systemd >/dev/null
trap 'docker rm -f gw-systemd >/dev/null' EXIT

echo "== waiting for systemd"
for i in $(seq 1 30); do
  if docker exec gw-systemd systemctl is-system-running >/dev/null 2>&1; then break; fi
  sleep 1
done

echo "== starting a user session for dev"
uid=$(docker exec gw-systemd id -u dev)
docker exec gw-systemd loginctl enable-linger dev
for i in $(seq 1 30); do
  if docker exec gw-systemd test -S "/run/user/$uid/bus" 2>/dev/null; then break; fi
  sleep 1
done

as_dev() {
  docker exec -u dev \
    -e XDG_RUNTIME_DIR=/run/user/$uid \
    -e DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus \
    -e HOME=/home/dev -e USER=dev \
    gw-systemd "$@"
}

echo "== creating a repo and watching it"
as_dev sh -c 'mkdir -p ~/notes && cd ~/notes && git init -q -b main'
as_dev sh -c 'cd ~/notes && gitwatchd -s 0 .'

echo "== autostart on"
as_dev gitwatchd autostart on
as_dev test -f /home/dev/.config/systemd/user/gitwatchd.service
test "$(as_dev systemctl --user is-enabled gitwatchd)" = "enabled"
test "$(as_dev systemctl --user is-active gitwatchd)" = "active"
as_dev gitwatchd autostart status

echo "== the daemon auto-commits a change"
as_dev sh -c 'echo hello > ~/notes/hello.txt'
for i in $(seq 1 30); do
  n=$(as_dev sh -c 'cd ~/notes && git rev-list --count HEAD 2>/dev/null' || echo 0)
  if [ "$n" = "1" ]; then break; fi
  sleep 1
done
test "$n" = "1"
as_dev sh -c 'cd ~/notes && git log -1 --pretty=%s' | grep -q "gitwatchd auto-commit"
as_dev gitwatchd status | grep -q "notes · main"

echo "== stop and start go through systemctl"
as_dev gitwatchd stop
test "$(as_dev systemctl --user is-active gitwatchd || true)" = "inactive"
as_dev gitwatchd start
test "$(as_dev systemctl --user is-active gitwatchd)" = "active"

echo "== the journal is the log"
as_dev journalctl --user -u gitwatchd --no-pager | grep -q "watching notes"

echo "== the daemon survives a unit restart and still commits"
as_dev sh -c 'echo more >> ~/notes/hello.txt'
for i in $(seq 1 30); do
  n=$(as_dev sh -c 'cd ~/notes && git rev-list --count HEAD')
  if [ "$n" = "2" ]; then break; fi
  sleep 1
done
test "$n" = "2"

echo "== autostart off cleans up"
as_dev gitwatchd autostart off
if as_dev test -f /home/dev/.config/systemd/user/gitwatchd.service; then
  echo "unit file should be removed" >&2; exit 1
fi

echo "PASS: systemd integration works end to end"
