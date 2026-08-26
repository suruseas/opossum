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
      # `ls` without --format json prints the same table `list` does. The two
      # spellings of one question must not answer with different machines.
      # `network ls --format json` is how doctor finds the networks nothing is
      # running on. $NETWORK_LS is the document to answer with; the default is a
      # machine holding only the runtime's own `default`.
      ls)
        # Without `--format json` the real CLI prints a table. Answering JSON
        # either way would hide a caller that forgot the flag.
        case "$*" in
          *"--format json"*)
            # A brace inside ${VAR:-...} ends the expansion, so the default is
            # built first and substituted plainly.
            default_nets='[{"configuration":{"name":"default","labels":{"com.apple.container.resource.role":"builtin"}}}]'
            printf '%s\n' "${NETWORK_LS:-$default_nets}" ;;
          *) printf 'NETWORK  SUBNET\ndefault  192.168.64.0/24\n' ;;
        esac
        ;;
    esac
    ;;
  ls)
    # `ls -a --format json` is the container listing. $CONTAINER_LS is the
    # document. Two flags matter and both are honoured, because a caller that
    # forgets either gets a wrong answer on a real machine and must get one
    # here: `--format json` or it is a table, and `-a` or only what is running.
    #
    # Without `-a` this answers the empty list, not "the running ones" — a shell
    # script has no JSON parser to filter with. So $CONTAINER_LS is read as a
    # document of stopped entries, which is what this shim is for: showing that
    # a caller who forgot `-a` sees less. The Go shim next door filters
    # properly. Anything needing the running half belongs there.
    #
    # The flag is looked for anywhere in the arguments, not at a fixed position:
    # `ls -a --format json` and `ls --format json -a` are the same request, and
    # a shim that only answers one of them tests the argument order rather than
    # the flag.
    case "$*" in
      *"--format json"*)
        case " $* " in
          *" -a "*) printf '%s\n' "${CONTAINER_LS:-[]}" ;;
          *)        printf '[]\n' ;;
        esac ;;
      *) printf 'ID  IMAGE  OS  ARCH  STATE\n' ;;
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
    # configuration.networks is here too, holding a different name from the one
    # under status, so that reading the wrong one of the two is a thing this
    # fixture can show rather than a thing it agrees with.
    cat <<'JSON'
[{"status":{"state":"running","networks":[{"network":"demo-net","ipv4Address":"192.168.64.10/24","ipv6Address":"fdee:0:0:0::10/64","ipv4Gateway":"192.168.64.1"}]},"configuration":{"networks":[{"network":"demo-net-configured"}],"publishedPorts":[{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}]}}]
JSON
    ;;
  *) echo "fake-container: unknown command $1" >&2; exit 0 ;;
esac
