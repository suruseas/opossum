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
| Mounts the Docker socket | 8 | Apple `container` has no equivalent; opossum refuses before starting anything |
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
| `build` | ✅ | string context or `{context, dockerfile, args, target}` (multi-stage `target`) |
| `platform` | ✅ | passed to `container run --platform`; `linux/amd64` also enables `--rosetta` so x86-64-only images run on Apple silicon |
| `ports` | ✅ | passed to `container run -p`; both the short form (`"8080:80"`, `"3000"`) and the long mapping form (`{target, published, protocol, host_ip}`) are accepted. A bare container port gets a host port (Apple's runtime requires one): the same number when it's free, otherwise a free one, with a notice. |
| `environment` | ✅ | list or map form; null value passes host value through |
| `env_file` | ✅ | string or list (short, or long `{path, required}`); `KEY=VALUE` files folded in, `environment` overrides them. Missing file errors unless `required: false` |
| `volumes` | ✅ | bind mounts (host paths resolved against the compose dir; `~` expanded; a missing source directory is created), named volumes (namespaced `<project>_<volume>`), anonymous volumes (`- /app/node_modules`, named after the service and path), and `type: tmpfs` (mounted via `--tmpfs`); short `src:dst[:ro]` or long form (`{type, source, target, read_only}`) |
| `tmpfs` | ✅ | service-level tmpfs targets (string or list); folded together with any `type: tmpfs` volume entries |
| `secrets` | ✅ | file-based only; mounted read-only at `/run/secrets/<name>` (the `*_FILE` pattern). `external` secrets are rejected; `uid`/`gid`/`mode` are not applied |
| `depends_on` | ✅ | list or long (`condition`) form — orders startup and gates on `service_healthy` / `service_completed_successfully` |
| `healthcheck` | ✅ | `test` (CMD / CMD-SHELL / string), `interval`, `timeout`, `retries`, `start_period`, and either way of switching it off (`disable: true` or `test: ["NONE"]`) |
| `command` | ✅ | list, or a string that is shell-word-split (`sh -c "echo hi"` → `sh`, `-c`, `echo hi`) |
| `entrypoint` | ✅ | overrides the image ENTRYPOINT; string (shell-split) or list, same as `command` |
| `profiles` | ✅ | a gated service starts only when one of its profiles is active (`--profile <name>`, `COMPOSE_PROFILES`, or naming the service); services with no `profiles` always start |
| `mem_limit` / `cpus` | ✅ | passed to `container run` as `-m` / `-c`. Also reads `deploy.resources.limits.{memory,cpus}` (the two forms must agree); memory is rounded up to MiB, CPUs to a whole number (Apple's runtime allocates whole vCPUs) |
| `ssh` | ✅ | `ssh: true` forwards the host's SSH agent into the container (`container run --ssh`), so a service can `git clone`/`push` private repos over SSH using your host keys — without baking keys into the image. Also available per one-off as `opossum run --ssh`. (An opossum extension; docker compose only has build-time `build.ssh`.) |
| `develop.watch` | ✅ | drives `opossum watch`: on host file changes under `path`, `action: sync` copies the changed file to `target` in the running container; `rebuild` rebuilds the image and recreates the container; `sync+restart` copies then restarts the container. Rebuilds/restarts are batched (a burst of edits triggers one). `ignore` globs are honored and ignored subtrees aren't watched. Prefer a **directory** `path` — a single-file `path` can miss an editor's atomic save (rename). |
| `user` / `working_dir` | ✅ | passed to `container run` as `--user` (`name\|uid[:gid]`) and `--workdir` |
| `init` | ✅ | `init: true` → `--init`: run a tini-like init as PID 1 to reap zombies |
| `read_only` | ✅ | `read_only: true` → `--read-only` root filesystem |
| `restart` | ✅ | `always` / `unless-stopped` / `on-failure[:N]` — `up` starts a small per-project supervisor that brings a service back when it exits, and `down` stops it. **`on-failure` can't be told from a clean exit**: Apple `container` doesn't report a container's exit code, so opossum treats any exit as a failure and gives up after a few tries rather than looping. Services another service waits on with `service_completed_successfully` are meant to exit, so they're never watched |
| `cap_add` / `cap_drop` | ✅ | Linux capabilities → `--cap-add` / `--cap-drop` (e.g. `NET_ADMIN`, or `ALL`) |
| `network_mode` | ✅ (`none`) | `network_mode: none` → `--network none`: full network isolation (loopback only, no egress and no name resolution) — the floor for sandboxing an untrusted workload. Other values (e.g. `host`) have no equivalent on Apple `container`, so they're ignored (the service joins the project network) and listed among the ignored fields — the file still loads. |
| `networks` (top-level + per-service) | ✅ | declare networks and place services on them (a service may join several — one `--network` each, in declaration order). A top-level `internal: true` network is created host-only (`container network create --internal`): no internet egress, though the host stays reachable — see [Constraining egress](agent-sandbox.md). `external: true` (with optional `name`) uses a pre-existing network by its real name (never created or removed). Peers on an internal network can't resolve each other by name (use IPs). Network **aliases** aren't applied. |
| `${VAR}` interpolation | ✅ | `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR:?required}`, `$$` escape; values from a `.env` file next to the compose file (or `--env-file` paths, which replace `.env`; later files win), overridden by the shell |

Other compose fields (e.g. `container_name`, `dns_search`)
are parsed but not acted on — `opossum config` (or `opossum up --verbose`) lists
the ignored fields, so a `docker-compose.yml` runs without surprises.

**Multiple files merge** like docker compose: pass `-f base.yml -f override.yml`
(later files override earlier ones — mappings merge by key, most sequences append,
`command`/`entrypoint` replace), and a `compose.override.yaml` (or
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
| A database's data directory on a bind mount | Bind mounts are host-owned and can't be chowned from inside the container, which every official DB image does at startup (`OPSM-105`) | Mounts a named volume there instead — **this changes where the data lives**; the host directory is left untouched, not copied |

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
- **note** — nothing to change: the compose file can't express a fix (a Docker
  socket mount, a host device). Recorded so the failure isn't a mystery. Notes carry no YAML, so there's nothing to uncomment.

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
`.env` file sitting next to the compose file (`KEY=value` lines, `#` comments,
optional surrounding quotes), and the process environment overrides them — so
`FOO=bar opossum up` wins over `FOO` in `.env`. Supported forms: `$VAR`,
`${VAR}`, `${VAR:-default}` (default when unset **or empty**), `${VAR-default}`
(default only when unset), `${VAR:?message}` / `${VAR?message}` (fail if
unset/empty), and `$$` for a literal `$`. An undefined variable with no default
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
| `ps` | ✅ | service / container / IP / ports / status |
| `images` | ✅ | each service's image, whether opossum builds it, and whether it's present locally |
| `logs [service…]` | ✅ | `--follow` (several services multiplexed, each line prefixed with its name), `-n/--tail` |
| `stats [service…]` | ✅ | live CPU / memory / net / block I/O / pids (streams; `--no-stream` for a snapshot). `--host` shows each service's **host** memory footprint — the resident size of its VM on your Mac — which a shared-VM tool can't report per service (see below) |
| `exec [-it] <service> <cmd…>` | ✅ | run a command in a running service |
| `build [service…]` | ✅ | build images for services with `build:` |
| `pull [service…]` | ✅ | pull images for services with `image:` |
| `import [service…]` | ✅ (extra) | copy a service's Docker-built image into `container`'s store, so `up` skips the rebuild |
| `doctor` | ✅ (extra) | diagnose the environment (runtime, DNS domain, outbound network, build VM memory, reclaimable storage, stack memory estimate); prints ✅/⚠️/❌ + a one-line fix each. `--format json` emits machine-readable `{healthy, checks[]}` for scripts/agents; a failed check exits non-zero in either format |
| `cp <src> <dst>` | ✅ | copy files between a service's container and the host (each path is a host path or `service:path`), like `docker compose cp` |
| `watch` | ✅ | watch each service's `develop.watch` paths and act on changes (like `docker compose watch`): `sync` copies files in, `rebuild` rebuilds + recreates, `sync+restart` copies + restarts; runs until Ctrl-C. Start the stack with `up` first |
| `start [service…]` | ✅ | start existing (stopped) containers |
| `stop [service…]` | ✅ | stop without removing |
| `restart [service…]` | ✅ | stop then start in place |
| `kill [service…]` | ✅ | send a signal (default KILL); `-s/--signal` |
| `run [--rm] [--no-deps] [-T] <service> [cmd]` | ✅ | one-off foreground container; starts deps unless `--no-deps`; `-T`/`--no-tty` disables the pseudo-terminal; progress goes to stderr so the one-off's stdout stays clean (usable as an MCP stdio bridge); no published ports |
| `config [--services]` | ✅ | validate and print the resolved config (interpolation + env_file applied), noting ignored fields; mirrors what `up` starts, so `profiles:`-gated services appear only with `--profile` |

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
| Named volumes | shared globally by name | namespaced `<project>_<volume>`; `down -v` only removes this project's |
| Volume seeding | a fresh named/anonymous volume is pre-filled from the image's contents at that path | **emulated by opossum** — Apple `container` mounts a fresh volume empty, so opossum fills one it creates from the image at that path (`cp -a`) before the service starts. See the limits below |
| Networks | user-defined networks + aliases | `networks:` **is** supported — a per-project default network (`<project>-net`), plus top-level `internal:`/`external:` and multiple networks per service; per-network **aliases** and static IPs aren't applied (see [Networking model](networking.md)) |
| Published ports | a bare `ports: - "3000"` picks a random host port | mirrors it to `3000:3000` when that port is free, else falls back to a free port and says so (`opossum ps` shows the real one; two services that both leave the host port open get different ones). Apple `container` requires a host port and has no random option, so the mirror is a predictable default rather than a random one |
| Healthcheck | engine-native | no native support — opossum runs `healthcheck.test` via `container exec` and polls |
| `service_completed_successfully` | engine tracks exit | opossum runs the one-shot in the **foreground** (an exit code is only observable there) |

**Volume seeding.** Docker copies an image's directory contents into a *fresh* named or anonymous volume the first time it's used; Apple `container` mounts it **empty**. opossum does the copy itself — before the service starts, a throwaway container copies the image's contents at that path into the volume with `cp -a`, run as root so that the image's ownership survives the copy the way it does on Docker, where the engine does it — so the common dev pattern of a bind-mounted source plus a `- /app/node_modules` volume works, for named and anonymous volumes alike. What it does not cover: only a volume **opossum creates** is filled, so an existing one is never touched and your data is safe; the copy needs `sh` inside the image, so a distroless/scratch image cannot be copied from and the volume mounts empty — opossum says so (`OPSM-108`) rather than leave you to find it, and the same warning covers any other reason the copy could not run; and `external: true` volumes are never seeded. `volume: {nocopy: true}` — and its short spelling `src:target:nocopy` — turns the copy off, as on Docker; `opossum config` prints it back so the output stays runnable — except on an anonymous volume, which has no source for the short spelling to attach to and so loses the option when the printed config is fed back in.

**Not supported / hard constraints:**

- **Platform**: macOS 26+ on Apple silicon, single host only (no Swarm/remote). Relies on `container`'s macOS-26 networking + DNS.
- **Ignored fields** (parsed and listed by `opossum config` / `--verbose`, not acted on): `container_name`, `dns`/`dns_search` (service discovery is automatic — see [Networking model](networking.md)), `network_mode` other than `none`, per-network **aliases** and static IPs (`ipam`), `deploy` (except `resources.limits`), `sysctls`, `devices`, `privileged`, and top-level volume `driver`/`labels`. (`networks`, `cap_add`/`cap_drop` *are* acted on.)
- **`secrets`**: file-based only; `external` secrets and `uid`/`gid`/`mode` are not applied.
- **DB data dirs**: a volume is its own ext4 filesystem here, so it arrives holding `lost+found` where Docker's arrives empty — and Postgres's `initdb` refuses a data directory that isn't empty. opossum **removes `lost+found` from the volumes it creates**, so a plain `pgdata:/var/lib/postgresql/data` works as it does on Docker. A volume opossum did not create (made by an older opossum, by `container volume create`, or by another project) still has it: `up` then reports Postgres's own refusal with `OPSM-101` and what to do about it. (MySQL/MariaDB tolerate the mount point either way.)
- **DB data dirs can't be bind-mounted** (use a **named volume**): Apple `container`'s bind mounts are host-owned (virtiofs) and can't be `chown`ed from inside the container, so a DB image (MySQL/Postgres/…) that chowns its data directory fails to start with `chown: … Operation not permitted`. A named volume *is* chownable, so mount the data directory from one. Self-host composes that put data under a bind-mounted `/mnt/docker-volumes/<svc>/…` (a Linux-host convention) hit this on macOS — when a DB crashes this way, `up` points at the fix.
- **Build context**: Apple's builder can't read a context under `/private/tmp` or a symlinked directory — build from a real path under your home dir (`up` warns).
- **Won't run at all**: composes that need Linux-host kernel access (WireGuard's `NET_ADMIN` + `/lib/modules`) — Apple `container` doesn't provide it (also true of Docker Desktop for the host-path cases). Tools that drive Docker through `/var/run/docker.sock` (e.g. Portainer) also can't manage opossum's containers: bind-mounting a host Unix socket into a container *does* work now (since `container` 1.1.0), but Apple `container` exposes no Docker-compatible daemon socket — it talks to the host over XPC — so the mount has nothing on the other end.
- **cgroup-sensitive JVM images (e.g. Elasticsearch 7.x)**: the container's bundled JDK reads the host cgroup to size the heap, and Apple `container`'s VM doesn't expose the cgroup mount the way it expects — the process crashes at launch with `CgroupInfo.getMountPoint() … null` before any config applies (`ES_JAVA_OPTS`/`JAVA_TOOL_OPTIONS` don't help; observed on Elasticsearch 7.16 and 7.17). `opossum ps` shows such a service as `stopped`; check `opossum logs <svc>`. This is a runtime/JDK–VM incompatibility, not an opossum limitation.
- **Not parsed**: `configs`, `extends`, and the map form of `external`.

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
  opossum's `up` simply fails to bind (nothing is harmed). And because named-volume
  data isn't shared between the two runtimes, opossum starts such a service from a
  fresh, empty volume rather than your Docker data.

In short: **point opossum at your existing `docker-compose.yml` and try `opossum
up`** — the worst case is a port clash or an unsupported field it simply skips
(run `opossum config`, or `--verbose`, to see which), not lost data.

[← back to the README](../README.md)
