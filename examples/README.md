# opossum examples

Two bundled stacks, and a walkthrough of every subcommand.

| File | Needs a build? | Demonstrates |
|------|----------------|--------------|
| [`hello.yaml`](hello.yaml) | no (image only) | `depends_on` ordering, bare-name service discovery, `entrypoint` + `command` |
| [`compose.yaml`](compose.yaml) | yes (`web`) | `build`, `ports`, `environment`, `healthcheck`, `depends_on` conditions (`service_healthy`, `service_completed_successfully`), `.env` / `${VAR}` interpolation, string `command`, a `develop.watch` rule for `opossum watch` |
| [`app-stack/compose.yaml`](app-stack/compose.yaml) | no (all pre-built) | a realistic, browsable stack: Postgres + Redis + Adminer UI + a worker — health-gated startup, a published port, and a **persistent named volume** done right (`PGDATA` subdirectory) |
| [`local-ai-stack/compose.yaml`](local-ai-stack/compose.yaml) | no (all pre-built) | LLM on the **host**, stack in containers: reaching a host service via the built-in `${OPOSSUM_HOST_GATEWAY}` alongside container-to-container discovery |
| [`mcp-stack/compose.yaml`](mcp-stack/compose.yaml) | no (all pre-built) | host MCP servers on Apple container instead of an always-on Docker Desktop: an HTTP (streamable) server via `up` + published port, and token-bearing / plain stdio servers via `opossum run --rm` (secrets in `.env`, not `.mcp.json`) |
| [`agent-sandbox/compose.yaml`](agent-sandbox/compose.yaml) | yes (`agent`) | run Claude Code fully autonomously inside a VM: the compose file **is** the agent's permission boundary — `./work` bind mount (files), `.env` token (secret), `networks:` (egress, incl. a host-only `internal:` "caged" variant), `mem_limit`/`cpus` (resources). One-off `opossum run --rm agent` |

`hello.yaml` runs anywhere `container` is up. `compose.yaml` additionally needs the
image builder (`container builder start`) for its `web` service.

## Setup (once)

```sh
sudo container system dns create opossum   # local DNS domain for name resolution
```

## Walkthrough (hello.yaml — build-free)

```sh
cd examples

opossum -f hello.yaml config --services   # db, web — the file parsed, nothing started
opossum -f hello.yaml config        # the resolved file (interpolation applied), then the ignored fields

opossum -f hello.yaml up            # start db then web (dependency order)
opossum -f hello.yaml ps            # SERVICE / CONTAINER / IMAGE / IP / PORTS / STATUS
opossum -f hello.yaml images        # SERVICE / IMAGE / SOURCE (pulled or built) / PRESENT
opossum -f hello.yaml logs web      # web's logs (add --follow to stream, -n N to tail)

# web resolves db by its bare service name over the shared network:
opossum -f hello.yaml exec web nslookup db      # run a command in the running service
container exec web.hello.opossum nslookup db   # the same, through the container CLI

opossum -f hello.yaml cp web:/etc/hostname ./hostname   # copy out of a service (service:path → host)
opossum -f hello.yaml cp ./note.txt web:/tmp/note        # and into one (host → service:path)

opossum -f hello.yaml up web        # (re)start only web and its dependencies
opossum -f hello.yaml stop          # stop containers, keep them
opossum -f hello.yaml restart       # stop + start in place
opossum -f hello.yaml kill web      # SIGKILL a service (-s TERM to choose; a shell as PID 1 ignores TERM)
opossum -f hello.yaml start web     # start a stopped or killed service again
opossum -f hello.yaml down          # stop, remove, and delete the network

opossum -f hello.yaml destroy --dry-run   # list everything opossum made for the project — containers, network, images —
opossum -f hello.yaml destroy             # and remove it all (asks first; --force for scripts, --keep-images to keep the pulls)
```

