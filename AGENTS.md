# opossum for AI agents

High-density reference for driving **opossum** — a Docker Compose–like orchestrator
that runs `compose.yaml` on Apple's `container` runtime (macOS 26, Apple silicon).
It shells out to the `container` CLI; it is not the Docker engine. This file is
facts only; the human-facing narrative is in `README.md`.

## Mental model

- One **VM per container** (kernel-isolated), not one shared VM. Idle cost is near
  zero (no daemon); each running container costs ~250–400 MB of host RAM.
- **The compose file is a capability declaration**: `volumes:` = files the service
  sees, `environment:`/`secrets:` = secrets it holds, `networks:` = how far it
  reaches, `${OPOSSUM_HOST_GATEWAY}` = how it reaches the host.
- opossum runs a **subset** of the compose schema. Unsupported fields are ignored
  with a warning (not a hard error) so a `docker-compose.yml` loads without
  surprises. `opossum config` prints the resolved project + the ignored fields.

## Quickstart

```sh
container system start                       # start the runtime (once per boot)
sudo container system dns create opossum     # ONLY needed for cross-service bare-name
                                             # resolution (svc→svc by name); skippable
                                             # otherwise (needs sudo — may prompt)
opossum up                                   # reads ./compose.yaml (+ override), starts in dep order
opossum ps                                   # SERVICE / CONTAINER / IP / PORTS / STATUS
opossum logs -f web                          # stream a service's logs
opossum down                                 # stop + remove + drop the network (-v also drops volumes)
```

`-f <file>` selects compose files (repeatable, later wins); `-p <name>` sets the
project name; `--verbose` echoes each `container` command; `--dns-domain` overrides
the discovery domain (default `opossum`).

## Commands

One line each. `[service…]` means optional service names (default: all). Exit code
is **0 on success, non-zero on any error** (see Exit codes).

