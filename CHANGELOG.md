# Changelog

All notable changes to opossum are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and this project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.8.0] - 2026-07-16

### Added

- **Declared networks, including host-only (`internal`) networks for egress
  control.** A top-level `networks:` block plus a per-service `networks: [name]`
  now place a service on a named network instead of the default per-project one.
  An `internal: true` network is created host-only (`container network create
  --internal`): no internet egress, though the host stays reachable — so an
  untrusted workload on it can only reach out through a proxy you run on the host
  (via `${OPOSSUM_HOST_GATEWAY}`), making the allowlist enforced rather than
  advisory. `external: true` (with optional `name:`) reuses a pre-existing network
  by its real name (never created or removed). opossum joins a service to at most
  one network today; on an internal network, peers can't resolve each other by
  name (use IPs). See the new "Constraining egress (agent sandboxes)" README
  section.
- `network_mode: none` now isolates a service from all networking (mapped to
  `container run --network none`): loopback only — no egress and no name
  resolution. It's the floor for sandboxing an untrusted workload, honored on
  both `up` and `run`, and toggling it recreates the container. Other
  `network_mode` values (e.g. `host`) are rejected at load rather than silently
  ignored.
- More compose run options are now applied, each a thin passthrough to the
  matching `container run` flag: `user` / `working_dir` (`--user` / `--workdir`),
  `init` (`--init`, a tini-like PID 1 that reaps zombies), `read_only`
  (`--read-only` root filesystem), and `cap_add` / `cap_drop` (`--cap-add` /
  `--cap-drop`). They're honored on both `up` and `run`, and a change to any of
  them recreates the container.
- `examples/mcp-stack` and a README section, "Run your MCP servers on Apple
  container": host MCP servers (small, idle, credential-holding) on Apple
  `container` instead of an always-on Docker Desktop. Shows the graduation ladder
  — a raw `container run` for a single secret-free stdio server, moving to a
  compose file for secrets (token in `.env`, not a committed `.mcp.json`),
  several servers, or an HTTP (streamable) server you `up`/publish a port and
  point a client at `http://localhost:8080/mcp`. Verified end-to-end with
  `hashicorp/terraform-mcp-server` (stdio and streamable-http).
- `opossum watch` now automates the `rebuild` and `sync+restart` actions
  (previously sync-only): a change under a `rebuild` rule rebuilds the service's
  image and recreates its container; `sync+restart` copies the file, then
  restarts the container. Rebuilds and restarts are batched, so a burst of edits
  triggers one per service.

## [0.7.0] - 2026-07-13

### Added

- `opossum watch` mirrors host file changes into running containers, like
  `docker compose watch`: it reads each service's `develop.watch` rules and, on a
  change under a rule's `path`, `action: sync` copies the file to `target` inside
  the container (honoring `ignore` globs). Start the stack with `up`, then run
  `watch` (Ctrl-C to stop). `rebuild`/`sync+restart` actions are parsed but not
  yet automated.
- `ssh: true` on a service (and `opossum run --ssh`) forwards the host's SSH
  agent into the container (`container run --ssh`), so a service can `git
  clone`/`push` private repositories over SSH with your host keys — without
  copying keys into the image.
- `${OPOSSUM_HOST_GATEWAY}` built-in interpolation variable expands to the
  address a container can use to reach a service running on the host (Apple
  `container` has no `host.docker.internal`), so a compose file can point a
  container at, e.g., a model server running natively on the host. Overridable
  via shell env or `.env`; a `examples/local-ai-stack` shows the pattern.
- `opossum run -T` / `--no-tty` disables the pseudo-terminal (like `docker
  compose run -T`), so `opossum run web cmd | jq` from a terminal isn't polluted
  by tty echo/CRLF.
- `opossum cp <src> <dst>` copies files between a service's container and the
  host (each path is a host path or `service:path`), like `docker compose cp` —
  a thin wrapper over `container cp` with service-name resolution.
- `opossum doctor` diagnoses the environment in one command: the `container`
  runtime, the DNS domain registration, outbound network/DNS from a probe
  container (catching a wedged default network), the build VM's memory, and — if
  a compose file is present — a rough memory estimate for the stack. Each check
  prints ✅/⚠️/❌ with a one-line fix.
- `up` warns when two services share the same named volume. Apple `container`
  attaches a named volume to only one running container at a time, so the others
  fail to start with an opaque VM error — the warning names the volume and the
  services and suggests a bind mount (or baking the data into the image).

### Fixed

