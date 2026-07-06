#!/bin/sh
# A fake `container` CLI used to smoke-test opossum without the real runtime.
# It logs each invocation to $FAKE_LOG and returns output shaped like the real
# `container` 1.0.0 CLI (see testdata/real-cli-output.md for the captured
# reference these are kept in sync with). Overrides:
#   FAKE_DNS_DOMAIN   domain reported by `system dns list` (default: opossum)
echo "container $*" >> "${FAKE_LOG:-/dev/null}"

case "$1" in
  network)
    # Real CLI echoes just the network name on success (exit 0).
    case "$2" in
      create) echo "$3" ;;
      delete) echo "$3" ;;
      list)   printf 'NETWORK  SUBNET\ndefault  192.168.64.0/24\n' ;;
    esac
    ;;
  build)   echo "built image" ;;
  run)     echo "started container" ;;
  logs)    echo "fake log line for $*" ;;  # real CLI streams container stdout
  stop)    : ;;
  delete)  : ;;
  exec)    : ;;   # healthcheck probe: succeed (exit 0 = healthy)
  system)
    # `system dns list`: header + one domain per line, matching the real CLI.
    if [ "$2" = dns ] && [ "$3" = list ]; then
      printf 'DOMAIN\n%s\n' "${FAKE_DNS_DOMAIN:-opossum}"
    fi
    ;;
  inspect)
    # Mirror the real `container inspect` shape: the interface address lives
    # under status.networks[].ipv4Address, while a published port surfaces a
    # 0.0.0.0 hostAddress that must NOT be mistaken for the container's IP.
    cat <<'JSON'
[{"status":{"state":"running","networks":[{"network":"demo-net","ipv4Address":"192.168.64.10/24","ipv6Address":"fdee:0:0:0::10/64","ipv4Gateway":"192.168.64.1"}]},"configuration":{"publishedPorts":[{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]
JSON
    ;;
  *) echo "fake-container: unknown command $1" >&2; exit 0 ;;
esac
