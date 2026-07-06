# opossum examples

Two bundled stacks, and a walkthrough of every subcommand.

| File | Needs a build? | Demonstrates |
|------|----------------|--------------|
| [`hello.yaml`](hello.yaml) | no (image only) | `depends_on` ordering, bare-name service discovery, `entrypoint` + `command` |
| [`compose.yaml`](compose.yaml) | yes (`web`) | `build`, `ports`, `environment`, `healthcheck`, `depends_on` conditions (`service_healthy`, `service_completed_successfully`), `.env` / `${VAR}` interpolation, string `command` |
| [`app-stack/compose.yaml`](app-stack/compose.yaml) | no (all pre-built) | a realistic, browsable stack: Postgres + Redis + Adminer UI + a worker — health-gated startup, a published port, and a **persistent named volume** done right (`PGDATA` subdirectory) |

`hello.yaml` runs anywhere `container` is up. `compose.yaml` additionally needs the
image builder (`container builder start`) for its `web` service.

## Setup (once)

```sh
sudo container system dns create opossum   # local DNS domain for name resolution
```

## Walkthrough (hello.yaml — build-free)

```sh
cd examples

opossum -f hello.yaml up            # start db then web (dependency order)
opossum -f hello.yaml ps            # SERVICE / CONTAINER / IMAGE / IP / PORTS / STATUS
opossum -f hello.yaml logs web      # web's logs (add --follow to stream, -n N to tail)

# web resolves db by its bare service name over the shared network:
container exec web.hello.opossum nslookup db

opossum -f hello.yaml up web        # (re)start only web and its dependencies
opossum -f hello.yaml stop          # stop containers, keep them
opossum -f hello.yaml restart       # stop + start in place
opossum -f hello.yaml down          # stop, remove, and delete the network
```

Container names are `<service>.<project>.opossum` (here `web.hello.opossum`), so
the `container` CLI can address them directly.

## Full-feature stack (compose.yaml)

```sh
container builder start                     # required for the web build
opossum up                                  # reads compose.yaml + .env
opossum ps
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

opossum -f examples/app-stack/compose.yaml down -v    # stop, remove, and drop the named volume
```

The `db` service keeps its data in a **named volume** and points `PGDATA` at a
subdirectory — Apple's `container` mounts a volume as a non-empty mount point,
which Postgres `initdb` rejects, so this is the pattern to follow (opossum warns
if you forget). `open http://localhost:8080` and logging in with server `db`
shows bare-name discovery working from a real UI.

## Running two projects at once

Projects are isolated automatically — same service names don't collide, because
each container is namespaced as `<service>.<project>.opossum` and each project
gets its own `<project>-net` network:

```sh
opossum -f hello.yaml -p a up       # db.a.opossum, web.a.opossum
opossum -f hello.yaml -p b up       # db.b.opossum, web.b.opossum — runs concurrently
```