- `run`'s stdout is now clean even when it starts dependencies or builds an
  image: dependency-startup, build, and volume-seeding progress go to stderr, so
  only the one-off's own stdout remains — completing the stdio bridge for tools
  like an MCP server speaking JSON-RPC over stdio (previously the build's final
  image tag leaked to stdout).

## [0.6.1] - 2026-07-13

### Fixed

- `run` now keeps the container's stdin connected (piped input reaches the
  process instead of hitting an immediate EOF) and prints its own progress to
  stderr, so the container's stdout comes through clean. Together these let
  stdio-based tools run as one-offs — e.g. an MCP server: point your MCP
  client's command at `opossum run --rm <service>`. A TTY is allocated only
  when opossum's own stdin is an interactive terminal (so `opossum run web sh`
  still gets a proper shell). One caveat: if the run first has to build the
  image or start dependencies, that output still reaches stdout — for a clean
  stdio pipe, use a service without a `build:` (or pre-build) and `--no-deps`
  (or pre-start the deps with `up`).

## [0.6.0] - 2026-07-09

### Added

- `opossum import [service…]` copies a service's Docker-built image into
  `container`'s store (`docker save` → `container image load`), so `up` starts it
  without rebuilding in Apple's builder — handy for onboarding (reuse images
  `docker compose` already built) or when Apple's builder can't handle a
  Dockerfile. A failed build now points to this fallback. `docker` is only
  invoked by `import`.
- `up --from-docker` does the import inline: for each service with a build, it
  imports the Docker-built image instead of building, then starts — a one-command
  onboarding path for a project you already `docker compose build`.

## [0.5.0] - 2026-07-09

### Added

- Multiple `-f` compose files are merged in order, and a `compose.override.yaml`
  (or `docker-compose.override.yml`) beside the base file is applied automatically.
- `logs --follow` across several services multiplexes their output into a single
  stream with per-service prefixes.
- `config` honors `--profile` / `COMPOSE_PROFILES`, showing only the services
  that would start.
- Resource limits are applied: `mem_limit` / `cpus` and
  `deploy.resources.limits.{memory,cpus}` are passed to the runtime as `-m` / `-c`.

- On an interactive terminal, `up` shows a "still working" spinner during long
  silent build phases (context transfer, base-image pull) so it no longer looks
  frozen. Piped/redirected output is unchanged.
- When a build fails from a corrupted builder cache or from the builder running
  out of resources, `up` prints an actionable hint (reset the builder, or give it
  more CPU/memory) instead of leaving you with the raw builder error.
- README troubleshooting for builds: giving the shared builder more CPU/memory
  when a heavy build is slow or fails with `Unavailable`/`EOF`, resetting a
  corrupted builder cache, and trimming a large build context.

### Fixed

- A bare container port in `ports` (e.g. `- "3000"`) now works: it's published
  as `3000:3000` instead of failing with `invalid publish value` (Apple
  `container` requires a host port).
- The Postgres named-volume warning is now actionable: it says the service
  won't start, names the fix (set `PGDATA` to a subdirectory), and tells you to
  re-run `up` — and no longer includes an internal tracking number.

## [0.4.0] - 2026-07-08

### Added

- `profiles` support: services in a profile don't start by default; enable them
  with `--profile <name>` or `COMPOSE_PROFILES`. `run` also honors profiles (a
  gated dependency is an error).
- `up --remove-orphans` removes containers for services deleted from the compose file.
- `--env-file <path>` overrides which file supplies interpolation variables
  (instead of the default `.env` next to the compose file).
- `up` is now idempotent: an unchanged service isn't recreated and an existing
  image isn't rebuilt, so re-running `up` is fast and non-destructive.
- Calmer `up` output: build progress is always shown; harmless warnings move
  behind `--verbose`.

### Fixed

- `up` applies a healthcheck's `timeout` (clamped to 30s for 0/negative values),
  so a hanging probe no longer blocks `up` indefinitely.
- Ctrl-C during `up` rolls back cleanly, killing in-flight build/run/probe
  children and leaving no orphaned containers or network.
- `up --foreground` recreates and attaches even when the service is unchanged.

## [0.3.0] - 2026-07-07

### Added

- `--verbose` prints each `container` command opossum runs, for debugging what's
  sent to the runtime.

### Fixed

- `env_file` now parses multi-line quoted values and `:`-separated entries.
- `env_file` values with an unterminated quote now error clearly, matching
  docker compose, instead of being silently mishandled.

## [0.2.0] - 2026-07-06

### Added

- Bind mounts now expand a leading `~` to the home directory, and a missing bind
  source directory is created before start (matching docker compose) instead of
  failing with `path '~/...' does not exist` (e.g. `~/minecraft_data:/data`).
