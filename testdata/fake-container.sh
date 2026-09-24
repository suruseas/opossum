#!/bin/sh
# A fake `container` CLI used to smoke-test opossum without the real runtime.
# It logs each invocation to $FAKE_LOG and returns output shaped like the real
# `container` CLI of the version testdata/real-cli-output.md names at its top
# (the captured reference these are kept in sync with; the shapes here are a
# subset of that version's — keys the parsers never read are left out). Overrides:
#   FAKE_DNS_DOMAIN   domain reported by `system dns list` (default: opossum)
#   STATE_DIR         remember what `delete`, `stop` and `volume delete` did, so a
#                     later `inspect`, `stop` or `delete` answers as the real CLI
#                     does (stopped; not found, exit 1). Unset, every container is
#                     running and every delete succeeds, as before.
# The two Go shims under cmd/opossum/testdata and internal/orchestrator/testdata
# answer the same questions; internal/shimcontract runs one table through all
# three.
echo "container $*" >> "${FAKE_LOG:-/dev/null}"

# publishedJSON turns the `-p` specs a run was given into the objects the real
# `container inspect` answers with. A range comes back as ONE object holding the
# low port and a `count`, which is what the runtime does (measured on 1.4.1) —
# spelling it out here so a reader of these ports sees the same shape either way.
publishedJSON() {
  printf '%s\n' "$1" | tr ' ' '\n' | awk '
    $0 == "" { next }
    {
      spec = $0; proto = "tcp"
      i = index(spec, "/")
      if (i > 0) { proto = tolower(substr(spec, i + 1)); spec = substr(spec, 1, i - 1) }
      n = split(spec, f, ":")
      cport = f[n]; hport = (n >= 2) ? f[n - 1] : f[n]
      addr = "0.0.0.0"
      if (n >= 3) { addr = f[1]; for (j = 2; j <= n - 2; j++) addr = addr ":" f[j] }
      gsub(/^\[|\]$/, "", addr)
      count = 1
      if ((k = index(hport, "-")) > 0) {
        lo = substr(hport, 1, k - 1) + 0; hi = substr(hport, k + 1) + 0
        count = hi - lo + 1; hport = lo
      }
      if ((k = index(cport, "-")) > 0) cport = substr(cport, 1, k - 1)
      if (out != "") out = out ","
      out = out "{\"containerPort\":" cport + 0 ",\"count\":" count ",\"hostAddress\":\"" addr "\",\"hostPort\":" hport + 0 ",\"proto\":\"" proto "\"}"
    }
    END { print out }
  '
}

# marker KIND NAME: the file that records NAME as gone/stopped, or empty when
# nothing is being remembered.
marker() {
  [ -n "${STATE_DIR:-}" ] || return 0
  # The name in hex: folding `.` and `/` into `_` made `demo_.hid` and
  # `demo__hid` the same marker, where the runtime keeps them apart.
  printf '%s/%s=%s' "$STATE_DIR" "$1" "$(printf '%s' "$2" | od -An -tx1 | tr -d ' \n')"
}
# is_there NAME: whether a container of this name is there as far as this fake
# is concerned — one named in $INSPECT_ABSENT was never made, one the delete
# case marked gone is no longer there. Both answers have to reach every
# command, not only `inspect`: a fake that says "no such container" to one
# question and hands logs to the next lets a command that should have passed
# the service by look as though it worked (#1096).
is_there() {
  for m in ${INSPECT_ABSENT:-}; do
    [ "$1" = "$m" ] && return 1
  done
  g=$(marker gone "$1")
  if [ -n "$g" ] && [ -e "$g" ]; then return 1; fi
  return 0
}
# last argument
last() { for a in "$@"; do :; done; printf '%s' "$a"; }

