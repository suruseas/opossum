# Changelog

All notable changes to opossum are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and this project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.20.0] - 2026-08-19

### Changed

- The hints opossum decodes out of a failed start now carry a diagnostic code, so
  they can be looked up in `AGENTS.md` rather than only read: an image with no
  arm64 build is `OPSM-412` (new), while a host port the pre-flight could not see
  reuses `OPSM-201` and an unresolvable file bind mount reuses `OPSM-107` — same
  cause and same fix as the pre-flight checks that name them, just caught later. A
  start failure opossum cannot decode stays uncoded, so a diagnosed failure and an
  undiagnosed one still look different.
- `up` and `run` now stop when a bind mount's host source is a symlink pointing at
  a socket, naming the path the link resolves to — mounting that instead is the way
  through, because a socket reached by its own path mounts fine, and so does a
  symlink to a file or a directory. It is the combination that Apple `container`
  1.1.0 refuses, and until now opossum attempted it and passed on four levels of
  nested `internalError` about `errno 95`. The common way to meet this is
  `/var/run/docker.sock` on a machine where Docker Desktop has linked it. The check
  only reads the host, so `--dry-run` reports it too.

### Fixed

- `.env` values that reference other variables are now expanded, matching Docker
  Compose (measured against v5.3.1). A line like
  `DATA_PATH=${DATA_ROOT:-/mnt/data}/psql` used to reach the compose file verbatim,
  so a mount written as `${DATA_PATH}:/var/lib/postgresql/data` split at the colon
  inside the default and the runtime was handed `${DATA_ROOT` as a volume name. The
  rules follow Compose: only keys defined above the line are in scope, an unresolved
  reference expands to empty, a single-quoted value is left alone, and the shell
  still wins over the file. Values in a service's `env_file:` are expanded the same
  way. Where several env files apply, they fill one map top to bottom — a later file
  overrides an earlier one, and a file's own line wins over a file read before it —
  while an outer level always wins. The levels, outermost first: the shell, the
  project's `.env` (or `--env-file`), a service's own `environment:` block, then that
  service's `env_file:` files.

  Two kinds of env-file line that used to load now fail — in a `.env`, an
  `--env-file`, or a service's `env_file:` alike, in both cases the way Compose
  already fails on them: a value holding a required reference (`${VAR:?message}`)
  when that variable is unset, and a value holding an unterminated `${`. A value can
  also change meaning wherever env files are read — `$` now starts a reference, so
  `PASSWORD=s3cr3t$pass` reads `$pass` as a variable. Write `$$` for a literal `$`,
  or single-quote the value.

## [0.19.1] - 2026-08-07

### Changed

- `up` now stops when a bind mount's host directory could not be created, instead
  of warning and starting the service anyway. It used to report the problem and
  then attempt the mount, so the warning was followed a moment later by the
  runtime's own `path '…' does not exist` with nothing tying the two together.
  Nothing is lost by stopping: the start was going to fail, and a failed start
  rolls the project back regardless.
- A bind mount whose host source is a symlink pointing at nothing now says so,
  rather than reporting that the directory could not be created and suggesting a
  `mkdir -p` that fails the same way.

### Fixed

- A volume whose mount target contains a `$`, a backtick, or a tab or newline —
  `data:/app/$(id)` — is now seeded as the path it is. The script opossum runs to fill a new volume
  quoted those paths the way Go does, which a shell reads as its own quoting: the
  copy would look somewhere other than the path you wrote and silently leave the
  volume empty, or run what was written in it.
- `up` no longer reports success over a service that died a moment after
  starting. It used to look once, the instant the container was launched, which is
  before a service with a bad config has finished failing — a postgres with no
  password set, or refusing a data directory it cannot initialise, was still
  "running" at that instant, so `up` exited 0 and the failure only showed up as
  `stopped` in `opossum ps`. It now looks again a second later and reports the
  exit with the service's logs. `OPOSSUM_CRASH_GRACE` sets that window;
  `OPOSSUM_CRASH_GRACE=0` gives the second back and, with it, gives up catching
  anything that doesn't die instantly.
- A new volume whose mount target begins with `-` — `data:-rf` — is filled from
  the image again. opossum copies the image's contents at that path into the fresh
  volume, and the copy read the target as options rather than as a path, so it
  failed and the volume came up empty behind a "couldn't fill the new volume"
  warning.