- `platform:` is passed to `container run --platform`; `linux/amd64` also enables
  Rosetta (`--rosetta`), so an x86-64-only image (e.g. `redislabs/redismod`) runs on
  Apple silicon instead of failing with "does not support required platforms".
- `up` pre-flights published host ports and fails fast with a clear message if one
  is already in use, instead of starting some services and then hitting the
  runtime's raw `bind: address already in use` on a later one. On macOS, a taken
  port 5000/7000 gets an AirPlay Receiver hint (a common surprise).
- `opossum images` lists each service's image, whether opossum builds it
  (`<project>-<service>:latest`) or pulls it, and whether it's present locally —
  the image-side counterpart to `ps`.
- `down --rmi local|all` removes images on teardown: `local` deletes the images
  opossum built for the project, `all` also deletes the pulled `image:` ones, so
  build artifacts from `up`/`build` can be cleaned up.
- Volume seeding + anonymous volumes: a fresh named or anonymous volume is now
  filled from the image's contents at its mount path before the container starts
  (mirroring Docker; Apple `container` mounts a fresh volume empty). This makes the
  common dev pattern — a bind-mounted source plus a volume to preserve the image's
  `node_modules` (e.g. `- /app/node_modules`) — work **unmodified**. A single-path
  entry is treated as an anonymous volume (namespaced per service, removed by
  `down -v`), not a bind mount. Existing volumes are never re-seeded, so data is
  preserved across re-ups.
- After starting a service that publishes ports, `up` prints the host-reachable
  address (e.g. `↳ web on the host: localhost:4200`), so it's clear where to open
  the service — the runtime echoes the container's `<svc>.<project>.<domain>` DNS
  name, which is for container-to-container resolution, not a URL the host can open.
- `opossum stats [service…]` streams live resource usage (CPU %, memory, net,
  block I/O, pids) for the project's containers, like `docker stats`; `--no-stream`
  prints a single snapshot.
- `up` warns when a service mounts a named volume directly at Postgres's data
  directory (`/var/lib/postgresql/data`) without redirecting `PGDATA` to a
  subdirectory — the mount point isn't empty, so `initdb` fails. It's the most
  common snag in real self-hosted app composes. (MySQL/MariaDB are unaffected.)
- tmpfs mounts (`container run --tmpfs <target>`, an in-memory filesystem) via
  either a `type: tmpfs` volume entry or the service-level `tmpfs:` field
  (string or list); both fold together, split out from bind/named `-v` mounts.
- `up` warns when a build context is somewhere Apple's `container` builder can't
  read — under `/private/tmp` or a symlinked directory — with a hint to build
  from the real path, instead of failing opaquely at `COPY` time.
- Long-form `env_file` entries (`{path, required}`) are accepted; an absent file
  marked `required: false` is skipped instead of erroring, so repos that gitignore
  a `.env` run without one.
- Long-form `volumes` entries (`{type, source, target, read_only}`) are accepted
  alongside the short `src:dst[:ro]` string, so real docker-compose files that
  use the mapping form parse and run as-is.
- `build.target` selects a multi-stage build stage (`container build --target`),
  so a service that pins a stage builds that one instead of the final image.
- File-based `secrets` are mounted read-only at `/run/secrets/<name>`, so images
  that read credentials via the `*_FILE` pattern (e.g. `POSTGRES_PASSWORD_FILE`)
  work. Short (`- name`) and long (`{source, target}`) service refs are accepted;
  `external` secrets are rejected and `uid`/`gid`/`mode` are not applied.
- Compose-file discovery: with no `-f`, opossum looks for `compose.yaml`,
  `compose.yml`, `docker-compose.yaml`, then `docker-compose.yml` in the working
  directory, so an existing `docker-compose.yml` runs as-is.
- `env_file` support: a service's `KEY=VALUE` env files are folded into its
  environment (explicit `environment` overrides them).
- `up` warns about compose fields it parses but doesn't act on (e.g.
  `container_name`, `restart`), so they aren't silently ignored.
- `opossum exec [-it] <service> <command>` runs a command in a running
  service's container; flags after the service name pass through to the command.
- `opossum build`, `pull`, `start`, and `kill` (`-s/--signal`) commands, each
  operating on the whole project or named services. See the README command
  support table.
- `opossum run [--rm] [--no-deps] <service> [command]` starts a one-off
  foreground container for a service (distinct name, no published ports); it
  starts dependencies first unless `--no-deps`.
- `opossum config [--services]` validates and prints the resolved compose
  configuration (interpolation and `env_file` applied), noting any ignored
  fields.
- `up` and `config` also surface ignored **top-level** compose keys (e.g.
  `networks`, `volumes`), not just per-service ones.
