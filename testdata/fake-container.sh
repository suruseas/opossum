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

# publish_side_refused answers one side of a `-p` (which: host or container): it prints the runtime's words and returns 1 when the side is not a port or a range of
# ports — a word, a space in it, `1e3`, `0x10`, more than one `-`, a number above 65535, nothing at all in the container side — and returns 0 otherwise
# (container 1.5.0, measured: `Error: invalid publish host port: <side>`, `Error: invalid publish container port: <side>`). Spellings the runtime reads in its own
# way (`5-`, `-5`) are not asked about.
publish_side_refused() {
  _which=$1 _side=$2
  if [ -z "$_side" ]; then
    [ "$_which" = container ] || return 0
    printf 'Error: invalid publish container port: \n' >&2; return 1
  fi
  _bad=0
  case "$_side" in *[!0-9+-]*) _bad=1 ;; esac
  # More than one `-` is no range (`5-6-7`), but `5--6` is read by the runtime in its own way (counts that differ), which is not asked about here.
  case "$_side" in *-*) case "${_side#*-}" in *-*) case "$_side" in *--*) ;; *) _bad=1 ;; esac ;; esac ;; esac
  if [ "$_bad" = 0 ]; then
    for _part in "${_side%%-*}" "${_side#*-}"; do
      # A `+` goes in front of an end and nowhere else (`+5`, not `5+`, `++5` or a `+` alone), and a number is 65535 at most, however many digits it is written with.
      [ "$_part" = + ] && _bad=1
      _digits=${_part#+}
      case "$_digits" in *+*) _bad=1 ;; esac
      case "$_digits" in ''|*[!0-9]*) continue ;; esac
      _digits=$(printf '%s' "$_digits" | sed 's/^0*//')
      if [ "${#_digits}" -gt 5 ] || { [ -n "$_digits" ] && [ "$_digits" -gt 65535 ]; }; then _bad=1; fi
    done
  fi
  [ "$_bad" = 0 ] && return 0
  printf 'Error: invalid publish %s port: %s\n' "$_which" "$_side" >&2
  return 1
}