## [0.19.0] - 2026-08-05

### Added

- `volume: {nocopy: true}` now works, and so does its short spelling `src:target:nocopy`. opossum fills a fresh volume from the image the way Docker does, and this is how a compose file turns that off — for a dependency directory the image ships stale, say, or one that is populated another way. It was being dropped during parsing, so the volume was filled anyway and nothing said why the line had no effect; the short spelling was worse, reaching the runtime as if `nocopy` were a mount mode. Measured against docker compose, which leaves such a volume empty.
- `up` now says when it couldn't fill a fresh volume from the image. opossum emulates Docker's volume seeding by running `cp -a` inside a throwaway container built from that image, and an image with no shell — most distroless and scratch builds — has nothing to run the copy with. The volume then mounts empty, which looks exactly like a service that lost its data. The warning names the volume, says why the copy couldn't run, and points at the ways out: put the content there another way, or add `volume: {nocopy: true}` to record that an empty mount is what you meant (`OPSM-108`).

### Changed

- `up --from-docker-compose` no longer proposes a named volume for every empty read-write bind directory. It used to read an empty directory as "the app will put its data here"; measured over 156 real-world projects that guess produced 193 proposals and about half were wrong — `/config` files you edit, `/downloads` and `/media` you open, `/logs` you read. A suggestion that is a coin flip is worse than none, because it teaches you to skip the section that also holds the good ones. Suggestions are now driven by what actually happened: when a container dies taking ownership of a bind mount — the failure Apple `container` produces because bind mounts are host-owned — that service and that mount are remembered, and the next `up --from-docker-compose` proposes a named volume for exactly that mount — written into the overlay when there isn't one yet, and reported on screen when there is (opossum never overwrites an existing `compose.opossum.yaml`). The automatic fixes for databases whose behaviour is known (Postgres, MySQL/MariaDB, ClickHouse, MongoDB, Redis) are unchanged.
- Declining `opossum destroy` at the prompt now ends with a pointer to `opossum destroy --dry-run`. Saying no skips the after-removal report, so nothing ever told you about the things destroy leaves alone (the DNS domain, workspace snapshots, unclaimed volumes) — the one-line pointer keeps them discoverable without burying a "no" in output.
- The warning about Postgres's data directory (`OPSM-101`) now reports what opossum found instead of predicting from the shape of the mount. It used to fire whenever a named volume sat directly at `/var/lib/postgresql/data`; with `lost+found` now cleared from the volumes opossum creates, that mount is the one that works, and the prediction would have been wrong on every `up` of a healthy stack — a warning that is wrong half the time teaches you to skip the paragraph that also holds the true ones. In its place: before the service starts, opossum reads a volume that already exists and warns only if it holds `lost+found` and no cluster; and if a service dies anyway, initdb's own refusal is decoded into the same guidance. What is left is the case that is genuinely still broken — a volume opossum didn't create, made by an older opossum, by `container volume create`, or by another project — and the message names it, says what it costs to recreate it, and gives the `PGDATA` route as the alternative that keeps the data.

### Fixed

- Filling a fresh volume from an image whose default user isn't root — anything with a `USER` line, which is the node images and most database images — copied nothing at all, and said nothing about it. A fresh volume's root belongs to `0:0` and is mode 755, so the image's own user cannot create a single file in it: this was never a matter of ownership not surviving the copy, the volume simply came up empty, and a service that had lost its data looked exactly the same. The copy now runs as root, which is the privilege Docker seeds with — there the engine does the copying — so the contents and their ownership both arrive intact. Failures of the copy are no longer swallowed either: one that starts and fails is reported (`OPSM-108`), while a path the image doesn't have stays quiet, because that one is the ordinary case.
- The compatibility documentation said opossum does not seed volumes. It does, and has since volume support landed: a fresh named or anonymous volume is filled from the image's contents at that path before the service starts, which is what makes the bind-mounted-source plus `- /app/node_modules` pattern work. The docs told you to work around a limitation that isn't there — installing dependencies at container start, or not using a volume for them — so if you did that, you no longer need to. The docs now say what is actually emulated and what still isn't (an image without a shell is skipped, `external: true` volumes are never touched).
- A fresh named or anonymous volume now starts empty, the way it does on Docker. Apple `container` gives every volume its own ext4 filesystem, so one arrives already holding `lost+found` — where a Docker volume, being a directory on the host, holds nothing — and any program that checks whether its data directory is empty sees the difference. Postgres is the one people hit: `initdb` refuses to initialise into a directory that isn't empty, and says so by name. opossum now clears `lost+found` out of the volumes it creates, so a compose file that writes `pgdata:/var/lib/postgresql/data` works here as it does on Docker, with no need to move `PGDATA` into a subdirectory of the mount. Volumes opossum didn't create are left alone, and the clearing is an `rmdir`: a `lost+found` that holds anything — files an fsck recovered — makes it fail and stay, so a step meant to make a volume look like Docker's cannot destroy what it finds there.