| Command | Does |
|---------|------|
| `up [service…]` | build (if missing) + start in dependency order. Leaves an unchanged running service alone, but **recreates a stopped or config-changed one** — so after a failed `up` you can just fix the compose and re-run `up` (no `down` first; re-running over a partial/failed bring-up is safe). Flags: `--build`, `--no-build`, `--force-recreate`, `--from-docker`, `--remove-orphans`, `--foreground`, `--profile <p>` |
| `down` | stop + remove containers + delete the project network. `-v` also removes named volumes; `--rmi local\|all` removes images; `--remove-orphans` |
| `ps` | table of service / container / IP / ports / status. STATUS is `running`/`stopped`/`absent` — it does **not** show healthcheck state; to confirm a service is *healthy*, check its `logs` (a `service_healthy` dependency gates on health automatically during `up`) |
| `logs [service…]` | print logs; `--follow`/`-f` streams (multiplexed, name-prefixed), `-n/--tail N` |
| `stats [service…]` | live CPU/mem/net/IO (streams); `--no-stream` for one snapshot; `--host` shows each service's host-memory footprint (its VM's resident size — a shared-VM tool can't do this per service) |
| `exec [-it] <service> <cmd…>` | run a command in a running container |
| `run [--rm] [--no-deps] [-T] <service> [cmd]` | one-off foreground container; starts deps unless `--no-deps`; `-T` disables the TTY (keeps piped stdout clean, e.g. an MCP stdio server); no published ports |
| `build [service…]` | build images for services with a `build:` |
| `pull [service…]` | pull images for services with an `image:` |
| `import [service…]` | copy a service's Docker-built image into `container`'s store (skip Apple's builder) |
| `cp <src> <dst>` | copy files host↔container; each path is a host path or `service:path` |
| `start [service…]` / `stop [service…]` / `restart [service…]` | start / stop / stop-then-start existing containers |
| `kill [service…]` | send a signal (default KILL); `-s/--signal <SIG>` |
| `watch` | sync host file changes into containers per `develop.watch`; runs until Ctrl-C (start with `up` first) |
| `images` | each service's image, whether opossum builds it, whether it's present |
| `config [--services]` | validate and print the resolved compose (interpolation + env_file applied), listing ignored fields |
| `doctor` | diagnose the environment (runtime, DNS domain, outbound network, builder memory, stack-memory estimate); non-zero exit if any check fails |

## Compose dialect: supported / ignored / rejected

**Supported (acted on):** `image`, `build` (`{context, dockerfile, args, target}`),
`platform` (`linux/amd64` adds `--rosetta`), `ports` (short `"8080:80"`/`"3000"` and
long `{target, published, protocol, host_ip}`), `environment`, `env_file`,
`volumes` (bind, named, `type: tmpfs`, short+long form), `tmpfs`, `secrets`
(file-based only, mounted at `/run/secrets/<name>`), `depends_on` (+ `condition:
service_healthy`/`service_completed_successfully`), `healthcheck` (CMD/CMD-SHELL/
string, `interval`/`timeout`/`retries`/`start_period`), `command`, `entrypoint`,
`profiles`, `mem_limit`/`cpus` (and `deploy.resources.limits.{memory,cpus}`), `ssh`
(forwards the host SSH agent), `develop.watch`, `user`, `working_dir`, `init`,
`read_only`, `cap_add`/`cap_drop`, `networks` (top-level + per-service, incl.
`internal: true` and `external: true`), `${VAR}` interpolation (`${VAR:-default}`,
`${VAR:?required}`, `$$`). YAML anchors + merge keys (`<<: *anchor`) resolve.

**Ignored with a warning (file still loads):** `restart`, `container_name`,
`network_mode` values other than `none` (e.g. `host` → the service joins the
project network), per-network `aliases`, `ipam`/static IPs under `networks`,
`deploy` beyond `resources.limits`, and other unrecognized fields. `opossum config`
lists them.

**Rejected (hard load error):** `external: true` secrets; a `secrets` entry with no
`file:`; a service with neither `image` nor `build`; `network_mode: none` combined
with `networks:`; a top-level network that is both `internal` and `external`; a
service referencing an undeclared network; `depends_on` on an unknown service;
`service_healthy` on a service with no healthcheck.

## Failure signatures → fix

opossum turns opaque runtime failures into actionable warnings and errors, each
stamped with a stable `[OPSM-NNN]` code. Match the code (or the signature) and
apply the fix — no need to re-read the prose. See "Diagnostic codes" for the full
list; codes are add-only and never change meaning.

- **`[OPSM-101]` … `a named volume mounted at /var/lib/postgresql/data makes
  Postgres initdb fail`** → the DB's data dir is a mount point (has `lost+found`);
  add `environment: PGDATA=/var/lib/postgresql/data/pgdata` and re-run `up`.
- **`[OPSM-204]` … `mounts the Docker socket … Apple container has no Docker daemon
  socket`** → the service needs Docker (e.g. Portainer); it can't work here. Remove
  the `docker.sock` mount or run that tool differently.
- **`[OPSM-201]` … `host port already in use: <port>`** (pre-flight) → free the host
  port or remap it in the compose file. On macOS, port 53 is taken by mDNSResponder.
- **`[OPSM-401]` … `container is not running (state "stopped"); its last log
  lines:`** → the dependency crashed at startup; the embedded logs show why (e.g.
  the Postgres `initdb` message above). Fix the dependency, not the dependent.
- **`[OPSM-404]` … `the container CLI was not found on PATH`** → Apple's `container`
  isn't installed. Every runtime command (`up`, `ps`, `images`, `logs`, `stats`, …)
  fails this way with a non-zero exit — an empty `ps` table would be a lie. Install
  it (`brew install container`, or the `.pkg` from the releases page), then
  `container system start`. `config` still works without it (it only parses compose).
- **`[OPSM-405]` … `the container system isn't running`** → the CLI is installed but
  the daemon is stopped. `ps`/`images` fail with a non-zero exit instead of printing
  an empty table / `PRESENT=no` (which would look like "nothing is here"). Run
  `container system start` (or `opossum doctor` to check the whole environment).
- **`[OPSM-102]` … `services <a,b> share named volume "<v>"`** → Apple `container`
  attaches a named volume to only one running container; use a bind mount for
  shared data, or don't run both at once.
- **`[OPSM-202]` … `DNS domain "opossum" not found`** → run `sudo container system
  dns create opossum` once, then `up` again (needed for bare-name discovery).
- **`[OPSM-203]` … `network <n> is internal (host-only): … no internet egress`** →
  expected for an `internal:` network; reach out only through a host proxy at
  `${OPOSSUM_HOST_GATEWAY}`, and address peers by IP (no name resolution).
- **`[OPSM-301]` … `context … under /private/tmp … builder can't read`** → build
  from a path under your home directory (the builder VM doesn't mount `/private/tmp`).
- **`unsupported network_mode "host"`** does NOT occur — such values are ignored, not
  rejected (the file loads); reported as `[OPSM-502]`.
- **connected but a tool call / outbound request fails** with the runtime days-old →
  the default network wedged (no code — it's a runtime state). Test `container run
  --rm alpine ping -c1 1.1.1.1`; if it fails, `container system stop && container
  system start`. `opossum doctor` checks this.
- **build hangs / `Unavailable`/`EOF` on a heavy image** → the shared builder VM (no
  code — a runtime resource issue) is starved (default 2 CPU / 2 GB). `container
  builder start --cpus 4 --memory 8g`, and shrink the context with `.dockerignore`.

### Diagnostic codes

Every `[OPSM-NNN]` opossum can emit (add-only; grouped 1xx storage / 2xx network /
3xx build / 4xx lifecycle / 5xx compose):

- `OPSM-101` — named volume mounted directly at Postgres's data dir (initdb fails).
- `OPSM-102` — a named volume shared by two running services (exclusive attach).
- `OPSM-201` — a published host port is already taken (pre-flight).
- `OPSM-202` — the DNS domain isn't registered (no bare-name discovery).
- `OPSM-203` — an internal network: no internet egress and no name resolution.
- `OPSM-204` — a service mounts `docker.sock` (Apple container has no Docker socket).
- `OPSM-301` — build context under `/private/tmp` (the builder VM can't read it).
- `OPSM-302` — build context is a symlink (the builder may reject it).
- `OPSM-401` — a dependency's container exited before becoming healthy (logs embedded).
- `OPSM-402` — orphan containers left by services no longer in the compose.
- `OPSM-403` — a `service_healthy` dependency defines no healthcheck (not waited on).
- `OPSM-404` — the `container` CLI isn't installed / not on PATH (every runtime command fails).
- `OPSM-405` — the `container` system (daemon) is installed but not running (`ps`/`images` fail loudly).
- `OPSM-501` — unsupported top-level compose field(s), ignored.
- `OPSM-502` — unsupported service compose field(s), ignored (e.g. `network_mode: host`).
- `OPSM-601` — a `watch` rebuild action failed.
- `OPSM-602` — a `watch` restart action failed.
- `OPSM-603` — a `watch` file sync failed.
- `OPSM-604` — `watch` couldn't start watching a path.
- `OPSM-605` — the `watch` file watcher reported an error.

## Sandboxing / egress (capability vocabulary)

- `network_mode: none` → `--network none`: full isolation, loopback only, no egress.
- top-level `networks: { caged: { internal: true } }` + `networks: [caged]` on a
  service → host-only network: no internet, host still reachable. Force egress through
  a host allowlist proxy the service reaches at `${OPOSSUM_HOST_GATEWAY}` — the
  allowlist is then enforced, not advisory. Peers on an internal network can't resolve
  each other by name; use IPs.
- `${OPOSSUM_HOST_GATEWAY}` → the host's LAN IP (bind host services on `0.0.0.0`).
- Pair with `cap_drop: [ALL]` + a non-root `user:` to keep a workload from
  reconfiguring its own networking. See `examples/agent-sandbox` for running an
  autonomous agent this way.

## Exit codes

- `0` — success.
- non-zero — any failure: a runtime error, a load/validation error (bad compose), an
  unknown service, a health-gate failure, the `container` CLI being absent
  (`[OPSM-404]`) or its system stopped (`[OPSM-405]`, `ps`/`images`), or `doctor`
  finding an unhealthy check.
  There are no granular per-cause codes today; read stderr for the message.