Container names are `<service>.<project>.opossum` (here `web.hello.opossum`), so
the `container` CLI can address them directly.

## Full-feature stack (compose.yaml)

```sh
container builder start                     # required for the web build
opossum build                               # build web's image (demo-web:latest) without starting anything
opossum pull                                # pull the other services' images ahead of time
opossum images                              # web is `built`, the rest `pulled`
opossum up                                  # reads compose.yaml + .env (builds web if its image is missing)
opossum ps
opossum port web 8080                       # the host address:port web landed on
POSTGRES_TAG=17 WEB_HOST_PORT=9090 opossum up   # override interpolated vars
opossum down
```

This stack starts `db` and `cache`, waits for both to pass their healthchecks,
runs the one-shot `migrate` to completion, and only then builds and starts `web`.

## Realistic app stack you can click around in (app-stack/compose.yaml)

A build-free stack (Postgres + Redis + Adminer + worker) that starts something
you can actually open in a browser:

```sh
opossum -f examples/app-stack/compose.yaml up   # db + cache become healthy, then adminer + worker start
opossum -f examples/app-stack/compose.yaml ps
opossum -f examples/app-stack/compose.yaml stats     # live CPU / memory per service (Ctrl-C to stop)
opossum -f examples/app-stack/compose.yaml logs worker   # see it resolve db/cache by bare name

open http://localhost:8080                      # Adminer — System: PostgreSQL, Server: db, user/pass/db: demo

opossum -f examples/app-stack/compose.yaml volumes    # the named volume it made: appstack_db_data
opossum -f examples/app-stack/compose.yaml down -v    # stop, remove, and drop the named volume
```

The `db` service keeps its data in a **named volume** and points `PGDATA` at a
subdirectory — Apple's `container` mounts a volume as a non-empty mount point,
which Postgres `initdb` rejects, so this is the pattern to follow (opossum warns
if you forget). `open http://localhost:8080` and logging in with server `db`
shows bare-name discovery working from a real UI.

## When something is off, and opossum's own tools

Four subcommands `docker compose` doesn't have. `doctor` needs no compose file;
the rest use the stacks above.

```sh
opossum doctor                              # runtime, DNS domain, network, builder, storage, leftover networks, memory — each ✅ or ⚠️ with the fix
opossum doctor --format json                # the same, for a script

# watch: sync host edits into the running container per the service's develop.watch rules
cd examples
opossum up                                  # the full-feature stack; web serves ./web/www as /www
opossum watch                               # keeps running; Ctrl-C to stop
#   in another shell: edit web/www/index.html, then `curl localhost:8080` shows the edit — no rebuild

# ws: snapshot a workspace with APFS clones, try something risky, roll back in an instant
cd examples/agent-sandbox
opossum ws snapshot --path ./work before    # near-instant, copy-on-write; lives in ./.opossum-snapshots/ (gitignore it)
opossum ws ls --path ./work                 # NAME / SAVED
#   ... let an agent loose on ./work ...
opossum ws rollback --path ./work before    # restores ./work (the state it replaces is saved as before-rollback-<time>)
opossum ws rm --path ./work before          # drop a snapshot; `ws prune` drops the auto-saved ones

# import: reuse an image Docker built instead of building it again with Apple's builder
cd examples
docker compose build web                    # (Docker Desktop) builds demo-web:latest
opossum import                              # copies it into container's image store; `images` then shows web as `built`
opossum up --from-docker-compose            # or at up: import from Docker instead of building, for a service whose image is missing
```

## Running two projects at once

Projects are isolated automatically — same service names don't collide, because
each container is namespaced as `<service>.<project>.opossum` and each project
gets its own `<project>-net` network:

```sh
opossum -f hello.yaml -p a up       # db.a.opossum, web.a.opossum
opossum -f hello.yaml -p b up       # db.b.opossum, web.b.opossum — runs concurrently
opossum ls                          # a  running(2) / b  running(2)
```