## [0.18.2] - 2026-08-01

### Fixed

- Correction to the 0.18.1 note on `healthcheck: disable: true`. That note said the check stayed live and a service depending on it with `condition: service_healthy` waited on a check you had switched off. That is what happened when `disable: true` was written beside a `test:` that actually ran, and that case is genuinely fixed. Written on its own, the line never got that far: the healthcheck looked absent rather than disabled, so a `service_healthy` dependant was already turned away at load — with the same message, before and after the fix. What 0.18.1 changed for `disable: true` on its own is that `opossum config` now echoes it back instead of dropping it.

## [0.18.1] - 2026-08-01

### Fixed

- `healthcheck: disable: true` now works. Only the `test: ["NONE"]` spelling was read, so the compose spec's other way of switching a healthcheck off did nothing at all — the check stayed live, and a service depending on it with `condition: service_healthy` waited on a check the user had switched off. Both spellings now mean the same thing, and `disable: true` wins over a `test:` written beside it.

## [0.18.0] - 2026-07-31

### Added

- `up` now says so when a bind mount names a file that isn't there. A missing bind source has to be created or the container won't start, and a directory is the only thing that can be created — so a file you were meant to supply (`./init.js:/docker-entrypoint-initdb.d/init.js`) quietly became a directory, the service started anyway, and the init script simply never ran. The failure showed up later as something else entirely. opossum now names the path, says a directory is standing in for the file, and tells you how to put the real one there (`OPSM-107`).

### Changed

- `destroy` now names any `.opossum-snapshots/` directories it found, alongside the DNS domain and the builder cache it already reported. Snapshots are still never removed — a snapshot belongs to the directory that was snapshotted, and that directory outlives any number of projects — but they are usually the largest thing left on disk, and a command that promises to leave no trace shouldn't be the reason you find them a month later.

### Fixed

- `up --from-docker-compose` no longer suggests a named volume for a bind mount that passes a single file through (`./init.js:/docker-entrypoint-initdb.d/init.js`). A bind source that doesn't exist yet is created as a directory whatever it was meant to be, so a file you were told to supply looked exactly like an empty data directory — and the suggestion, if applied, would have hidden the file you were about to put there.
- A service that `up` left alone because it was already up to date now keeps its `restart:` supervision when a later service fails the bring-up. The failed start is rolled back, but an untouched container is not part of that rollback — it is still running, and opossum was reporting that nothing was left, so nothing watched it.
- `destroy --dry-run` now reports what it would leave behind — the DNS domain, the build cache, and any workspace snapshots — the same way the real run does. The preview is the mode you read before deciding, and it was the one that didn't say: when there was something to remove, the list of what stays came only after it was gone.

## [0.17.0] - 2026-07-30

### Added