case "$1" in
  network)
    # Real CLI echoes just the network name on success (exit 0).
    case "$2" in
      # container 1.4.1 refuses a name outside lower-case letters, digits, `.`,
      # `_` and `-`, starting and ending with a letter or digit, at most 63
      # characters (testdata/real-cli-output.md). opossum passes the name last,
      # so the last argument is read as the name; with none (or only a flag)
      # the real CLI exits 64. The letters are spelled out: `[a-z]` takes upper
      # case in some locales. printf, not echo: sh's echo expands backslashes.
      create)
        name=$(last "$@")
        case "$name" in
          create|-*) printf "Error: Missing expected argument '<name>'\n" >&2; exit 64 ;;
          ''|*[!abcdefghijklmnopqrstuvwxyz0123456789._-]*|[!abcdefghijklmnopqrstuvwxyz0123456789]*|*[!abcdefghijklmnopqrstuvwxyz0123456789])
            printf 'Error: invalid network name: %s\n' "$name" >&2; exit 1 ;;
        esac
        if [ "${#name}" -gt 63 ]; then printf 'Error: invalid network name: %s\n' "$name" >&2; exit 1; fi
        printf '%s\n' "$name" ;;
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
  run)
    # A `--name` container 1.4.1 would not create is refused before anything is
    # recorded (testdata/real-cli-output.md). The flags are read up to the image
    # (the first argument that does not start with `-`), skipping the value of
    # each flag that takes one (every flag but the ones listed, from `container
    # run --help`), so a `--name` among the command's arguments is left alone;
    # the last `--name` there counts. No value, or `-` followed by more, is a
    # missing value (exit 64); a name that is not 2 to 63 characters starting
    # with a letter or digit and holding only letters, digits, `_`, `.` and `-`
    # is not a valid container ID (exit 1). This reads the shapes opossum passes;
    # not read the way the real CLI reads them: `--name=<name>`, `-e=<value>`,
    # combined short flags, `--`, `-h`/`--help`/`--version`, `--debug`, a value
    # starting with `-` for another flag (the real CLI calls it missing, exit 64
    # — `--user -1`, #996), and a flag with no value at the end. The letters are
    # spelled out: a range can take other letters in some locales.
    name= given= want= n=0
    for a in "$@"; do
      n=$((n+1)); [ "$n" -eq 1 ] && continue  # "run"
      if [ -n "$want" ]; then
        if [ "$want" = name ]; then
          case "$a" in -?*) printf "Error: Missing value for '--name <name>'\n" >&2; exit 64 ;; esac
          name=$a; given=1
        fi
        want=; continue
      fi
      case "$a" in
        -d|--detach|-i|--interactive|-t|--tty|--init|--no-dns|--read-only|--rm|--remove|--rosetta|--ssh|--virtualization) : ;;
        --name) want=name ;;
        -*) want=value ;;
        *) break ;;
      esac
    done
    if [ "$want" = name ]; then printf "Error: Missing value for '--name <name>'\n" >&2; exit 64; fi
    if [ -n "$given" ]; then
      case "$name" in
        ''|?|[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789]*|*[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-]*)
          printf 'Error: container ID %s is not a valid container ID\n' "$name" >&2; exit 1 ;;
      esac
      if [ "${#name}" -gt 63 ]; then printf 'Error: container ID %s is not a valid container ID\n' "$name" >&2; exit 1; fi
    fi
    # A `-v <source>:<target>` whose source container 1.4.1 reads as a volume
    # name and refuses is refused next, before anything is recorded; the first
    # such `-v` before the image is named (testdata/real-cli-output.md). A source
    # holding `/` is a path, not a name. A name whose first character is not a
    # letter or digit, holding anything but letters, digits, `_`, `.` and `-`, or
    # longer than 255 characters is refused — so an empty source (an anonymous
    # volume to the runtime) has nothing refused. The
    # flags are walked as above, with the same forms unread; also unread are
    # `--volume`, `--mount` and a `-v` value with no `:`.
    want= n=0
    for a in "$@"; do
      n=$((n+1)); [ "$n" -eq 1 ] && continue  # "run"
      if [ -n "$want" ]; then
        if [ "$want" = volume ]; then
          case "$a" in
            *:*)
              src=${a%%:*} bad=
              case "$src" in
                */*) : ;;
                [!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789]*|*[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-]*) bad=1 ;;
                *) if [ "${#src}" -gt 255 ]; then bad=1; fi ;;
              esac
              if [ -n "$bad" ]; then
                printf "Error: invalid volume name '%s': must match ^[A-Za-z0-9][A-Za-z0-9_.-]*\$\n" "$src" >&2; exit 1
              fi ;;
          esac
        fi
        want=; continue
      fi
      case "$a" in
        -d|--detach|-i|--interactive|-t|--tty|--init|--no-dns|--read-only|--rm|--remove|--rosetta|--ssh|--virtualization) : ;;
        -v) want=volume ;;
        -*) want=value ;;
        *) break ;;
      esac
    done
    # Running a name makes it there and running again (see stop/delete), with
    # the project label this run gave it (none for a run without one), given
    # as `-l` (the spelling opossum passes) or `--label`.
    proj= pub= prev=
    for a in "$@"; do
      if [ "$prev" = -l ] || [ "$prev" = --label ]; then
        case "$a" in opossum.project=*) proj=${a#opossum.project=} ;; esac
      fi
      # The published ports this run asked for, remembered so that `inspect`
      # can answer with them. Without this the fixture answers the same ports
      # for every container, and a caller asking "which ports does THIS one
      # hold" gets a number nobody published — so a check that reads them is
      # measured against a constant rather than against what it was told.
      if [ "$prev" = -p ] || [ "$prev" = --publish ]; then
        pub="$pub $a"
      fi
      prev=$a
    done
    prev=
    for a in "$@"; do
      if [ "$prev" = --name ]; then
        m=$(marker gone "$a"); if [ -n "$m" ]; then rm -f "$m"; fi
        m=$(marker stopped "$a"); if [ -n "$m" ]; then rm -f "$m"; fi
        m=$(marker project "$a"); if [ -n "$m" ]; then printf '%s' "$proj" > "$m"; fi
        m=$(marker ports "$a"); if [ -n "$m" ]; then printf '%s' "$pub" > "$m"; fi
      fi
      prev=$a
    done
    echo "started container" ;;
  logs)
    # The real CLI streams container stdout. With $LOGS_SLEEP the stream stays
    # open that many seconds, as `container logs -f` does, and a SIGINT or
    # SIGTERM then ends it with 130 or 143 of its own, and a SIGHUP by the
    # signal (container 1.4.1). The sleep starts, and the traps are set, before
    # the line is written, so a signal right after the line finds both.
    # $LOGS_EMPTY names containers that have written nothing yet: the real CLI
    # ends at once, exit 0 and nothing written, unless it is asked to follow
    # with -n, when it follows the empty log (container 1.4.1).
    # A container that is not there has no logs (container 1.4.1): reading them
    # as empty would let a command that should have passed the service by look
    # as though it worked.
    n=$(last "$@")
    if ! is_there "$n"; then
      echo "Error: failed to get logs for container $n (cause: \"internalError: \"failed to open container logs: notFound: \"container with ID $n not found\"\"\")" >&2; exit 1
    fi
    line="fake log line for $*"
    case " ${LOGS_EMPTY:-} " in
      *" $(last "$@") "*)
        case " $* " in *" -f "*) ;; *) exit 0 ;; esac
        case " $* " in *" -n "*) line="" ;; *) exit 0 ;; esac ;;
    esac
    if [ -n "${LOGS_SLEEP:-}" ]; then
      sleep "$LOGS_SLEEP" & sp=$!
      trap 'kill "$sp" 2>/dev/null; exit 130' INT
      trap 'kill "$sp" 2>/dev/null; exit 143' TERM
      trap 'kill "$sp" 2>/dev/null; trap - HUP; kill -HUP $$' HUP
      if [ -n "$line" ]; then echo "$line"; fi
      wait "$sp"
    elif [ -n "$line" ]; then
      echo "$line"
    fi
    ;;
  stop)
    # The exit code answers only whether the name exists (container 1.4.1): 0
    # for a running, stopped or exited container, 1 when there is none.
    n=$(last "$@"); g=$(marker gone "$n")
    if [ -n "$g" ] && [ -e "$g" ]; then
      echo "Error: internalError: \"failed to stop container\" (cause: \"notFound: \"container with ID $n not found\"\")" >&2; exit 1
    fi
    s=$(marker stopped "$n"); if [ -n "$s" ]; then : > "$s"; fi
    ;;
  start)
    # As for stop: a name that is not there cannot be started (container 1.4.1).
    n=$(last "$@")
    if ! is_there "$n"; then
      echo "Error: get failed: container $n not found" >&2; exit 1
    fi
    s=$(marker stopped "$n"); if [ -n "$s" ]; then rm -f "$s"; fi
    ;;
  delete|rm)
    n=$(last "$@"); g=$(marker gone "$n")
    if [ -n "$g" ] && [ -e "$g" ]; then
      echo "Error: internalError: \"failed to delete container\" (cause: \"notFound: \"container with ID $n not found\"\")" >&2; exit 1
    fi
    if [ -n "$g" ]; then : > "$g"; fi
    s=$(marker stopped "$n"); if [ -n "$s" ]; then rm -f "$s"; fi
    ;;
  volume)
    if [ "$2" = delete ] || [ "$2" = rm ]; then
      g=$(marker volgone "$3")
      if [ -n "$g" ] && [ -e "$g" ]; then
        echo "Error: failed to delete one or more volumes: [\"$3\"]" >&2; exit 1
      fi
      if [ -n "$g" ]; then : > "$g"; fi
      echo "$3"
    fi
    ;;
  exec)    : ;;   # healthcheck probe: succeed (exit 0 = healthy)
  system)
    # `system dns list`: header + one domain per line, matching the real CLI.
    if [ "$2" = dns ] && [ "$3" = list ]; then
      printf 'DOMAIN\n%s\n' "${FAKE_DNS_DOMAIN:-opossum}"
    fi
    # `system status`: the daemon-liveness report (container 1.4.1). With
    # `--format json` the JSON the readers prefer (status, versions, counts);
    # without it the table, of which the readers look only for the
    # `status running` row.
    if [ "$2" = status ]; then
      if [ "$3" = --format ] && [ "$4" = json ]; then
        printf '{"client":{"appName":"container","build":"release","commit":"unspecified","version":"1.4.1"},"host":{"architecture":"arm64","cpus":8,"operatingSystem":"Version 26.6.2 (Build 25G83)"},"paths":{"appRoot":"/Users/<user>/Library/Application Support/com.apple.container/","installRoot":"/opt/homebrew/Cellar/container/1.4.1/"},"resources":{"containersRunning":0,"containersTotal":1,"images":18},"server":{"appName":"container-apiserver","build":"release","commit":"unspecified","version":"1.4.1"},"status":"running"}\n'
      else
        printf 'FIELD               VALUE\nstatus              running\nclient.version      1.4.1\nserver.version      1.4.1\n'
      fi
    fi
    ;;
  inspect)
    # $INSPECT_ABSENT names containers that do not exist (exit 1, like the real CLI).
    for m in ${INSPECT_ABSENT:-}; do
      [ "$2" = "$m" ] && { echo "Error: container not found: $2" >&2; exit 1; }
    done
    g=$(marker gone "$2")
    if [ -n "$g" ] && [ -e "$g" ]; then echo "Error: container not found: $2" >&2; exit 1; fi
    state=running
    s=$(marker stopped "$2"); if [ -n "$s" ] && [ -e "$s" ]; then state=stopped; fi
    # Mirror the real `container inspect` shape: the interface address lives
    # under status.networks[].ipv4Address, while a published port surfaces a
    # 0.0.0.0 hostAddress that must NOT be mistaken for the container's IP.
    # configuration.networks is here too, holding a different name from the one
    # under status, so that reading the wrong one of the two is a thing this
    # fixture can show rather than a thing it agrees with.
    # The labels are the project label a run of the name gave it, if any. A name
    # never run here, with $INSPECT_PROJECT_FROM_NAME set, carries the project
    # its <service>.<project>.<domain> spelling names; $INSPECT_UNLABELED names
    # containers that carry none.
    proj=
    p=$(marker project "$2")
    if [ -n "$p" ] && [ -e "$p" ]; then
      proj=$(cat "$p")
    elif [ -n "${INSPECT_PROJECT_FROM_NAME:-}" ]; then
      proj=$(printf '%s' "$2" | awk -F. 'NF >= 3 { print $(NF-1) }')
    fi
    for m in ${INSPECT_UNLABELED:-}; do
      [ "$2" = "$m" ] && proj=
    done
    labels=
    if [ -n "$proj" ]; then labels="\"opossum.project\":\"$proj\""; fi
    # The ports this container was run with, if this fixture ran it. A name it
    # never ran keeps the one fixed entry below, which is what every name
    # answered before: tests that only need "a container exists" are unchanged,
    # and a test that runs one and then asks what it publishes now gets its own
    # answer.
    published='{"containerPort":8080,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}'
    # A name this fixture ran answers with what that run published — nothing at
    # all when the run had no `-p`, which is what the real CLI answers. A name it
    # never ran keeps the one fixed entry above.
    pm=$(marker ports "$2")
    if [ -n "$pm" ] && [ -e "$pm" ]; then
      published=$(publishedJSON "$(cat "$pm")")
    fi
    sed -e "s/\"state\":\"STATE\"/\"state\":\"$state\"/" -e "s/\"labels\":{LABELS}/\"labels\":{$labels}/" \
        -e "s|\"publishedPorts\":\[PUBLISHED\]|\"publishedPorts\":[$published]|" <<'JSON'
[{"status":{"state":"STATE","networks":[{"network":"demo-net","ipv4Address":"192.168.64.10/24","ipv6Address":"fdee:0:0:0::10/64","ipv4Gateway":"192.168.64.1"}]},"configuration":{"labels":{LABELS},"networks":[{"network":"demo-net-configured"}],"publishedPorts":[PUBLISHED]}}]
JSON
    ;;
  stats)
    # container 1.3.1: one name that does not exist fails the whole call, and
    # nothing is shown for the ones that do (stats-absent-only.txt). Stopped
    # ones are skipped quietly. Otherwise the table's header, as passthrough.
    shift
    for a in "$@"; do
      for m in ${INSPECT_ABSENT:-}; do
        [ "$a" = "$m" ] && { echo "Error: no such container: $a" >&2; exit 1; }
      done
    done
    echo "Container ID  Cpu %    Memory Usage         Net Rx/Tx            Block I/O            Pids"
    ;;
  *) echo "fake-container: unknown command $1" >&2; exit 0 ;;
esac