# publish_range_reversed says that a side of a `-p` is a range written high-low (`5-1`), which the runtime refuses as the range it is.
publish_range_reversed() {
  case "$1" in
    *-*) _lo=${1%%-*} _hi=${1#*-} ;;
    *) return 1 ;;
  esac
  _lo=${_lo#+} _hi=${_hi#+}
  case "$_lo$_hi" in ''|*[!0-9]*) return 1 ;; esac
  [ -n "$_lo" ] && [ -n "$_hi" ] || return 1
  [ "$_lo" -gt "$_hi" ]
}

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
# case marked gone is no longer there (and with $INSPECT_STRICT, one nothing ran is not). Both answers have to reach every
# command, not only `inspect`: a fake that says "no such container" to one
# question and hands logs to the next lets a command that should have passed
# the service by look as though it worked (#1096).
is_there() {
  for m in ${INSPECT_ABSENT:-}; do
    [ "$1" = "$m" ] && return 1
  done
  g=$(marker gone "$1")
  if [ -n "$g" ] && [ -e "$g" ]; then return 1; fi
  # $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, for start, logs,
  # exec and kill as for inspect (#1551).
  if [ -n "${INSPECT_STRICT:-}" ]; then
    c=$(marker created "$1")
    if [ -n "$c" ] && [ ! -e "$c" ]; then return 1; fi
  fi
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
        # A label the runtime refuses (1.5.0, rc 1; testdata/real-cli-output.md, #1817). A label with no `=` is a key alone, and is taken. Otherwise the key is what
        # is before the first `=` — except where it is empty or the value is, where the whole label is named as the key (`=1`, `k=`). The checks are made in this
        # order and the first that fails is the one said: the key's length (128), the key's content (words of lower-case letters, digits and hyphens that start
        # and end with a letter or a digit, joined by a single `.` or `/`), and the whole label's length (4096). The lengths are counted by the shell, in
        # characters where its locale counts them so and in bytes where it does not (dash), which differs from the runtime for a multibyte label alone.
        # A value that is its own argument and starts with `-` is read as another flag, so the option has no value (rc 64, measured on 1.5.0, #1730):
        # opossum passes `--label=<k=v>` joined (#1423) and never reaches this.
        prev=""
        for a in "$@"; do
          case "$prev" in
            --label) case "$a" in -?*) printf "Error: Missing value for '--label <label>'\n" >&2; exit 64 ;; esac ;;
          esac
          prev=$a
        done
        for a in "$@"; do
          case "$a" in
            --label=*)
              l=${a#--label=}
              case "$l" in
                *=*) k=${l%%=*}; v=${l#*=}
                     { [ -z "$k" ] || [ -z "$v" ]; } && k=$l
                     code=""
                     if [ "${#k}" -gt 128 ]; then code=length
                     else
                       case "$k" in
                         *[[:space:][:cntrl:]]*) code=content ;;
                         *) printf '%s\n' "$k" | grep -Eq '^[a-z0-9]([a-z0-9-]*[a-z0-9])?([./][a-z0-9]([a-z0-9-]*[a-z0-9])?)*$' || code=content ;;
                       esac
                       [ -z "$code" ] && [ "${#l}" -gt 4096 ] && code=total
                     fi
                     if [ -n "$code" ]; then
                       # the runtime writes a tab as `\t` and another control character as `\u{01}`
                       shown() { printf '%s' "$1" | sed -e "s/$(printf '\t')/\\\\t/g" -e "s/$(printf '\001')/\\\\u{01}/g"; }
                       case "$code" in
                         length) printf 'Error: LabelError(code: ContainerResource.AppErrorCode(rawValue: "invalid_label_key_length"), metadata: ["key": "%s", "maxLength": "128"])\n' "$(shown "$k")" >&2 ;;
                         content) printf 'Error: LabelError(code: ContainerResource.AppErrorCode(rawValue: "invalid_label_key_content"), metadata: ["key": "%s"])\n' "$(shown "$k")" >&2 ;;
                         total) printf 'Error: LabelError(code: ContainerResource.AppErrorCode(rawValue: "invalid_label_length"), metadata: ["label": "%s", "maxLength": "4096"])\n' "$(shown "$l")" >&2 ;;
                       esac
                       exit 1
                     fi ;;
              esac ;;
          esac
        done
        # The labels it was made with are what a later `inspect` answers with
        # (one `key=value` per line), where something is being remembered.
        m=$(marker netlabels "$name")
        if [ -n "$m" ]; then
          : > "$m"
          want=""
          for a in "$@"; do
            [ "$want" = 1 ] && printf '%s\n' "$a" >> "$m"
            want=""
            [ "$a" = "--label" ] && want=1
            # opossum passes `--label=<k=v>` as one argument (#1423); the real CLI reads both.
            case "$a" in --label=*) printf '%s\n' "${a#--label=}" >> "$m" ;; esac
          done
        fi
        gm=$(marker netgone "$name")
        [ -n "$gm" ] && rm -f "$gm"
        printf '%s\n' "$name" ;;
      # A network already deleted is not there to delete a second time: the
      # real CLI fails (1.4.1: `Error: failed to delete one or more networks:
      # ["<name>"]`, a different shape from `network inspect`'s `network not
      # found: <name>` — DeleteNetwork's networkAlreadyGone reads both).
      delete)
        gm=$(marker netgone "$3")
        if [ -n "$gm" ] && [ -e "$gm" ]; then
          printf 'Error: failed to delete one or more networks: ["%s"]\n' "$3" >&2; exit 1
        fi
        [ -n "$gm" ] && : > "$gm"
        m=$(marker netlabels "$3")
        [ -n "$m" ] && rm -f "$m"
        echo "$3" ;;
      # `inspect` of a network this fake saw made answers the labels it was
      # made with, in the shape the real CLI prints (an empty `labels` for one
      # made without any); of any other, nothing, as it always did.
      inspect)
        gm=$(marker netgone "$3")
        if [ -n "$gm" ] && [ -e "$gm" ]; then
          printf 'Error: network not found: %s\n' "$3" >&2; exit 1
        fi
        m=$(marker netlabels "$3")
        if [ -n "$m" ] && [ -e "$m" ]; then
          printf '[\n  {\n    "configuration" : {\n      "labels" : {\n'
          awk -F= 'NF { k = $1; v = substr($0, length(k) + 2); gsub(/\\/, "\\\\", k); gsub(/"/, "\\\"", k); gsub(/\\/, "\\\\", v); gsub(/"/, "\\\"", v); if (n++) printf ",\n"; printf "        \"%s\" : \"%s\"", k, v } END { if (n) printf "\n" }' "$m"
          printf '      },\n      "name" : "%s"\n    }\n  }\n]\n' "$3"
        fi ;;
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
  build)
    # An option that takes a value, given one that starts with `-` as its own argument, has none (rc 64, measured on 1.5.0, #1730): opossum passes
    # these joined (`--build-arg=<k=v>`, #1423) and never reaches this. The metavars are the CLI's own.
    prev=""
    for a in "$@"; do
      case "$prev" in
        --target)    meta="--target <stage>" ;;
        --build-arg) meta="--build-arg <key=val>" ;;
        --label)     meta="--label <key=val>" ;;
        --tag)       meta="--tag <name>" ;;
        *)           meta="" ;;
      esac
      if [ -n "$meta" ]; then
        case "$a" in -?*) printf "Error: Missing value for '%s'\n" "$meta" >&2; exit 64 ;; esac
      fi
      prev=$a
    done
    echo "built image" ;;
  image)
    # What the real CLI refuses before it does anything (container 1.5.0, testdata/real-cli-output.md):
    # a subcommand it does not have is rc 64, and so is a subcommand that needs an image or a reference
    # and is given none; `delete` with none is rc 1. `ls`, `prune` and `load` need no argument.
    case "$2" in
      ''|inspect|tag|save|pull|push|delete|rm|list|ls|load|prune) ;;
      *) printf "Error: Unexpected argument '%s'\nUsage: container image [--debug] <subcommand>\n" "$2" >&2; exit 64 ;;
    esac
    if [ -n "$2" ] && [ -z "$3" ]; then
      case "$2" in
        inspect) printf "Error: Missing expected argument '<images> ...'\n" >&2; exit 64 ;;
        tag)     printf "Error: Missing expected argument '<source>'\n" >&2; exit 64 ;;
        save)    printf "Error: Missing expected argument '<references> ...'\n" >&2; exit 64 ;;
        pull)    printf "Error: Missing expected argument '<reference>'\n" >&2; exit 64 ;;
        delete|rm) printf 'Error: no images specified and --all not supplied\n' >&2; exit 1 ;;
      esac
    fi
    # `load` reads an archive from its standard input (opossum's `docker image save | container image
    # load`), and the real CLI refuses one with nothing in it (container 1.5.0): rc 1, nothing loaded.
    # With `-i`/`--input` (`--input=<path>` too) it reads a file, which this does not look at. Two
    # simplifications, neither in internal/shimcontract because the real CLI answers otherwise: any
    # non-empty input is taken (the real CLI refuses what is no tar: rc 1, `unable to open the
    # archive, code -30`), and a file named by `-i` is taken whether or not it is there (the real CLI
    # says `file does not exist`, rc 1).
    if [ "$2" = load ]; then
      case " $* " in
        *" -i "*|*" --input "*|*" --input="*) ;;
        *) if [ "$(cat | wc -c | tr -d ' ')" = 0 ]; then
             printf 'Error: failed to extract archive: no entries found in archive\n' >&2; exit 1
           fi ;;
      esac
    fi
    # An image named in $IMAGE_ABSENT is not there, and the real CLI refuses what is asked of it
    # (container 1.5.0, testdata/real-cli-output.md): `inspect`, `tag` (its source), `save` and `push`
    # (the last argument) and a `delete` without `--force` are rc 1; `delete --force` of it is rc 0.
    absent_image() { for m in ${IMAGE_ABSENT:-}; do [ "$1" = "$m" ] && return 0; done; return 1; }
    ref=$(last "$@")
    case "$2" in
      inspect) if absent_image "$3"; then printf 'Error: image not found: %s\n' "$3" >&2; exit 1; fi ;;
      tag) if absent_image "$3"; then printf 'Error: image with reference %s\n' "$3" >&2; exit 1; fi ;;
      push) if absent_image "$ref"; then printf 'Error: image with reference %s\n' "$ref" >&2; exit 1; fi ;;
      save) if absent_image "$ref"; then
          printf 'failed to get image for reference %s: notFound: "image with reference %s"\nError: failed to save image(s)\n' "$ref" "$ref" >&2; exit 1
        fi ;;
      delete|rm)
        case " $* " in
          *" --force "*) ;;
          *) if absent_image "$ref"; then printf 'Error: failed to delete one or more images: ["%s"]\n' "$ref" >&2; exit 1; fi ;;
        esac ;;
    esac
    case "$2" in
      inspect)
        # $IMAGE_CMD gives an image its CMD (#1635), as `ref=word,word` entries separated by
        # spaces (no space or quote in a word), in the real shape: one variant without a CMD (an
        # attestation, what a multi-platform image answers) and the variant that has it
        # (`variants[].config.config.Cmd`, the form testdata/image-inspect/ records). An image
        # not named there answers nothing, as before.
        for e in ${IMAGE_CMD:-}; do
          [ "${e%%=*}" = "$3" ] || continue
          words=$(printf '%s' "${e#*=}" | awk -F, '{ for (i = 1; i <= NF; i++) printf "%s\"%s\"", (i > 1 ? "," : ""), $i }')
          printf '[{"variants":[{"config":{"config":{}}},{"config":{"config":{"Cmd":[%s]}}}]}]\n' "$words"
          exit 0
        done ;;
      # The rest of what the real CLI has answers nothing here, as it did before: it is not
      # "an unknown command", which is what this used to print for them all.
      *) ;;
    esac ;;
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
    # starting with `-` given to another flag as a separate argument (the real
    # CLI calls it missing, exit 64 — measured on `--user -1`; opossum itself
    # now passes such values combined, `--user=-1`, #996), and a flag with no
    # value at the end. The letters are spelled out: a range can take other
    # letters in some locales.
    name= given= want= n=0
    for a in "$@"; do
      n=$((n+1)); [ "$n" -eq 1 ] && continue  # "run"
      if [ -n "$want" ]; then
        if [ "$want" = name ]; then
          case "$a" in -?*) printf "Error: Missing value for '--name <name>'\n" >&2; exit 64 ;; esac
          name=$a; given=1
        fi
        if [ "$want" = label ]; then
          case "$a" in -?*) printf "Error: Missing value for '--label <label>'\n" >&2; exit 64 ;; esac
        fi
        want=; continue
      fi
      case "$a" in
        -d|--detach|-i|--interactive|-t|--tty|--init|--no-dns|--read-only|--rm|--remove|--rosetta|--ssh|--virtualization) : ;;
        --name) want=name ;;
        --label) want=label ;;
        # A long flag opossum passes as one `--flag=value` argument (#996) is
        # already complete — unlike a short flag or one given a separate value,
        # it does not consume the next word too, so a `--name` after it is not
        # its value.
        --*=*) : ;;
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
      # A name this fake itself has already run and not since deleted is taken:
      # 1.4.1 refuses `run` of an existing name the same way whether it is
      # running or stopped (measured, testdata/real-cli-output.md), before the
      # volume check below — and until this a second run of one name silently
      # replaced the first, a call opossum never makes deliberately (a genuine
      # replace always deletes first), so nothing exercised this question
      # until #962's contract row asked it.
      m=$(marker created "$name")
      if [ -n "$m" ] && [ -e "$m" ]; then
        printf 'Error: container with id %s already exists\n' "$name" >&2; exit 1
      fi
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
        # A long flag opossum passes as one `--flag=value` argument (#996) is
        # already complete — unlike a short flag or one given a separate value,
        # it does not consume the next word too.
        --*=*) : ;;
        -*) want=value ;;
        *) break ;;
      esac
    done
    # A `-p` whose two sides name different numbers of ports is refused next,
    # before anything is recorded (container 1.4.1, testdata/real-cli-output.md:
    # `Error: publish host and container port counts are not equal: <host>:<container>`
    # (the two sides as written, without the address or the protocol),
    # exit 1, for a host range with one container port, the other way round, and
    # two ranges of different lengths). The sides are the last two `:` fields once
    # the protocol and an IPv6 address are taken off.
    prev=
    for a in "$@"; do
      if [ "$prev" = -p ] || [ "$prev" = --publish ]; then
        s=${a%%/*}
        # A `-p` that names no host port at all is refused in the runtime's own words for each spelling (container 1.5.0): one that starts with `:`
        # is no publish value, an IPv4 address with nothing after it a missing host port, an IPv6 one an IPv4 address that is none. opossum sends none.
        case "$a" in
          :*) printf 'Error: invalid publish value: %s\n' "$a" >&2; exit 1 ;;
          \[*\]::*) printf 'Error: invalid publish IPv4 address: %s\n' "$a" >&2; exit 1 ;;
          *[!0-9.:]*::*|'') ;;
          [0-9.]*::*) case "${s%%::*}" in ''|*[!0-9.]*) ;; *) case "${s#*::}" in *:*) ;; *) printf 'Error: invalid publish host port: %s:\n' "${s%%::*}" >&2; exit 1 ;; esac ;; esac ;;
        esac
        # No `:` at all is no publish value (a bare container port included), and the protocol after the first `/` is tcp or udp in either case, asked before the
        # ports are (container 1.5.0, measured: `invalid publish value: <as written>`, `invalid publish protocol: <lower case>`).
        case "$s" in *:*) ;; *) printf 'Error: invalid publish value: %s\n' "$a" >&2; exit 1 ;; esac
        case "$a" in
          */*) proto=$(printf '%s' "${a#*/}" | tr 'A-Z' 'a-z')
            case "$proto" in tcp|udp) ;; *) printf 'Error: invalid publish protocol: %s\n' "$proto" >&2; exit 1 ;; esac ;;
        esac
        case "$s" in \[*) s=${s#*]}; s=${s#:} ;; esac
        case "$s" in
          *:*)
            ctr=${s##*:}; rest=${s%:*}; host=${rest##*:}
            # A side that is not a port or a range is refused as such, the host side first, before the range questions below.
            publish_side_refused host "$host" || exit 1
            publish_side_refused container "$ctr" || exit 1
            # A range written high-low (`5-1`) and one that starts at 0 or at 1 (`0:80`, `0-1:80`, `1:80`, `01:80`, `127.0.0.1:0:80`; `80:1`, `80-81:1-2`) is refused before the counts are asked,
            # the host side first and then the container side (container 1.5.0, rc 1: `Error: invalid publish host port range: <host>`,
            # `Error: invalid publish container port range: <container>`; 2 and above are taken when the range is the right way round and the sides are ports.
            # An empty host is not a 0: the forms with no host port at all were refused above, in the runtime's other words.
            for side in host ctr; do
              case "$side" in host) v=$host ;; *) v=$ctr ;; esac
              if publish_range_reversed "$v"; then
                printf 'Error: invalid publish %s port range: %s\n' "$([ "$side" = host ] && echo host || echo container)" "$v" >&2; exit 1
              fi
              lo=${v%%-*}; lo=${lo#+}
              case "$lo" in ''|*[!0-9]*) ;; *)
                lz=${lo#"${lo%%[!0]*}"}
                case "$lz" in ''|1) printf 'Error: invalid publish %s port range: %s\n' "$([ "$side" = host ] && echo host || echo container)" "$v" >&2; exit 1 ;; esac ;;
              esac
            done
            hc=1 cc=1
            case "$host" in *-*) hc=$(( ${host#*-} - ${host%%-*} + 1 )) ;; esac
            case "$ctr" in *-*) cc=$(( ${ctr#*-} - ${ctr%%-*} + 1 )) ;; esac
            if [ "$hc" -ne "$cc" ]; then
              printf 'Error: publish host and container port counts are not equal: %s:%s\n' "$host" "$ctr" >&2; exit 1
            fi ;;
        esac
      fi
      prev=$a
    done
    # Running a name makes it there and running again (see stop/delete), with
    # the project label this run gave it (none for a run without one), given
    # as `-l` (the short spelling) or `--label`; opossum itself passes
    # `--label=<v>` as one argument (#996), so that combined form is read too.
    proj= pub= prev=
    for a in "$@"; do
      case "$a" in
        --label=opossum.project=*) proj=${a#--label=opossum.project=} ;;
      esac
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
      # A named volume `-v NAME:/target` is made by the run, as the real runtime makes one; a path
      # (a bind) is not. $INSPECT_STRICT asks `volume delete` of a name nothing made to be refused.
      if [ "$prev" = -v ]; then
        case "$a" in
          *:*)
            vsrc=${a%%:*}
            case "$vsrc" in
              ''|/*|.*|'~'*) ;;
              *) m=$(marker volseen "$vsrc"); if [ -n "$m" ]; then : > "$m"; fi
                 m=$(marker volgone "$vsrc"); if [ -n "$m" ]; then rm -f "$m"; fi ;;
            esac ;;
        esac
      fi
      if [ "$prev" = --name ]; then
        m=$(marker gone "$a"); if [ -n "$m" ]; then rm -f "$m"; fi
        m=$(marker stopped "$a"); if [ -n "$m" ]; then rm -f "$m"; fi
        m=$(marker project "$a"); if [ -n "$m" ]; then printf '%s' "$proj" > "$m"; fi
        m=$(marker ports "$a"); if [ -n "$m" ]; then printf '%s' "$pub" > "$m"; fi
        m=$(marker created "$a"); if [ -n "$m" ]; then : > "$m"; fi
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
    # $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, for stop as for
    # inspect (#1551).
    if [ -n "${INSPECT_STRICT:-}" ]; then
      c=$(marker created "$n")
      if [ -n "$c" ] && [ ! -e "$c" ]; then
        echo "Error: internalError: \"failed to stop container\" (cause: \"notFound: \"container with ID $n not found\"\")" >&2; exit 1
      fi
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
  kill)
    # The real CLI only truly kills a running container: one already stopped
    # refuses with invalidState, and one not there refuses with notFound —
    # both measured on 1.4.1, same pair as the other two fakes. Orchestrator.Kill
    # re-inspects after either failure and only reports it when the container
    # is still running or unreadable, so a container not running discards
    # both — answering them correctly here changes no caller's behaviour.
    n=$(last "$@")
    if ! is_there "$n"; then
      echo "Error: internalError: \"failed to kill container\" (cause: \"notFound: \"container with ID $n not found\"\")" >&2; exit 1
    fi
    s=$(marker stopped "$n")
    if [ -n "$s" ] && [ -e "$s" ]; then
      echo 'Error: internalError: "failed to kill container" (cause: "invalidState: "no runtime client exists: container is stopped"")' >&2; exit 1
    fi
    if [ -n "$s" ]; then : > "$s"; fi
    ;;
  delete|rm)
    n=$(last "$@"); g=$(marker gone "$n")
    if [ -n "$g" ] && [ -e "$g" ]; then
      echo "Error: internalError: \"failed to delete container\" (cause: \"notFound: \"container with ID $n not found\"\")" >&2; exit 1
    fi
    # $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, for delete as for
    # inspect (#1551).
    c=$(marker created "$n")
    if [ -n "${INSPECT_STRICT:-}" ] && [ -n "$c" ] && [ ! -e "$c" ]; then
      echo "Error: internalError: \"failed to delete container\" (cause: \"notFound: \"container with ID $n not found\"\")" >&2; exit 1
    fi
    if [ -n "$g" ]; then : > "$g"; fi
    s=$(marker stopped "$n"); if [ -n "$s" ]; then rm -f "$s"; fi
    if [ -n "$c" ]; then rm -f "$c"; fi
    ;;
  volume)
    # A `--label` given a value of its own that starts with `-` has none (rc 64, measured on 1.5.0, #1932).
    if [ "$2" = create ]; then
      prev=""
      for a in "$@"; do
        if [ "$prev" = --label ]; then
          case "$a" in -?*) printf "Error: Missing value for '--label <label>'\n" >&2; exit 64 ;; esac
        fi
        prev=$a
      done
    fi
    if [ "$2" = delete ] || [ "$2" = rm ]; then
      # $INSPECT_STRICT: a volume nothing here made is not one the runtime has, and the real CLI
      # refuses to delete it (container 1.4.1, testdata/real-cli-output.md); without it every name
      # is taken to be there (#1551).
      if [ -n "${INSPECT_STRICT:-}" ]; then
        s=$(marker volseen "$3")
        if [ -n "$s" ] && [ ! -e "$s" ]; then
          echo "Error: failed to delete one or more volumes: [\"$3\"]" >&2; exit 1
        fi
      fi
      g=$(marker volgone "$3")
      if [ -n "$g" ] && [ -e "$g" ]; then
        echo "Error: failed to delete one or more volumes: [\"$3\"]" >&2; exit 1
      fi
      if [ -n "$g" ]; then : > "$g"; fi
      echo "$3"
    fi
    ;;
  # A container that is not there cannot be exec'd into: the real CLI exits 1
  # (1.4.1: `Error: get failed: container <name> not found`) — a healthcheck
  # probe against a container that vanished mid-probe must see a failure.
  exec)
    # The container is the first argument that is not a flag (`exec -t NAME ...`), and a flag that takes its
    # value apart (`-e A=1`, `--user u`; container 1.5.0, testdata/real-cli-output.md) is read with the value,
    # which is not the name. A value joined to the flag (`--env=A=1`) is one argument.
    n=; skip=1; val=
    for a in "$@"; do
      if [ "$skip" = 1 ]; then skip=0; continue; fi
      if [ -n "$val" ]; then val=; continue; fi
      case "$a" in
        -e|--env|--env-file|--gid|--uid|-u|--user|-w|--workdir|--cwd|--ulimit) val=1; continue ;;
        -*) continue ;;
      esac
      n=$a; break
    done
    if [ -n "$n" ] && ! is_there "$n"; then
      printf 'Error: get failed: container %s not found\n' "$n" >&2; exit 1
    fi ;;   # otherwise: succeed (exit 0 = healthy)
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
    # $INSPECT_STRICT: a name nothing here ran is not a container the runtime has, as the real CLI
    # answers for one it has never seen (container 1.4.1, testdata/real-cli-output.md); without it
    # every name is taken to be there (#1551).
    if [ -n "${INSPECT_STRICT:-}" ]; then
      c=$(marker created "$2")
      if [ -n "$c" ] && [ ! -e "$c" ]; then echo "Error: container not found: $2" >&2; exit 1; fi
    fi
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
    # The names are the arguments that are not a flag (--no-stream, --format json): with
    # $INSPECT_STRICT a flag read as a name is one nothing ran (#1551).
    prev=
    for a in "$@"; do
      case "$a" in -*) prev=$a; continue ;; esac
      [ "$a" = json ] && [ "$prev" = --format ] && { prev=$a; continue; }
      prev=$a
      is_there "$a" || { echo "Error: no such container: $a" >&2; exit 1; }
    done
    echo "Container ID  Cpu %    Memory Usage         Net Rx/Tx            Block I/O            Pids"
    ;;
  *) echo "fake-container: unknown command $1" >&2; exit 0 ;;
esac