- `restart:` is now honoured. A project that declares a policy gets a small supervisor of its own, started by `up` and stopped by `down`, that brings a service back when it exits — the job Docker's always-running engine does and Apple `container` has no equivalent for. `always` and `unless-stopped` behave as they do under Docker, including leaving a service you stopped on purpose alone. `on-failure` can only be approximated: the runtime doesn't report a container's exit code, so a crash and a clean exit look the same, and opossum retries a bounded number of times rather than looping a service that may have finished. `opossum config` says so for the services affected.
- `opossum destroy` removes everything opossum created for a project in one command: containers (including orphans), the project network, named volumes, images it built or pulled, the restart supervisor, the `.opossum/` state directory and the generated `compose.opossum.yaml`. Your compose file, `.env` and sources are never touched, and neither is anything shared — volumes declared `external: true` and other projects' containers stay, while the DNS domain and the build cache are reported with the command to remove them rather than removed for you. It lists what it will remove and asks first; `--force` skips the question for scripts and agents, `--dry-run` lists and stops, and `--keep-overlay` keeps `compose.opossum.yaml` in case you edited it.
- `opossum destroy` now reports volumes it cannot account for instead of leaving them invisible. A volume named for the project that no service claims — left behind when a service was renamed or removed from the compose file — is listed as kept, with the command to remove it, because opossum cannot tell such a volume from an `external: true` one by name alone. Previously the teardown reported that everything was gone while these stayed on disk.

### Changed

- The restart supervisor's log is now capped at 1MB. A service that fails permanently is restarted for as long as its policy asks, and each attempt writes a line — about 270KB a day, previously without limit, for a project you may have forgotten about. On reaching the cap the log keeps its newest half and says so on its first line (`[OPSM-410]`); the recent lines are the ones that explain why a service is down.

### Fixed

- A service declaring `restart:` is no longer left unwatched when `up` fails. A service that exits immediately after starting fails the up, but the rest of the stack stays running — opossum stopped there without starting its supervisor, so `restart: always` quietly did nothing for the containers that were still up. A bring-up that fails and is rolled back still starts no supervisor: nothing survives it to watch.
- `opossum up <service>` no longer stops watching the services it didn't touch. Bringing up one service used to replace the project's restart supervisor with one covering only that service, so anything else still running quietly lost the `restart:` policy the compose file gives it. A partial up now watches what it started plus whatever was already being watched and is still there.
- `opossum destroy -p <other-project>` no longer removes the current directory's generated files under another project's name. The runtime objects belong to the name you gave; `.opossum/` and the generated `compose.opossum.yaml` belong to the directory you are standing in, which is usually a different project. The plan now names that directory and says whose files they are, and `--force` refuses rather than act on the guess — add `--keep-local` to remove only the named project's containers, volumes, images and supervisor.

## [0.16.0] - 2026-07-29

### Added

- `compose.opossum.yaml` is now the whole compatibility picture for a project, not just the fixes: alongside the changes opossum **applied**, it records **suggestions** (a concrete change written out but commented — it alters what the project means, so it's yours to decide; uncomment the block to apply it) and **notes** (things no compose change can fix, like a Docker socket mount or a host device). Each entry says which of the three it is, with a stable marker. `up` reports the three separately, so a note is never counted as a change opossum made. Suggestions cover a named volume shared by several services (which Apple `container` can't attach twice) and an application's own data directory on a bind mount; notes cover Docker socket mounts and host devices. An overlay is written only when there is something to apply or suggest — findings that are notes alone are reported but don't create a file.

### Changed

- The automatic fix for a database data directory on a bind mount now also covers **ClickHouse**, **MongoDB** and **Redis/Valkey** (previously Postgres and MySQL/MariaDB only), each confirmed on the real runtime to fail the same way. Every chowned directory on a service is fixed in one pass — MongoDB has two (`/data/db` and `/data/configdb`), and fixing only one left the container still crashing. A service built on a database image but running a client or dump command (`redis-cli`, `mongodump`, a shell) is left alone: those read a bind mount fine, so rewriting one would have swapped real data for an empty volume.
- A `ports:` entry that names only a container port (`ports: ["3000"]`) no longer fails when the matching host port is taken. Compose leaves the host port to the engine for those, so opossum now falls back to a free one and says which (`opossum ps` shows the ports actually published) instead of refusing to start. The same-number mapping is still preferred whenever it's available, and an explicit `"3000:3000"` is never moved — that one still fails loudly, since it's a contract you wrote down.

### Fixed

- The host-port pre-flight (`OPSM-201`) now detects a port held by a listener bound to all interfaces over IPv4. It probed with a single dual-stack bind, which on macOS succeeds alongside such a listener — so the port read as free and the run failed later with the runtime's raw bind error instead. This was the case the check most needed to catch: the daemons that squat ports, AirPlay's receiver on 5000/7000 included, listen on IPv4. (A daemon bound only to `127.0.0.1` is still not detected; the runtime does bind such a port, but traffic reaches the other listener rather than the container.)

