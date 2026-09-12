# Compatibility

Two questions, one page. **How far does a real-world `compose.yaml` get on Apple's
`container`, unmodified?** — answered by measurement, with its method and its
blind spots. And **what exactly is supported?** — the compose fields opossum acts
on, ignores and refuses, and where it differs from `docker compose` on purpose.

An AI agent reading this project should use [`AGENTS.md`](../AGENTS.md) instead:
the same facts, written for a context window rather than a person.

## Figures

<!-- These figures are quoted in README.md and README.ja.md. A test keeps the two
     in step, so change them here and there in the same commit. -->

| Figure | Value |
|---|---|
| Projects measured | 156 |
| Ran completely, as written | 61 (39%) |
| Ran completely with `--from-docker-compose` | 78 (50%) |
| Projects made worse by `--from-docker-compose` | 0 |

"Ran completely" means every service the project declares was running when `up`
returned. It does not mean the application works: a running container can still be
misconfigured, and no HTTP request was made. It is the honest floor — the point at
which the orchestration stopped being the obstacle.

## The corpus

[Haxxnet/Compose-Examples](https://github.com/Haxxnet/Compose-Examples) at commit
`99b28cb` (2026-07-22): 161 example directories, of which 156 contain a compose
file. These are self-hosting stacks people actually run — Bitwarden, Nextcloud,
Gitea, Immich, and so on — not files written to exercise a feature.

All 156 use pre-built images; not one declares `build:`. The figures therefore say
nothing about building images on `container`, which is a separate path with its own
constraints (see [benchmarks](benchmarks.md) and the troubleshooting notes in the
README).

## Method

Both arms ran on **one binary**, and **interleaved per project** — arm A then arm B
on the same project, back to back:

- **A**: `opossum up`
- **B**: `opossum up --from-docker-compose`

B − A is therefore the effect of the automatic fixes alone. Interleaving matters:
if the arms ran as two separate passes, anything that drifted in between — free
host ports, the image cache, the runtime's own health — would show up as an
effect. Between arms every trace was removed (`down -v`, the generated overlay,
the project's volumes), so neither arm inherited the other's state.

Each `up` was bounded at 120 seconds by a real kill, and timeouts were counted
rather than dropped, so the bound is auditable: 2 projects in arm A and 3 in arm B
hit it. A project that needs longer than two minutes to pull and start is recorded
as a timeout, not as a failure of compatibility.

## What moved, and why

17 projects went from not running to running completely; none went the other way.
The fixes opossum wrote into `compose.opossum.yaml` were:

| Fix | Projects |
|---|---|
| A database's data directory was a bind mount the runtime can't take ownership of, so it became a named volume | 31 |
| Postgres's `PGDATA` moved into a subdirectory of its volume, so initialisation succeeds on a non-empty mount | 19 |

(The two overlap: 18 projects needed both.)

## What is still in the way

The remaining failures are not one problem. Arm B, by kind:

| Outcome | Projects | What it is |
|---|---|---|
| Ran completely | 78 | — |
| Started, then every service exited | 40 | The image's own prerequisites are unmet — secrets, config files, a database that was never initialised. Not an orchestration failure |
| A published host port was already taken | 9 | Something else on the Mac holds it |
| Mounts the Docker socket | 8 | nothing here answers on that path about these containers; where the path is a symlink to a socket `opossum` refuses before starting anything |
| Some services ran, some didn't | 7 | Usually one service in the stack hitting one of the rows below |
| A data directory is a bind mount | 4 + 3 | Refused up front, or discovered on the first start |
| A network declared `external: true` doesn't exist | 2 | Create it first, or drop the declaration |
| Timed out at 120s | 3 | See above |
| Other single cases | 2 | A missing bind source; a dependency that exited before becoming healthy |

Every one of these is reported with a diagnostic code and a suggested fix rather
than a raw runtime error — that is the part of compatibility that a table of
supported fields doesn't capture.

## Re-measuring

The figures above are a snapshot, and the honest way to update them is to re-run
the same corpus at the same commit with the same bounds, then change this page and
the README together. A measurement that changes its corpus and its subject at the
same time can't be compared to the one before it.

---

The figures above are the outcome. The rest of this page is the surface behind
them.

## Compose fields

| Field | Supported | Notes |
|-------|-----------|-------|
| `image` | ✅ | |
| `build` | ✅ | string context or `{context, dockerfile, args, target}` (multi-stage `target`; `args` as a mapping or a list of `NAME=value` — or a bare `NAME`, which takes the shell's value and is left out when unset, as docker compose passes it; with `container` 1.3.1 the builder does not read the shell itself — merged by variable across `-f` files like `environment`) |
| `platform` | ✅ | passed to `container run --platform`; `linux/amd64` also enables `--rosetta` so x86-64-only images run on Apple silicon |
| `ports` | ✅ | passed to `container run -p`; both the short form (`"8080:80"`, `"3000"`) and the long mapping form (`{target, published, protocol, host_ip}`) are accepted. A part written as nothing — an empty host address (`::80`, `:8080:80`) or an empty protocol after a `/` (`80/`) — is read as the ports that are there, as docker compose reads it (the runtime refuses those spellings as written), and an IPv6 host address written without brackets (`::1:8080:80`) is passed bracketed, the spelling the runtime takes. Each is checked at load the way docker compose validates it — ports are numbers from 1 to 65535 (the host port may be 0 or left out) written in digits (no sign, padding or hex), ranges are `low-high` and a container range needs a host range of the same length, a host address is an IP, a protocol is tcp, udp or sctp — and refused with the entry number and what to write; in the long form each key is checked by its own name (docker compose lets `target: 0` or `published: a` through there and fails at `up`). A bare container port gets a host port (Apple's runtime requires one): the same number when it's free, otherwise a free one, with a notice. |
| `environment` | ✅ | list or map form; null value passes host value through |
| `env_file` | ✅ | string or list (short, or long `{path, required}`); `KEY=VALUE` files folded in, `environment` overrides them. A missing file — or one that fails to expand — errors unless `required: false`, and only where the environment is needed: `config`, `up` and `run` raise it, while `ps`, `logs` and `config --services` do not. A service that `profiles:` keeps out of this run is not read by any of them |
| `volumes` | ✅ | `external: true` (with optional `name`), or the older map form `external: {name: x}`, which is read the way docker compose reads it — a `name:` that differs from the map's is refused, an unknown key in the map is refused; `external: null` is read as false where docker refuses it. Also: bind mounts (host paths resolved against the compose dir; `~` expanded; a missing source directory is created), named volumes (namespaced `<project>_<volume>`), anonymous volumes (`- /app/node_modules`, named after the service and path), and `type: tmpfs` (mounted via `--tmpfs`); short `src:dst[:ro]` or long form (`{type, source, target, read_only}`, where `type` is required, as docker compose requires it) |
| `tmpfs` | ✅ | service-level tmpfs targets (string or list); folded together with any `type: tmpfs` volume entries |
| `secrets` | ✅ | file-based only; mounted read-only at `/run/secrets/<name>` (the `*_FILE` pattern). `external` secrets are rejected here (docker compose resolves them from a swarm). `uid`/`gid`/`mode` are read and listed as ignored — docker compose ignores them too (it warns `secrets \`uid\`, \`gid\` and \`mode\` are not supported, they will be ignored`, and the file lands as `root root 0644` there as here) |
| `configs` | ✅ | placed in the container as a read-only file, the way docker compose places it: `file:` is the host file; `content:` (interpolated with the rest of the file) and `environment:` (the variable's value as `up` sees it) are written under `$XDG_STATE_HOME/opossum/<project>/configs/` first. A service names the ones it takes, at `/<name>` or at its `target` (counted from `/`). Refused as docker compose refuses them: a reference to an undeclared config, a declaration with none or more than one of `file`/`content`/`environment`, a `configs` that is not a list; `external` configs are rejected here (docker compose refuses them when the container is created); `uid`/`gid`/`mode` are read and listed as ignored (docker compose warns it ignores them too). A `file:` that is not there fails when the container starts, as there. An `environment:` config reads the variable from the project's environment (the shell over `.env`), and one that is not set is refused naming it, as docker compose refuses it. Known differences: an empty `content: ""` is refused as no form set, where docker compose reads it past and places an empty directory at the target; an `environment:` config declared in an included file is read here (from this project's environment), where docker compose refuses the include outright (`file|environment|content attributes are mutually exclusive`, whatever the variable holds) |
| `depends_on` | ✅ | list or long (`condition`) form — orders startup and gates on `service_healthy` / `service_completed_successfully` |
| `healthcheck` | ✅ | `test` (CMD / CMD-SHELL / string), `interval`, `timeout`, `retries`, `start_period`, and either way of switching it off (`disable: true` or `test: ["NONE"]`) |
| `command` | ✅ | list, or a string that is shell-word-split (`sh -c "echo hi"` → `sh`, `-c`, `echo hi`) |
| `entrypoint` | ✅ | overrides the image ENTRYPOINT; string (shell-split) or list, same as `command` |
| `profiles` | ✅ | a gated service starts only when one of its profiles is active (`--profile <name>`, `COMPOSE_PROFILES`, or naming the service); `--profile '*'` or `COMPOSE_PROFILES=*` activates every profile, as docker compose reads it (`*` is special only there — a partial pattern such as `to*` is a plain name, and a service declaring `profiles: ["*"]` stays gated); services with no `profiles` always start. A `profiles` written as one value instead of a list is refused naming the field |
| `mem_limit` / `cpus` | ✅ | passed to `container run` as `-m` / `-c`. Also reads `deploy.resources.limits.{memory,cpus}` (the two forms must agree); memory is rounded up to MiB, CPUs to a whole number (Apple's runtime allocates whole vCPUs) |
| `ssh` | ✅ | `ssh: true` forwards the host's SSH agent into the container (`container run --ssh`), so a service can `git clone`/`push` private repos over SSH using your host keys — without baking keys into the image. Also available per one-off as `opossum run --ssh`. (An opossum extension; docker compose only has build-time `build.ssh`.) |
| `develop.watch` | ✅ | drives `opossum watch`: on host file changes under `path`, `action: sync` copies the changed file to `target` in the running container; `rebuild` rebuilds the image and recreates the container; `sync+restart` copies then restarts the container. Rebuilds/restarts are batched (a burst of edits triggers one). `ignore` takes one glob or a list; ignored subtrees aren't watched. A rule needs a `path` (an empty one is refused rather than watching the whole project); `action` is one of docker compose's five (`restart` and `sync+exec` load but aren't automated yet — `watch` says so at the first change), left out it means `sync`; `sync`, `sync+restart` and `sync+exec` need a `target`. Prefer a **directory** `path` — a single-file `path` can miss an editor's atomic save (rename). |
| `user` / `working_dir` | ✅ | passed to `container run` as `--user` (`name\|uid[:gid]`) and `--workdir` |
| `init` | ✅ | `init: true` → `--init`: run a tini-like init as PID 1 to reap zombies |
| `read_only` | ✅ | `read_only: true` → `--read-only` root filesystem |
| `restart` | ✅ | `always` / `unless-stopped` / `on-failure[:N]` — `up` starts a small per-project supervisor that brings a service back when it exits, and `down` stops it. **`on-failure` can't be told from a clean exit**: Apple `container` doesn't report a container's exit code, so opossum treats any exit as a failure and gives up after a few tries rather than looping. Services another service waits on with `service_completed_successfully` are meant to exit, so they're never watched |
| `cap_add` / `cap_drop` | ✅ | Linux capabilities → `--cap-add` / `--cap-drop` (e.g. `NET_ADMIN`, or `ALL`) |
| `labels` | ✅ | put on the container (`container run -l key=value`), the mapping form and the list form alike (`- key=value`; a bare `key` is the empty value, as docker compose reads it), values interpolated with the rest of the file. opossum's own labels go after them, so a label spelled like one of ours (`opossum.project`) is overridden by ours — as docker compose's own `com.docker.compose.*` win over a clash. A network declaration's `labels` go to `container network create --label` when opossum creates the network. A volume declaration's `labels` are still read past: opossum does not create named volumes itself (the runtime makes them on first mount), and `container volume inspect` shows no labels even when `volume create --label` is given |
| `shm_size` | ✅ | the size of `/dev/shm` (`--shm-size`), written as `64M`, `1gb` or a byte count and carried as the byte count docker compose normalises it to; a value that is not a size is refused at load |
| `ulimits` | ✅ | resource limits (`--ulimit name=soft:hard`): a number sets soft and hard alike, `{soft, hard}` sets them apart; anything but a mapping, and a limit that is not a whole number, is refused at load, as docker compose refuses them; a negative limit is refused here too (docker compose hands it to the runtime, and `container` 1.4.1 takes only a non-negative integer or `unlimited` — its wording for `abc` was `must be a non-negative integer or 'unlimited'`) |
| `mac_address` | ✅ | given to the container's first network (`container run --network <name>,mac=…`, which the runtime takes per network); any usual spelling is taken (`02:42:ac:11:00:77`, dashed, dotted, upper case) and carried in the colon form the runtime reads; a value that is not a 48-bit MAC address, or a service with `network_mode: none`, is refused at load (docker compose refuses a bad address when the container is made). Per-network `mac_address` under `networks:` is still read past and listed |
| `network_mode` | ✅ (`none`) | `network_mode: none` → `--network none`: full network isolation (loopback only, no egress and no name resolution) — the floor for sandboxing an untrusted workload. Other values (e.g. `host`) have no equivalent on Apple `container`, so they're ignored (the service joins the project network) and listed among the ignored fields — the file still loads. |
| `networks` (top-level + per-service) | ✅ | declare networks and place services on them (a service may join several — one `--network` each, in declaration order; after a multi-file merge, in name order). A top-level `internal: true` network is created host-only (`container network create --internal`): no internet egress, though the host stays reachable — see [Constraining egress](agent-sandbox.md). `external: true` (with optional `name`) uses a pre-existing network by its real name (never created or removed). Peers on an internal network can't resolve each other by name (use IPs). Network **aliases** aren't applied. |
| `${VAR}` interpolation | ✅ | `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR:?required}`, `$$` escape; values from a `.env` file next to the compose file (or `--env-file` paths, which replace `.env`; later files win), overridden by the shell |

Other compose fields (e.g. `container_name`, `dns_search`)
are parsed but not acted on — `opossum config` (or `opossum up --verbose`) lists
the ignored fields, so a `docker-compose.yml` runs without surprises.

**Multiple files merge** like docker compose: pass `-f base.yml -f override.yml`
(later files override earlier ones — mappings merge by key, most sequences append,
`command`/`entrypoint` replace, `volumes` merge by mount point, and a service's
`networks` merge by network name across the list and map forms — a file that
lists `[back]` over one that wrote `back: {aliases: [...]}` keeps the map's
entries, and a name both files carry is joined once; `build` merges as one mapping whichever form each file used — a path written over a mapping changes only the context, and a mapping written over a path keeps it). A key a later file writes with nothing after it (`working_dir:`, `ports:`,
a network's `internal:`) is "not given" and leaves the earlier value in place;
the exceptions, as in docker compose, are `command:`/`entrypoint:` (no command)
and a variable inside `environment:` or `build.args` (taken from the shell), and a `compose.override.yaml` (or
`docker-compose.override.yml`) next to a discovered compose file is merged
automatically. `volumes` are keyed by **mount point**: if more than one entry
mounts the same container path, the last one wins — so an override can swap a bind
mount for a named volume, rather than leaving two sources on one path.

**opossum overlay.** A `compose.opossum.yaml` (or `.yml`) next to a discovered
compose file is merged **last, at the highest precedence** — after the base file
and any `compose.override.yaml`. docker compose doesn't read this name, so the same
directory works with both tools and your original files stay untouched: put the
tweaks that make a project run on Apple `container` here and keep them out of the
shared compose file. When one is merged, opossum prints a one-line notice naming it
(delete the file to opt out).

`opossum up --from-docker-compose` **writes that overlay for you**. Two things
about a `docker-compose.yml` can stop it starting here for reasons that are
properties of the runtime rather than mistakes in your file:

| What | Why it fails on Apple `container` | What the overlay does |
|---|---|---|
| A named volume mounted at Postgres's data directory | Historically the volume arrived holding `lost+found`, so the directory wasn't empty and `initdb` refused it (`OPSM-101`). opossum now clears that from volumes it creates, so this only bites on a volume made elsewhere | Points `PGDATA` at a subdirectory — the data stays in the same volume |
| A database's data directory on a bind mount | Bind mounts are host-owned and can't be chowned from inside the container, which the official Postgres, MySQL/MariaDB, ClickHouse and MongoDB images do at startup (`OPSM-105`) | Mounts a named volume there instead — **this changes where the data lives**; the host directory is left untouched, not copied. The Redis family is not on that list — its images disagree with each other, so the name says nothing. Those mounts are left exactly as written; if one is seen failing and opossum can tell which mount died, what follows is put in front of you to decide on — in the overlay when there isn't one yet, on screen when there is — not a change already made. Where it cannot tell, it says so and leaves the change to you |

The file is the whole compatibility picture for the project, not just the fixes,
so what opossum *couldn't* fix is in the same place. Entries come in three kinds,
each marked:

- **applied** — changed, and in effect. This is what made the project run.
- **suggestion — NOT APPLIED** — a concrete change written out but commented,
  because it alters what the project means (where data lives, how services share
  it). Uncomment the block to apply it; it's self-contained, including any
  `volumes:` declaration it needs. A suggestion is only written for something
  opossum watched happen — a container that died taking ownership of a bind mount,
  a named volume two running services both need — never because a directory looked
  like it might one day hold data.
- **note** — something opossum writes no YAML for (`OPSM-204` a Docker socket
  mount, `OPSM-106` a host device or session socket, `OPSM-409`
  `restart: on-failure`, `OPSM-111` a Postgres data dir left as a bind mount).
  Recorded so the failure isn't a mystery. Notes carry no YAML, so there's
  nothing to uncomment.

Each entry says what it's about and why (with the diagnostic code); applied entries
add how to check it and how to undo it, suggestions add how to apply or ignore
them, and notes add what to expect instead. opossum **never overwrites an
existing `compose.opossum.yaml`** and never modifies your own compose file.

## Health-gated startup

`depends_on: {<svc>: {condition: service_healthy}}` makes opossum wait until the
dependency is healthy before starting the dependent. Apple's `container` runtime
has no native healthcheck, so opossum runs the dependency's `healthcheck.test`
via `container exec` and polls it (`retries` attempts, `interval` apart, after an
initial `start_period`) until it passes. The dependency must define a
`healthcheck` that is switched on, or the file is rejected — a healthcheck turned
off with `disable: true` or `test: ["NONE"]` counts as none at
all. The default condition (`service_started`)
still just orders startup.

`depends_on: {<svc>: {condition: service_completed_successfully}}` treats the
dependency as a one-shot (e.g. a migration/init step): opossum runs it in the
**foreground** and only starts the dependent if it exits 0. The runtime exposes
an exit code only from a foreground `run` — `container inspect` reports a bare
`stopped` with no code — so a run-to-completion service can't also be required
`service_healthy` (it stops when it finishes); that combination is rejected.

## Variable interpolation

References in the compose file are expanded before parsing. Values come from a
`.env` file sitting next to the compose file (`KEY=value` lines, `#` comments — a
` #` after an unquoted value starts one, as docker compose reads it, and a quoted
value ends at its closing quote, with `\"`, `\\` and `\n` inside double quotes read as
docker compose reads them), and the process environment overrides them — so
`FOO=bar opossum up` wins over `FOO` in `.env`. Supported forms: `$VAR`,
`${VAR}`, `${VAR:-default}` (default when unset **or empty**), `${VAR-default}`
(default only when unset), `${VAR:?message}` (fail if unset or
empty) / `${VAR?message}` (fail only if unset), and `$$` for a literal `$`. An undefined variable with no default
expands to an empty string. A reference may span lines via a YAML double-quoted
`\`-continuation, and a reference nested in another's default (`${A:-${B:-x}}`) is
resolved too.

Because expansion runs on the raw file **before** YAML parsing — which is what lets
it reach every field uniformly, including `x-` extensions and block scalars — a
`${…}` written inside a **comment** is expanded as well, unlike docker compose
(which interpolates after parsing and so ignores comments). For a `${VAR}` this is
harmless (the comment is dropped anyway), but a `${VAR:?required}` in a comment will
**fail the load**. Keep interpolation syntax out of comments, or write the `$` as
`$$` to keep it literal.

What a reference expands to is text, as it is for docker compose: `TAG=42` under
`image: alpine:${TAG}` is the tag `42`, `V=[1]` under `environment:` is the value
`[1]`, `F=1.50` stays `1.50`, and a boolean field (`read_only: ${RO}`) takes the
word. A value carrying line breaks — a multi-line PEM key in `.env`, say — is carried
through parsing whole: referencing it from the compose file yields the full
multi-line string, as docker compose does (measured on v5.4.0). Expanding raw
text can't write such a value in place, so opossum holds it aside behind a
one-line private-use marker (U+E001) and restores it after the parse — which is
why a compose file or variable that already contains U+E001 is refused.

For the same reason, a reference in a mapping **key** (`${NAME}: value`) is
expanded too: raw text has no notion of key versus value. docker compose leaves
keys alone, so a file that relies on a literal `${…}` key reads differently here.

The values in an env file are themselves expanded, as docker compose does. This
was measured against Compose v5.3.1 case by case, because two of the rules are not
the ones a reader would guess:

- **Only keys defined above the line are in scope.** `B=${A}/b` after `A=/a` gives
  `/a/b`, but a reference to a key defined further down the file expands to empty.
- **A single-quoted value is left alone**, the way a shell treats single quotes.
  Double-quoted and unquoted values are expanded.

When the same key is defined twice, which one wins depends on whether the other
definition is at the same level, and the two answers are opposite:

- **A strictly outer level always wins.** The levels, outermost first, are: the
  shell, then the project's `.env` (or `--env-file`), then a service's own
  `environment:` block, then that service's `env_file:` files. So a value in an
  `env_file:` can reference a key that only `environment:` defines, and where
  both define one, `environment:` is what that value sees.
- **Within one level the files are a single map filled top to bottom**, so the
  last assignment wins and a file's own line beats a file read before it. Given
  `one.env` with `A=first` and `two.env` with `A=second` then `B=${A}`, `B` is
  `second`. A value read *before* the override still holds the old one.

Expansion is a single pass: what one value expands to is not expanded again when
another value references it.

A value that is nothing but a reference, and whose variable is unset, becomes an
explicit empty string. Expansion runs before the parser, so such a value would
otherwise arrive as a bare `key:` and read as YAML null — and under
`environment:` null is not "empty", it is "inherit this one from the host". A
`KEY:` you write yourself still means that: the repair is applied to the parsed
document, to a null the expansion left behind and not to one you typed. It
therefore follows the parser wherever the parser goes: mapping values in block or
flow style, whether written beside the key or under it; items of a sequence in
either style; values carrying an anchor. It leaves alone anything the parser reads
as text: a reference inside a `|` block, or inside a quoted string spanning
several lines, empties in place and the text around it is kept as written — which
in a folded string means the space in front of the reference stays, as Compose
keeps it. A reference in a comment does not make the value beside it empty:
`KEY:  # ${VAR}` still inherits `KEY` from the host. The values of `.env` and
`env_file:` entries are not YAML at all, so a `PATH`-style value ending in a colon
is untouched.

A compose file may not contain U+E000, a private-use character opossum writes
where a reference expanded to nothing so that the parser can be asked afterwards
where the value went. A file holding one is refused rather than read.

opossum also provides one built-in: **`${OPOSSUM_HOST_GATEWAY}`** — the address a
container can use to reach a service running on the host (see below). It ranks
below every level above, so a same-named entry in the shell, in `.env`, in a
service's `environment:`, or in its `env_file:` overrides it — including for
values derived from it in the same file.

## Commands

opossum mirrors the common `docker compose` subcommands, delegating each to the
`container` CLI.

| Command | Supported | Notes |
|---------|-----------|-------|
| `up [service…]` | ✅ | build + start the project, or named services plus their deps. Leaves a running service untouched when its config is unchanged (build images only if missing), and flags orphan containers from removed services; `--force-recreate`, `--build`, `--no-build`, `--from-docker-compose` (import build images from Docker instead of building; formerly `--from-docker`, which still works and warns), `--remove-orphans`, `--foreground`, `--profile` |
| `down [-v] [--rmi local\|all]` | ✅ | stop, remove, and delete the project network; `-v` also removes named volumes; `--rmi local` removes opossum-built images (`all` also removes pulled ones); `--remove-orphans` also removes containers for services no longer in the compose |
| `destroy` | ✅ (extra) | remove everything opossum created for the project in one step — containers, the project network, named volumes, images, the restart supervisor, `.opossum/` and the generated `compose.opossum.yaml`. Your compose file, `.env` and sources are never touched, and neither is anything shared (`external: true` volumes, other projects, the DNS domain, the build cache). Lists what it will remove and asks; `--force` skips the question, `--dry-run` lists and stops, `--keep-overlay` and `--keep-images` narrow it. `--keep-local` leaves this directory's generated files alone and removes only the runtime objects; `-p <other>` with `--force` is refused when it would take this directory's files with it. Volumes named for the project that no service claims are listed, not removed — a name alone can't distinguish a leftover from an `external: true` volume or from another project's |
| `ps` | ✅ | service / container / image / IP / ports / status |
| `port <service> <container-port>` | ✅ | print the host side of a published port on one line, `0.0.0.0:65345`, as docker compose does — the way to read the host port opossum settled on when `ports: - "3000"` was moved off a busy 3000 (see "Published ports" above); `--protocol tcp\|udp` (tcp by default). A port that is not published is refused with the ones that are (`no port 90/tcp for container web.demo.opossum: 80/tcp, 90/udp`), or with `(none published)` when it publishes nothing; a service whose container is absent or stopped with `service "web" is not running`, as docker compose words it. No `--index`: a service is one container here |
| `ls [-a] [-q] [--format json]` | ✅ | the opossum projects on this machine — every project that has containers, found by the label opossum puts on them — as a NAME / STATUS table, with a count of containers by state (`running(2)`, or `running(1), stopped(1)`); needs no compose file. As under docker compose, a project with no running container appears only with `--all`, `-q` prints names only and `--format json` an array of `{Name, Status}`. There is no CONFIG FILES column: opossum does not record which compose file made a project. The state words are the runtime's (`stopped` where docker compose says `exited`) |
| `volumes [service…] [-q] [--format json]` | ✅ | the volumes the file's services mount that exist on the runtime, under the names the runtime knows them by (`<project>_<volume>`), as a DRIVER / VOLUME NAME table; named services narrow it to what they mount. As under docker compose, a volume appears once a service mounting it has started (a declared but unmounted volume is never created), and external volumes are not listed. A volume the file no longer mounts is not listed either — docker compose finds a project's volumes by label, opossum by what the file mounts, the same rule `down -v` removes by; `-q` prints names only and `--format json` an array of `{Name, Driver}` (docker compose prints one object per line there) |
| `images` | ✅ | each service's image, whether opossum builds it, and whether it's present locally |
| `logs [service…]` | ✅ | `--follow` (several services multiplexed, each line prefixed with its name), `-n/--tail` |
| `stats [service…]` | ✅ | live CPU / memory / net / block I/O / pids (streams; `--no-stream` for a snapshot). `--host` shows each service's **host** memory footprint — the resident size of its VM on your Mac — which a shared-VM tool can't report per service (see below) |
| `exec [-it] <service> <cmd…>` | ✅ | run a command in a running service |
| `build [service…]` | ✅ | build images for services with `build:` |
| `pull [service…]` | ✅ | pull images for services with `image:` |
| `import [service…]` | ✅ (extra) | copy a service's Docker-built image into `container`'s store, so `up` skips the rebuild |
| `doctor` | ✅ (extra) | diagnose the environment (runtime — read from `system status --format json` on `container` 1.4.1, with the server version, the client/server version match and the container and image counts; DNS domain, outbound network, build VM memory, reclaimable storage, networks nothing is running on, stack memory estimate); prints ✅/⚠️/❌ + a one-line fix each. `--format json` emits machine-readable `{healthy, checks[]}` for scripts/agents; a failed check exits non-zero in either format |
| `cp <src> <dst>` | ✅ | copy files between a service's container and the host (each path is a host path or `service:path`), like `docker compose cp` |
| `watch` | ✅ | watch each service's `develop.watch` paths and act on changes (like `docker compose watch`): `sync` copies files in, `rebuild` rebuilds + recreates, `sync+restart` copies + restarts; runs until Ctrl-C. Start the stack with `up` first |
| `start [service…]` | ✅ | start existing (stopped) containers |
| `stop [service…]` | ✅ | stop without removing |
| `restart [service…]` | ✅ | stop then start in place |
| `kill [service…]` | ✅ | send a signal (default KILL); `-s/--signal` |
| `run [--rm] [--no-deps] [-T] <service> [cmd]` | ✅ | one-off foreground container; starts deps unless `--no-deps`; `-T`/`--no-tty` disables the pseudo-terminal; progress goes to stderr so the one-off's stdout stays clean (usable as an MCP stdio bridge); no published ports. `--audit` reports afterwards what the run did (workspace file diff, egress destinations when routed through a proxy, exit code), as text or `--audit-format json` |
| `ws snapshot\|ls\|rollback\|rm\|prune` | ✅ (extra) | snapshot and roll back a workspace directory (`--path`, default `./work`) using APFS copy-on-write clones — near-instant, almost no extra disk until they diverge; on a non-APFS filesystem it falls back to a full copy and says so. Snapshots live in `.opossum-snapshots/` beside the workspace |
| `config [--services]` | ✅ | validate and print the resolved config (interpolation + env_file applied, a bare variable name shown with the shell's value the way docker compose shows it — left unset, it stays bare under `environment` and is left out of `build.args`, which is what `up` passes; the value is the shell's, where docker compose also reads `.env` for it), noting ignored fields; mirrors what `up` starts, so `profiles:`-gated services appear only with `--profile`. `--services` prints the names in startup order without resolving `env_file`, so it answers for a project whose env files do not — compose-file `${VAR}` interpolation still runs, and still fails the command when a required variable has no value |

Add `--verbose` to any command to print each underlying `container` invocation
(as `+ container …`) to stderr — handy when filing a bug report, so you can see
exactly what opossum ran.

## Where it differs from docker compose

opossum aims to run a familiar `compose.yaml`, but it delegates to Apple's
`container` (not the Docker engine), so some behaviors differ and some compose
features aren't supported. The detailed rationale for each is in
[Known limitations](troubleshooting.md#known-limitations); this is the scannable overview.

**Behaves differently** (same field, different mechanics):

| Area | docker compose | opossum (on Apple `container`) |
|------|----------------|--------------------------------|
| Setup | none | one-time `sudo container system dns create opossum` for name resolution |
| Container names | `<project>-<service>-N` | `<service>.<project>.<domain>` (DNS-registered for bare-name discovery) |
| Named volumes | `<project>_<volume>` (a bare name is namespaced by project; only an `external:` or `name:` volume keeps its own), one volume mounted by several services at once; `down -v` removes the project's and leaves external ones | the same names and the same `down -v` scope — but a named volume is attached to **one running container at a time**: Apple `container` attaches it as an exclusive block device, so two services that mount the same one cannot run together (the second fails to start and `up` says so; measured on `container` 1.4.1, and on docker compose v5.5.0 for the docker side). Share data through a bind mount instead — see [Known limitations](troubleshooting.md#known-limitations) |
| Volume seeding | a fresh named/anonymous volume is pre-filled from the image's contents at that path | **emulated by opossum** — Apple `container` mounts a fresh volume empty, so opossum fills one it creates from the image at that path (`cp -a`) before the service starts. See the limits below |
| Networks | user-defined networks + aliases | `networks:` **is** supported — a per-project default network (`<project>-net`), plus top-level `internal:`/`external:` and multiple networks per service; a top-level network's `ipam.config` subnet is given to `container network create --subnet` (an IPv6 one to `--subnet-v6`; it is carried, and shown by `config`, as the network it names — `10.7.0.1/24` is `10.7.0.0/24` — where docker compose keeps the spelling; one of each at most, as docker's bridge driver takes, and a subnet must be in CIDR form — both refused at load where docker compose refuses them when the network is made); two projects declaring the same subnet are refused by the runtime as by docker (`overlaps an existing network`); a network that already exists with another subnet is kept and `up` says so (`OPSM-207`: `down`, then `up`, recreates it — docker compose recreates it in place); a later `-f` file's `ipam.config` replaces the earlier file's, as under docker compose; on an `external: true` network `ipam` is not applied (the network is the user's) and not listed either, as docker compose reads it. Per-network **aliases** and static IPs (`ipv4_address`) aren't applied: `container run` has no flag for either (see [Networking model](networking.md)) |
| Published ports | a bare `ports: - "3000"` picks a random host port | mirrors it to `3000:3000` when that port is free, else falls back to a free port and says so (`opossum ps` shows the real one; two services that both leave the host port open get different ones). Apple `container` requires a host port and has no random option, so the mirror is a predictable default rather than a random one |
| Healthcheck | engine-native | no native support — opossum runs `healthcheck.test` via `container exec` and polls |
| `service_completed_successfully` | engine tracks exit | opossum runs the one-shot in the **foreground** (an exit code is only observable there) |
| Two `up`s at once | the second reports the services already running (the engine owns the containers, so both `up`s see one state) | the second is refused at once (`OPSM-208`, naming the other's pid) until the first finishes — here each `up` owns what it started and rolls that back on failure, so two at once could remove a service the other had just reported (measured on `container` 1.4.1), and refusing is the way to keep one owner; a `down` or `destroy` under an `up` is refused the same way, and a one-off `run` takes the same lock while it starts the service's dependencies. A foreground `up` holds it until the service exits or Ctrl-C, so a `down` from another terminal is refused until then |

**Volume seeding.** Docker copies an image's directory contents into a *fresh* named or anonymous volume the first time it's used; Apple `container` mounts it **empty**. opossum does the copy itself — before the service starts, a throwaway container copies the image's contents at that path into the volume with `cp -a`, run as root so that the image's ownership survives the copy the way it does on Docker, where the engine does it — so the common dev pattern of a bind-mounted source plus a `- /app/node_modules` volume works, for named and anonymous volumes alike. What it does not cover: only a volume **opossum creates** is filled, so an existing one is never touched and your data is safe; the copy needs `sh` inside the image, so a distroless/scratch image cannot be copied from and the volume mounts empty — opossum says so (`OPSM-108`) rather than leave you to find it, and the same warning covers any other reason the copy could not run; and `external: true` volumes are never seeded. `volume: {nocopy: true}` — and its short spelling `src:target:nocopy` — turns the copy off, as on Docker; `opossum config` prints it back so the output stays runnable — except on an anonymous volume, which has no source for the short spelling to attach to and so loses the option when the printed config is fed back in.

**Not supported / hard constraints:**

- **Platform**: macOS 26+ on Apple silicon, single host only (no Swarm/remote). Relies on `container`'s macOS-26 networking + DNS.
- **Ignored fields** (parsed and listed by `opossum config` / `--verbose`, not acted on): `container_name`, `dns`/`dns_search` (`container run --dns` replaces the runtime's resolver — the one that answers service names — rather than adding to it, so a service given its own `dns:` could no longer reach its siblings by name; measured on `container` 1.4.1: with `--dns 1.1.1.1`, even listed alongside the runtime's resolver, a sibling's name is NXDOMAIN. docker compose keeps both because its embedded DNS answers names and forwards the rest to the `dns:` servers — see [Networking model](networking.md)), `network_mode` other than `none`, per-network **aliases** and static IPs (`ipv4_address`; a top-level network's `ipam.config` subnet *is* applied), `deploy` (except `resources.limits`), `sysctls`, `devices`, `privileged` (`container run` has no flag for any of the three — 51 flags on 1.4.1, none for sysctls, devices or privileged), and the keys of a top-level network, volume or secret declaration opossum does not read (`driver`, the keys of `ipam` other than a `config` entry's `subnet`, a volume's or secret's `labels`, a secret's `name` — which docker compose does not use for a file secret either: the file is mounted under the secret's key, measured on v5.5.0), listed as `networks.back.ipam.driver`. A key opossum does not read inside a block it does act on, or in a list's long-form item, is listed by its full name (`deploy.replicas`, `build.labels`, `ports entry 1.mode`, `depends_on.db.restart`). (`networks`, `cap_add`/`cap_drop` *are* acted on.)
- **`secrets`**: file-based only; `external` secrets are rejected (`uid`/`gid`/`mode` are ignored, as docker compose ignores them).
- **DB data dirs**: a volume is its own ext4 filesystem here, so it arrives holding `lost+found` where Docker's arrives empty — and Postgres's `initdb` refuses a data directory that isn't empty. opossum **removes `lost+found` from the volumes it creates**, so a plain `pgdata:/var/lib/postgresql/data` works as it does on Docker. A volume opossum did not create (made by an older opossum, by `container volume create`, or by another project) still has it: `up` then reports Postgres's own refusal with `OPSM-101` and what to do about it. (MySQL/MariaDB tolerate the mount point either way.)
- **DB data dirs can't be bind-mounted** (use a **named volume**): Apple `container`'s bind mounts are host-owned (virtiofs) and can't be `chown`ed from inside the container, so a DB image (MySQL/Postgres/…) that chowns its data directory fails to start with `chown: … Operation not permitted`. A named volume *is* chownable, so mount the data directory from one. Self-host composes that put data under a bind-mounted `/mnt/docker-volumes/<svc>/…` (a Linux-host convention) hit this on macOS — when a DB crashes this way, `up` points at the fix.
- **Won't run at all**: composes that need Linux-host kernel access (WireGuard's `NET_ADMIN` + `/lib/modules`) — Apple `container` doesn't provide it (also true of Docker Desktop for the host-path cases).
- **Won't manage *these* containers**: tools that drive Docker through `/var/run/docker.sock` (e.g. Portainer). Apple `container` exposes no Docker-compatible daemon socket of its own — it talks to the host over XPC — so nothing here answers on that path about the containers it runs. The mount itself is not the obstacle: bind-mounting a host Unix socket into a container *does* work (since `container` 1.1.0), and a Docker daemon was reached that way from inside a container on 2026-08-28. That is the one socket that has been measured here; what a session socket mount (X11, PulseAudio) reaches is a separate question, and has not been measured — see **OPSM-106**. What puts a symlink at that name varies by which Docker distribution you have, and one merely installed answers nothing — so what you get, if anything answers, is that daemon's containers, not opossum's.
- **JVM images with an old bundled JDK (e.g. early Elasticsearch 7.x)**: the VM mounts cgroup v2 with no controllers, and JDK 17.0.1 crashes reading it — `CgroupInfo.getMountPoint() … null` at launch, before any config applies (`ES_JAVA_OPTS`/`JAVA_TOOL_OPTIONS` don't help). Measured on `container` 1.3.1: Elasticsearch 7.16.3 and 7.17.0 (both bundle JDK 17.0.1) crash this way; 7.17.28 (JDK 22) starts and serves on port 9200. So it is the image's JDK, not the Elasticsearch major: pin a patch release with a recent bundled JDK. `opossum ps` shows a crashed service as `stopped`; check `opossum logs <svc>`. This is a JDK–VM incompatibility, not an opossum limitation.
- **Shape checks at load**: a file is checked the way docker compose validates one before anything runs, and refused with the field and what to write instead — a key docker compose does not take is refused naming it, as docker compose refuses it (`additional properties 'foo' not allowed`), at the places opossum reads into: a service (`enviroment:`), `build`, `healthcheck`, `deploy`, `deploy.resources` and its `limits`, `develop` and a watch rule, a long-form port, mount, secret, config or env file item and a mount's `volume:` options, a dependency's mapping, a service's per-network entry, the top level (`servcies:`) and a volume, network, secret or config declaration; a key docker compose takes that opossum does not act on is listed among the ignored fields; an `x-` key is taken anywhere here (docker compose refuses one inside an env file item, an include entry and `extends`). Under a block opossum lists whole — a mount's `bind` and `tmpfs` options, `deploy.resources.reservations`, a network declaration's `ipam`, a service's `logging` — and inside a declaration reached through `<<:`, a key is listed, not refused, where docker compose refuses it; the keys docker compose takes come from the compose specification's schema (the copy is named in the source) — (and the line, where the mistake is a key written with nothing after it, a value that is not a boolean where one belongs, or a top-level key of the wrong shape) — a field written with nothing after it (`volumes:` alone; `command:` and `entrypoint:` alone still mean "no command"), a number or a boolean where a string belongs (`image: 42`, `volumes: [42]`, `profiles: [42]`), a bare name where only a list is taken (`cap_add: NET_ADMIN`), an empty item (`- ` alone), a resource limit written as a list, a mapping or a blank (`cpus: [1]`, `mem_limit: ""`, and the same under `deploy.resources`), a variable whose value is a list or a mapping (`environment: {A: [1]}`, and the same under `labels`), a variable whose name is not a string (`environment: {1: a}`, `{~: a}`), an `env_file` that is the empty string, `labels` written as one value (`labels: x`), a key with nothing after it where the value is what opossum acts on — a mount's `type:`, `source:` or `target:`, a port's `target:` or `published:`, a secret's `source:`, an env file's `path:`, a dependency's `condition:`, a network's `internal:`, `name:` or `driver:`, a volume's `name:` or `driver:`, a secret declaration's `file:` or `name:`, the `name:` inside `external: {name: …}` — and any of those declaration keys written as a number, a boolean, a list or a mapping (`name: 42`, `driver: [a]`) — a mount written in the long form without its `type:`, a bind mount without its `source:`, a network listed twice, a service key with nothing under it, a top-level `name:` or `version:` that is not a string, a top-level `networks:`, `volumes:`, `secrets:` or `configs:` with nothing under it or written as a list or a single value, the same shapes one level down (`build.context`, `deploy.resources` limits and reservations, `healthcheck`, `develop.watch` rules), a watch rule's `action` that is not one docker compose takes or that copies files without a `target`, and a blank `healthcheck` duration (`interval: ""`). A boolean written as the quoted word (`read_only: "true"`, `internal: "True"`, the YAML 1.1 `"yes"`/`"no"`/`"on"`/`"off"`) is read as the boolean, as docker compose reads it, when it is written in place rather than through an alias; `"1"`, `"t"` or `""` is refused, as there. YAML aliases (`*name`) are read wherever a value or a list item can stand. With several `-f` files each file is checked on its own before the merge, as docker compose checks them, and a mistake is refused naming its file; a declaration's `external: {name: x}` is read as `external: true` with `name: x` before the merge, as docker compose reads it, so a later file's `external: true` (or `false`, or a bare `name:`) keeps the name, a later `name:` wins, and a later map with another name is refused as a conflict, as there; in a later file a key written with nothing after it is not a mistake but "not given", and keeps the earlier file's value (a whole service included; a bare key a later file writes that no earlier file gave a value to — a new network's `internal:`, a `build.context:` no base has — is refused naming that file). Known differences from docker compose: `external: {}` (the map form with no name) is an external resource with no name here, where docker compose uses the declaration's key as the name; in a later file, `external: {name: }` with nothing after `name:` keeps the earlier file's name here ("not given", as for any bare key) where docker compose refuses it; a mount of `type: npipe`, `cluster` or `image` is refused here (`bind`, `volume` and `tmpfs` are the mounts opossum passes to `container run`) where docker compose reads it; a bind mount whose `source` is the empty string (a `${VAR}` that expands to nothing included) is an anonymous volume here, in the short form and the long form alike, where docker compose binds the project directory for the long form (`source: ""`) and refuses the short form (`:/x` is `empty section between colons`; measured on v5.5.0); a key with nothing after it that opossum does not act on (a port's `host_ip:`, a mount's `read_only:`) is read past here where docker compose refuses it; a network listed twice in a later file is refused here (docker compose reads that list as a map before checking and lets it through when an earlier file lists the network too); `healthcheck.retries: 0` means the default rather than zero; a watch rule's `action` may be left out; and a key docker compose takes that opossum does not read under `build`, `healthcheck`, `deploy`, `develop` or a watch rule is listed among the ignored fields by its full name (`deploy.replicas`, `build.labels`) rather than acted on, and so is such a key in a list's long-form item (`ports entry 1.mode`, `volumes entry 2.bind`) or in a dependency's mapping (`depends_on.db.restart`). Differences the other way: `ssh: true` on a service is opossum's own key (it forwards the host's SSH agent), which docker compose would refuse; and a file named by `extends: {file: …}` is checked whole here, so a typo in one of its services this file does not extend is refused, where docker compose reads only the extended service.
- **Not parsed**: none of the keys this table names is left unread (`configs` was the last); a top-level key outside it (`models`, for one) is read past and listed by `config`. A file's `include:` is read as docker compose reads it: each entry names a file (or, in the long form, one or several under `path`, with `project_directory` and `env_file`), read as a project of its own — its relative paths (`build`, a bind mount's source, `env_file`, a secret's `file`) count from its project directory (the first path's directory when not given; a nested `include` counts from it too, not from the file that names it), its own `include` and `extends` are resolved, its bare keys are refused as in any first file, and its variables come from that directory's `.env` (or the entry's `env_file`) under the including project's shell and `.env`; the included files are merged in order under the including file, whose settings win where both define a service, and their `volumes`/`networks`/`secrets`/`configs` declarations come along — not their `name`, `version` or extension fields. A service of the including file may `extends:` an included one. A file that is not there, a chain of includes that comes back to a file, and an `include:` that is not a list are refused naming the file (an empty or bare `include:` names nothing and is read past). A service key the including file writes with nothing under it is the included service where one defines it, as in a later `-f` file; what is still nothing after the merge is refused. A service with `extends:` naming a service of the same file is read as docker compose reads it (the named service's settings first, its own over them, merged as a later `-f` file merges; with several `-f` files, each file's `extends` is resolved against the services that file defines, before the files are merged); `extends: {file: …}` reads the named file the way docker compose does: the path is taken from the project directory (the first `-f` file's, or the include entry's), the named service's own `extends` is resolved first (a chain may run into a third file, found from the named file's own directory; a cycle through files is refused), and the paths that service wrote relative to its file (`build`, a bind mount's source, `env_file`, `develop.watch` paths) resolve against that file's directory — a short-form source counts as a path when written as `.`, `..`, `./…` or `../…`, the forms opossum mounts as a host path, so `.hidden:/x` stays a named volume here where docker compose reads any source beginning with `.` as a path; only the service comes over — the named file's top-level `volumes`/`networks`/`secrets` declarations do not, and a `depends_on` it names must be a service of this project. A file or service that is not there is refused naming the file.

Everything else in the [Compose support](#compose-fields) and
[Command support](#commands) tables works as in docker compose.

## Reuse images you already built with Docker

Images are OCI-standard, so a Docker-built image runs on Apple `container` — the
two just keep separate stores. If you're coming from `docker compose`, you almost
certainly already have your services built; `opossum import` copies them over so
the first `up` starts everything **without rebuilding** in Apple's builder:

```sh
docker compose build          # (or you already have the images)
opossum import                # docker save → container image load, per build service
opossum up                    # starts immediately; no rebuild

# …or in one step — import each build service instead of building it, then start:
opossum up --from-docker-compose
```

`docker compose` and opossum name a built image the same way
(`<project>-<service>:latest`), so the import lands under the tag `up` looks for.
This is also the escape hatch when Apple's builder can't handle a Dockerfile
(BuildKit-specific features): build it with Docker and import it. `docker` is only
invoked by `import` — the normal path never shells out to it. Alternatively, push
the image to a registry and let `opossum pull` fetch it.

## Safe to try alongside Docker

opossum drives Apple's `container` runtime, which is **entirely separate from
Docker** — separate images, containers, and volumes, in their own storage. So you
can run `opossum up` in a project you already use with `docker compose` without
disturbing it:

- **Your Docker containers and named volumes are not touched.** opossum only ever
  invokes the `container` CLI, never `docker`. It creates its *own* named volumes
  in the `container` runtime, and even `opossum down -v` removes only those — a
  Docker volume of the same name (and its data) is left intact.
- **Bind mounts are the one shared surface.** A `./path:/…` bind mount points both
  engines at the same host directory, so don't run opossum and Docker against the
  same bind-mounted data (e.g. a database dir) *at the same time* — that's the
  usual "two engines, one data directory" hazard, not something opossum does to
  you.
- **Ports and data.** If your Docker stack is already up on the same host ports,
  opossum's `up` refuses before starting anything and names the port (`OPSM-201`;
  nothing is harmed). And because named-volume data isn't shared between the two
  runtimes, opossum starts such a service from a fresh volume — seeded from the
  image, not from your Docker data.

In short: **point opossum at your existing `docker-compose.yml` and try `opossum
up`** — the worst case is a port clash or an unsupported field it simply skips
(run `opossum config`, or `--verbose`, to see which), not lost data.

[← back to the README](../README.md)