- `opossum down -v/--volumes` removes the project's named volumes after teardown.
- A top-level volume declared `external: true` is used by its real name (not
  namespaced per project) and is never removed by `down -v`, matching docker
  compose's protection of user-managed volumes.

### Changed

- `up --foreground` now errors immediately when more than one long-running service
  would start (it can only attach to one, and the runtime's foreground `run` blocks
  until the container exits, so the rest would never start). Use it with a single
  service, or drop it to start the whole stack detached. One-shot dependencies
  don't count.
- `ps` now lists only containers that exist: a service that was never created or
  was removed by `down` is omitted (rather than shown as a dead `stopped` row), so
  after a teardown `ps` is empty — matching docker compose. Existing stopped
  containers still appear as `stopped`.
- When a `service_healthy` dependency's container has exited while opossum waits
  for it, `up` now fails fast with `container is not running … check
  \`opossum logs <svc>\`` instead of an opaque "healthcheck did not pass".
- Named volumes are now namespaced by project (`<project>_<volume>`, matching
  docker compose), so concurrent projects that share a volume name no longer
  collide on one global volume — and `down -v` only removes *this* project's
  volumes. Bind mounts are unaffected. (Volumes created by an earlier opossum
  under the bare name are not migrated; recreate them or reference the old name
  explicitly as a bind/`-v` mount.)

## [0.1.0] - 2026-07-03

First tagged release. Everything opossum can do so far.

### Added

- **Dependency-ordered orchestration.** Parse a compose subset, topologically
  sort services by `depends_on` (cycles rejected), start them in order on a
  shared per-project network, and tear down in reverse.
- **Service discovery by bare name.** Each container is named
  `<service>.<project>.<domain>` and searches `<project>.<domain>`, so peers
  resolve one another by their bare service name over the project network.
- **Multiple projects at once.** The `<project>` segment namespaces containers,
  so stacks that share service names run concurrently under a single registered
  DNS domain, each on its own `<project>-net` — no per-project setup. A
  `opossum.project` label + pre-flight guard refuses to clobber another
  project's containers.
- **`depends_on` conditions.** `service_healthy` gates a dependent until the
  dependency's `healthcheck.test` passes (polled via `container exec`, since the
  runtime has no native healthcheck). `service_completed_successfully` runs a
  one-shot dependency to completion and gates on its exit code.
- **`healthcheck`** — `test` (`CMD` / `CMD-SHELL` / string), `interval`,
  `timeout`, `retries`, `start_period`.
- **`.env` / `${VAR}` interpolation** — `$VAR`, `${VAR}`, `${VAR:-default}`,
  `${VAR-default}`, `${VAR:?required}`, and `$$`; values from a `.env` file next
  to the compose file, overridden by the shell.
- **`command` and `entrypoint`** — list form verbatim, string form shell-word
  split; `entrypoint` overrides the image ENTRYPOINT.
- **Commands** — `up [service…]` (whole project, or named services plus their
  dependencies), `down`, `ps` (service / container / IP / ports / status from
  `container inspect`), `logs [service…]` (`--follow`, `-n/--tail`), `stop`, and
  `restart`.
- **Clean-failure semantics.** A failed `up` rolls back the containers it started
  and removes the network if it created it.
- **Two-layer verification.** A fake `container` shim
  (`testdata/fake-container.sh`, kept in sync with the real CLI via
  `testdata/real-cli-output.md`) drives fast, unattended tests of the emitted
  command sequences; a documented real-`container` review
  ([`docs/real-runtime-review.md`](docs/real-runtime-review.md)) confirms
  behavior on macOS 26.

### Fixed

- `ps` no longer reports a published port's `0.0.0.0` host address as a
  container's IP (typed inspect parsing preferring the interface IPv4/IPv6).
- `ps` STATUS now reflects the real `status.state` instead of being inferred
  from whether an IP was assigned.
- A string `command` is shell-word-split, so `command: sh -c "…"` reaches the
  runtime as argv instead of one opaque argument.
- `down` no longer warns when re-run against an already-removed network.

### Known limitations

- Named volumes are passed through untouched; only bind-mount host paths are
  resolved to absolute paths.
- `restart` reassigns a container's IP (the runtime does this on `start`); the
  name and config are preserved, so name-based discovery is unaffected.

[Unreleased]: https://github.com/suruseas/opossum/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/suruseas/opossum/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/suruseas/opossum/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/suruseas/opossum/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/suruseas/opossum/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/suruseas/opossum/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/suruseas/opossum/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/suruseas/opossum/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/suruseas/opossum/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/suruseas/opossum/releases/tag/v0.1.0