## [0.15.0] - 2026-07-28

### Added

- A `compose.opossum.yaml` (or `.yml`) next to a discovered compose file is now auto-merged **last, at the highest precedence** — after the base file and any `compose.override.yaml`. docker compose ignores this name, so the same directory works with both tools and your original files stay untouched: keep Apple-`container`-specific tweaks here. Merging one prints a one-line notice naming the file (delete it to opt out).
- `up --from-docker-compose` now **writes the fixes a docker compose project needs to run on Apple `container`** into a `compose.opossum.yaml`, then starts — so bringing a project over is one command instead of read-warning-edit-retry. It handles the two patterns that fail for runtime reasons rather than mistakes in your file: a named volume mounted at Postgres's data directory (`PGDATA` is pointed at a subdirectory of it) and a database data directory on a bind mount (swapped for a named volume — note this changes where the data lives; the host directory is left untouched). Every entry says what changed, why (with its diagnostic code), how to verify it, what to do if it still fails, and how to undo it. An existing `compose.opossum.yaml` is never overwritten, your own compose file is never modified, and nothing is written when there's nothing to fix.
- A Japanese README ([`README.ja.md`](README.ja.md)), and a diagram in the **Networking model** section (mermaid) contrasting the docker-compose and Apple-`container` network models side by side.

### Changed

- `up --from-docker` is now **`up --from-docker-compose`**. The flag is the switch for bringing an existing docker compose project up here, and the new name says so. The old `--from-docker` still works exactly as before and prints a one-line notice pointing at the new name, so existing scripts and examples keep running.

### Fixed

- `volumes` entries that mount the same container path now collapse to one, matching docker compose: the last entry wins, whether the duplicates come from one file or from a file and its override. Previously every entry was passed to the runtime, so two sources could end up mounted at a single path — and an override couldn't swap a bind mount for a named volume.

## [0.14.0] - 2026-07-24

### Added

- `opossum doctor` now checks **reclaimable storage**: it reads `container system
  df` and warns when a large amount of image/volume storage is unused by any
  running container, with the reclaim command (`container image prune -a`). Apple's
  `container images ls` doesn't list untagged images, so build/pull leftovers can
  fill the disk unseen — this surfaces them before that happens. The amount is
  shown even when it's within a normal cache.
- When a build fails because the host ran out of disk (`no space left on device`),
  `build`/`up --build` now decodes it into the fix — free space with
  `container image prune -f` and `container builder delete --force`, then retry —
  instead of forwarding the raw builder error. A real build pulls multi-GB base
  images and layers onto the host volume, so this is a common failure; the remedy
  is the opposite of the resource-starvation one (growing the builder would only
  use more disk). Found dogfooding real builds.

### Changed

