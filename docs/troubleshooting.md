# Troubleshooting

What goes wrong on Apple `container` that isn't opossum's doing, and what to do
about it — plus the limits worth knowing before you hit them.

## If a service doesn't come up

(see [Differences from docker compose](compatibility.md#where-it-differs-from-docker-compose)):

1. **DNS domain not registered** → services can't resolve each other by name. Run the setup line above.
2. **Postgres on a volume opossum didn't create** → `initdb` refuses it, naming `lost+found`. Volumes opossum creates are cleared of it, so recreating the volume (`opossum down -v`, then `up`) is the fix; `PGDATA=/var/lib/postgresql/data/pgdata` still works if you'd rather keep the volume.
3. **Host port already in use** → `up` names the port and service; on macOS a taken 5000/7000 is often the **AirPlay Receiver** (turn it off in System Settings › General › AirDrop & Handoff, or remap the host port).
4. **Building from a temp/scratch dir** → Apple's builder can't read a context under `/private/tmp` or a symlink. Build from a real path under your home directory (or use `--from-docker-compose`).

## Troubleshooting builds

Builds run in Apple's shared `container` builder VM, which starts with modest
resources (2 CPUs / 2 GB). Since each running service is its own VM too, a heavy
build can starve.

- **A build is very slow, runs out of memory, or fails with `Unavailable` /
  `EOF`** (e.g. a large multi-stage image, or a big `apt-get install`): give the
  builder more resources. It's a shared VM, so this is a one-time setup, not
  per-project.
  ```sh
  container builder delete --force
  container builder start --cpus 4 --memory 8g
  opossum up
  ```
  Also make sure the host has RAM to spare — stopping other heavy services while
  the first build runs helps, since every service is a separate VM.
- **A build hangs or fails with `unable to read root manifest` /
  `failed to load cache key`** (often after interrupting a build with Ctrl-C):
  the builder cache is in a bad state. Reset it and retry:
  ```sh
  container builder delete --force
  opossum up
  ```
- **A build fails with `no space left on device`**: the host volume is out of
  disk. A real build pulls multi-GB base images and writes build layers onto the
  host, so this is common when disk is tight. Free space and retry — don't grow
  the builder, which only uses more disk:
  ```sh
  container image prune -f          # remove unused images
  container builder delete --force  # clear the builder's cache (recreated automatically)
  df -h /                           # confirm there's room, then: opossum up
  ```
- **`transferring context` is slow**: your build context is large. Add a
  `.dockerignore` next to the Dockerfile that excludes things the image doesn't
  need — `.git`, `node_modules`, `tmp`, `log`, `vendor/bundle`, build artifacts —
  so less data is sent to the builder.

## Known limitations

- **Named volumes are mount points, so a database's data directory can't sit
  directly on one.** opossum passes named volumes through and the runtime
  auto-creates them, but `container` mounts a volume as a filesystem mount point
  containing `lost+found`. Postgres/MySQL `initdb` refuses a non-empty data
  directory, so `-v pgdata:/var/lib/postgresql/data` fails. Point the database at
  a **subdirectory** of the mount instead — e.g. for Postgres set
  `environment: { PGDATA: /var/lib/postgresql/data/pgdata }`. Only bind-mount host
  paths are resolved to absolute paths. Named volumes are namespaced per project
  (`<project>_<volume>`), so concurrent projects don't share one — except a
  volume declared `external: true` in the top-level `volumes:` block, which is
  used by its real name (its declared `name:`, or the key) and never removed by
  `down -v` — the user manages it. `external` takes the bool form; the volume
  must already exist (opossum doesn't create it). Other top-level volume settings
  (`driver`, `labels`, …) are not applied.
- **A named volume can't be shared by two running containers.** `container`
  attaches a named volume as an exclusive block device, so if two services mount
  the same named volume, the first to start gets it and the others fail with `The
  storage device attachment is invalid`. (Docker shares named volumes; a common
  case is an app + nginx sharing a `public`/assets volume.) `up` **warns** when it
  sees this — use a **bind mount** (a host path, which *is* shareable) for the
  shared data, or bake it into the image.
- **`networks:` — aliases and static IPs (`ipam`) aren't applied**, and an
  `internal:` network has no name resolution (peers must use IPs). Multiple networks
  per service and `external:` reuse both work. See [Networking
  model](networking.md) for the full picture.
- **`restart:` is honoured by a small per-project supervisor**, not by a resident
  engine: `up` leaves one running when the project declares a policy, and `down`
  stops it. `on-failure` can only be approximated — Apple `container` doesn't
  report a container's exit code, so opossum retries a bounded number of times
  rather than looping a service that may have finished on purpose. Note a restart
  reassigns the container's IP; its name and config are preserved, so name-based
  discovery is unaffected.
- **No Docker-in-Docker / nested containers inside a service.** A service runs in
  a `container run` VM with no nested virtualization (no `/dev/kvm`), so it can't
  run its own containers — a build/test job that shells out to `docker` won't
  work inside a service. Apple `container` *can* do nested virtualization, but
  only through a separate `container machine --virtualization` VM, which needs
  Apple silicon **M3 or newer** (with macOS 15+). opossum doesn't yet drive
  container machines, so there's no supported nested-container path today. This is
  a natural area to extend — **contributions welcome** (see the tracking issue for
  agent/sandbox use cases).

[← back to the README](../README.md)