- When a service fails to start for a reason the pre-flight can't catch, `up` now
  decodes the cryptic runtime error into the fix instead of just forwarding it:
  a host-port conflict the host probe can't see (Apple `container`'s built-in DNS
  holds port 53, so a DNS server like AdGuard/Pi-hole clashes) → remap the port;
  an image with no arm64 build (`does not support required platforms`) → add
  `platform: linux/amd64` (opossum runs it via Rosetta); a bind mount of a config
  file whose host source is missing → create the file first (opossum makes a missing
  bind source a directory, which can't mount onto a file path). Found dogfooding
  real self-host composes.

## [0.13.0] - 2026-07-24

### Added

- When a database container crashes because it can't `chown` a **bind-mounted**
  data directory (Apple `container`'s bind mounts are host-owned and not chownable —
  common with self-host composes that put data under `/mnt/docker-volumes/…`), the
  crash report (`[OPSM-401]`/`[OPSM-407]`) now points at the fix: use a **named
  volume** for that directory. Previously the raw `chown: … Operation not permitted`
  was shown without the remedy.
- `up` now fails up front (`[OPSM-205]`) when a service joins a network declared
  `external: true` that doesn't exist — opossum uses external networks by name and
  never creates them, so a missing one used to surface only as a raw "network not
  found" when the service tried to start. The error names the network and gives two
  fixes (create it, or drop `external:`). Common with reverse-proxy composes that
  expect a shared `proxy` network.
- `up` now catches a service that **exits right after starting** even when nothing
  gates on it (no healthcheck or `depends_on`). Previously such a service — a bad
  config, a failed Postgres `initdb`, a missing mount — left `up` reporting success
  (exit 0) over a dead container. Now `up` prints the crashed service's last log
  lines (`[OPSM-407]`) and exits non-zero, so "started" never masks "already dead".
  The containers are left up for inspection (not rolled back). A dependency crash
  caught by a health gate is still `[OPSM-401]`.

### Changed

- `opossum run` (a one-off) now gets the same named-volume conflict handling as
  `up`: it warns up front (`[OPSM-103]`) when a running container — including the
  service's own `up` container — already holds a volume the one-off needs, and if
  the run still hits the cryptic exclusive-attach VZError it's decoded to the same
  clear message naming the volume and holder. A one-off's ordinary non-zero exit is
  untouched, so `run` keeps propagating exit codes.
- More actionable error messages across the remaining lifecycle and teardown paths
  (second pass of the error audit): a failed `start`/`restart` says the container
  must exist first (`opossum up`); a failed `pull` names the image and points at
  registry auth/network; a generic service-start failure points at `opossum logs`;
  `logs` points at `opossum ps`; the `watch` rebuild/restart/sync/setup/error
  warnings each carry a fix (and sync now names the file and service); Docker-import
  failures explain what to check. Best-effort teardown (`down --volumes`/`--rmi`)
  no longer swallows a real failure silently — a volume or image that won't delete
  now warns with a next step, while a clean re-run (already gone) stays quiet.

### Fixed

- Variable interpolation now resolves a reference **nested inside another's
  default** (`${A:-${B:-x}}`) and handles a reference written across lines with YAML
  double-quoted `\`-continuations. Previously a nested `${…}` was truncated at the
  first `}` and a multi-line reference failed to parse — both are used by real
  self-host composes (e.g. rocketchat's MongoDB replica-set URL).

## [0.12.0] - 2026-07-23

### Added

- `up`/`run` now print a one-line `note:` when the compose file has fields opossum
  ignores (e.g. `dns_search`, `container_name`), pointing to `opossum config` for
  the full list — so a dropped field never silently looks like it took effect. It's
  a low-key note, not a warning; `--verbose` still lists each ignored field. AGENTS.md
  now also documents that `dns`/`dns_search` are ignored and that service discovery
  is automatic (bare service names under `<project>.opossum`), so there's no reason
  to set them.
- When a service fails to start because a named volume is **already attached to
  another running container** (Apple `container` attaches a named volume to only
  one running container at a time), opossum now decodes the cryptic virtualization
  error (`VZErrorDomain Code=2 "The storage device attachment is invalid"`) into a
  clear `[OPSM-103]` message that **names the volume and the container holding
  it** — including a holder from a *different* project — and tells you how to free
  it. The same conflict is also flagged as a pre-flight warning at `up` time when
  the holder is already running, so you see it before the failed start.
- `opossum run --audit` reports what a one-off did after it finishes — the
  "verify" half of declaring what an agent may do: the workspace file diff
  (added/changed/deleted + content hashes, from an APFS snapshot taken just before
  the run), the egress destinations (read from the allowlist proxy's log when the
  run routes through one; otherwise marked *unobserved* rather than a misleading
  blank), and the exit code. `--audit-format json` gives a machine-readable report
  (like `doctor --format json`); the container's own stdout goes to stderr so the
  report owns stdout.
- A service can declare the MCP servers an agent inside it should use with the
  `x-opossum-mcp-tools` compose extension, and opossum generates a `.mcp.json` and
  mounts it read-only at `/run/opossum/mcp.json` — so "which tools this agent has"
  is declared in the compose file, not hand-wired. Each entry is another service
  (`svc`, `svc:port`, `svc:port/path` — reached by name on the shared network) or an
  explicit `name=url`. Pass it to Claude Code with `--mcp-config /run/opossum/mcp.json`.
  MVP is HTTP-transport MCP servers. See `examples/agent-sandbox`.
- `opossum ws snapshot [name]` / `ws ls` / `ws rollback <name>` snapshot and roll
  back a workspace directory (`--path`, default `./work`) using APFS copy-on-write
  clones — a snapshot is near-instant and uses almost no extra disk, so an agent
  can try something risky and reset in an instant. `rollback` saves the current
  state first (reversible); snapshots live in `.opossum-snapshots/` beside the
  workspace; on a non-APFS filesystem it falls back to a full copy and says so.
  `ws rm <name>…` deletes named snapshots and `ws prune` clears out the auto-saves
  `rollback` accumulates (`--keep N` to keep the newest few, `--all` for every
  snapshot), so they don't pile up.
- `OPOSSUM_DOCKER_BIN` overrides the `docker` CLI that `opossum import` shells out
  to (mirroring `OPOSSUM_CONTAINER_BIN`) — useful when docker lives on a
  nonstandard path or you want `import` to use a specific docker-compatible CLI.

### Changed

- When the `container` runtime isn't running, a **mutating** command (`up`, `run`,
  `build`, `pull`, `down`, …) now **starts it automatically** (`container system
  start` — a light, idempotent launchd start) and proceeds, printing a one-line
  notice (`[OPSM-406]`) that also explains *why* it was needed. Previously `up`
  began work and then failed mid-way with the runtime's raw error. Read-only
  commands (`ps`/`images`) still report `[OPSM-405]` without starting anything (a
  read shouldn't have a side effect), and both messages now say why the runtime
  needs starting. Opt out of auto-start with `OPOSSUM_NO_AUTO_START`.
- Clearer, more actionable error messages across common failures (first pass of a
  broader audit): a malformed compose file now says it's invalid YAML and where to
  look (instead of a raw parser dump); `depends_on`/`secrets`/duration/memory/cpus
  mistakes now show the fix or a valid example; an unknown service name lists the
  services the project actually defines; `snapshot not found` points at `opossum ws
  ls`; and failing to create a bind mount's host directory now warns (`[OPSM-104]`)
  with the fix instead of failing silently later. The guiding rule — every error
  says what happened, why, and what to do next.
- The `examples/agent-sandbox` **caged** variant now shows a working MCP tool: an
  agent whose internet is fenced to an allowlist can still use one host tool. The
  tool is declared as an explicit URL through the host gateway (an internal network
  has no name resolution) and `NO_PROXY` keeps that traffic off the egress proxy —
  so the caged agent reaches only the allowlisted internet plus the one declared
  tool, and nothing else.

## [0.11.0] - 2026-07-21

### Added

- The `examples/agent-sandbox` "caged" variant now ships a **self-contained egress
  allowlist**: a small forward-proxy service and a `proxy/allowlist` file declare,
  in the compose file itself, exactly which hosts a sandboxed agent may reach
  (default-deny). The agent runs on a host-only network with no internet of its
  own, so the proxy is its only way out and the allowlist is enforced rather than
  advised — no host-side proxy to set up. See the example's README.

### Changed

- `opossum ps` and `opossum images` now fail with a clear error (`[OPSM-405]`) and a
  non-zero exit when Apple's `container` system is installed but **not running**,
  instead of printing an empty table / `PRESENT=no` that looks like "nothing is
  here". Start the system with `container system start` (or run `opossum doctor`).
- When Apple's `container` CLI isn't installed, **every** runtime command now
  fails the same way — a clear, actionable error (`[OPSM-404]` with the
  `brew install container` / `container system start` steps) and a non-zero exit —
  instead of some commands quietly succeeding with misleading output. Previously
  `ps` printed an empty table (looking like "nothing is running") and `images`
  reported `PRESENT=no`, both exiting 0. `config` still works without the CLI
  since it only parses your compose files.

## [0.10.0] - 2026-07-17

### Added

- opossum's warnings and recovery-relevant errors now carry a stable
  `[OPSM-NNN]` code (e.g. `[OPSM-101]` for the Postgres data-dir volume trap,
  `[OPSM-204]` for a Docker-socket mount). The codes are add-only and map 1:1 to
  `AGENTS.md`'s failure-signature / diagnostic-codes tables, so an agent (or a
  human) can jump straight to the fix. Message wording is unchanged apart from
  the prefix.
- `opossum doctor --format json` prints the environment checks as
  machine-readable JSON — a top-level `{healthy, checks[]}` where each check has
  `{id, status, detail, fix}` (status is `ok`/`warn`/`fail`) — so scripts and
  agents can decide from one call. The default output stays human-readable, and
  a failed check still exits non-zero in both formats.
- `AGENTS.md` — a high-density, facts-only reference for driving opossum from an
  AI agent: the command surface (with exit-code behavior), the
  supported/ignored/rejected compose fields, a failure-signature→fix table, and
  the sandboxing/egress vocabulary. README points agents at it. A test keeps it
  in sync with the actual CLI (every command must be documented).
- `opossum up --dry-run` **prints the plan without executing anything**: the
  service startup order, the recreate/skip decisions, and the exact `container`
  commands it would issue — but it creates, starts, and deletes nothing. It fills
  the gap between `config` (resolves the configuration only) and `--verbose`
  (shows commands while running them), so you can validate what `up` will do
  before acting. (A `--format json` variant may follow.)
- `up` now **warns when a service mounts the Docker socket** (`docker.sock`).
  Apple `container` has no Docker daemon socket, so the mount fails at runtime
  with an opaque error — the warning explains the real reason up front (tools
  that drive Docker over its socket, e.g. Portainer, can't work here).

### Changed

- When a dependency fails to become healthy because its container has exited,
  `up` now **embeds that container's last log lines in the error** — so you see
  the real cause (e.g. a Postgres `initdb` failure) immediately. It no longer
  points you at `opossum logs`, which wouldn't work: a failed `up` rolls back and
  removes the container.

## [0.9.0] - 2026-07-16

### Added

- `opossum stats --host` reports each service's **host** memory footprint — the
  resident size of its VM on your Mac — alongside the guest-view usage. Because
  Apple `container` runs each container in its own VM, this per-service cost to
  the host is a real number a shared-VM tool (Docker Desktop, Colima, OrbStack)
  can't break down per service. It's host-derived and approximate; a service
  whose VM can't be mapped shows `—` rather than failing.
- A service can now join **multiple declared networks** (`networks: [a, b]`) —
  each becomes a `container run --network`, in declaration order. Previously a
  service was limited to one network. (Per-network aliases are still not applied.)
- `examples/agent-sandbox`: run Claude Code fully autonomously inside an Apple
  `container` VM, where the compose file **is** the agent's permission boundary —
  a `./work` bind mount (the files it sees), a `.env` token (the secret it holds),
  `networks:` (how far it reaches, including a host-only `internal:` "caged"
  variant that forces egress through a host proxy), and `mem_limit`/`cpus`. Driven
  as a one-off with `opossum run --rm agent`. Includes a README covering the two
  bring-your-own auth options and what the VM does and doesn't protect.
- The **long mapping form of `ports:`** (`{target, published, protocol,
  host_ip}`) is now accepted, alongside the short string form. Real-world compose
  files that use it previously failed to load (`cannot unmarshal !!map into
  string`); they now normalize to the same `host:container` spec.

### Fixed

- An unsupported `network_mode` (e.g. `host`, which several real-world compose
  files use) no longer fails the whole file at load. It's ignored — the service
  joins the project network — and listed among the ignored fields, so a
  `docker-compose.yml` loads without surprises. Only `network_mode: none` is
  acted on.

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

[Unreleased]: https://github.com/suruseas/opossum/compare/v0.20.0...HEAD
[0.20.0]: https://github.com/suruseas/opossum/compare/v0.19.1...v0.20.0
[0.19.1]: https://github.com/suruseas/opossum/compare/v0.19.0...v0.19.1
[0.19.0]: https://github.com/suruseas/opossum/compare/v0.18.2...v0.19.0
[0.18.2]: https://github.com/suruseas/opossum/compare/v0.18.1...v0.18.2
[0.18.1]: https://github.com/suruseas/opossum/compare/v0.18.0...v0.18.1
[0.18.0]: https://github.com/suruseas/opossum/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/suruseas/opossum/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/suruseas/opossum/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/suruseas/opossum/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/suruseas/opossum/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/suruseas/opossum/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/suruseas/opossum/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/suruseas/opossum/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/suruseas/opossum/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/suruseas/opossum/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/suruseas/opossum/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/suruseas/opossum/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/suruseas/opossum/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/suruseas/opossum/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/suruseas/opossum/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/suruseas/opossum/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/suruseas/opossum/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/suruseas/opossum/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/suruseas/opossum/releases/tag/v0.1.0
