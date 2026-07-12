# Benchmarks: Apple `container` vs Docker Desktop

Why run a compose stack on Apple's `container` (via opossum) instead of Docker
Desktop? The honest answer is **footprint and isolation, not raw start speed.**
These are indicative numbers measured on one machine — the commands are here so
you can re-measure on yours.

## Environment

- macOS 26, Apple silicon
- Apple `container` 1.0.0 (via opossum)
- Docker Desktop, Docker Engine 29.5.3 (for comparison)
- Image: `alpine:3.20` (pre-pulled in both)

## Results

| Metric | Docker Desktop | Apple `container` |
|--------|----------------|-------------------|
| Single-container start (`run --rm alpine true`, median of 7) | **0.19 s** | 0.81 s |
| Idle host-side daemon memory (RSS) | ~373 MB of `com.docker.*` host processes | **~58 MB** of `container-*` helpers |
| Always-on Linux VM | **~7.8 GB** guest RAM provisioned (`docker info` `MemTotal`), running whenever Docker Desktop is up | **none** — a lightweight VM is started per container, on demand |
| Added memory per running container | shares the one VM | **~250–400 MB** (its own micro-VM; ~270 MB typical for nginx:alpine, plus ~20 MB of helpers) |
| Isolation boundary | shared VM kernel | **per-container VM** |
| License | Docker Desktop requires a paid subscription for larger orgs | Apple `container` is open source, no subscription |

### File I/O on bind mounts — *not* improved

A common reason "Docker on Mac feels slow" is bind-mount file I/O: the host
directory is shared into the Linux VM over a virtualized filesystem, so
metadata-heavy work (many small files — `node_modules`, source trees, DB data)
pays a big penalty. **Apple `container` uses the same host↔VM sharing model, so
this is not fixed** — and by these numbers its bind-mount I/O is a bit *slower*
than Docker's VirtioFS.

Creating 20,000 small files (`echo x > f$i`), wall time:

| Storage | Docker Desktop | Apple `container` |
|---------|----------------|-------------------|
| Bind-mounted host dir | ~4.0 s | ~6.6 s |
| In-VM (container fs / named volume) | ~0.8 s | ~2.6 s |

Sequential large writes (`dd` 256 MB) are fine on both (VirtioFS-class
throughput); the penalty is specifically small-file / metadata operations on
**bind mounts**.

**Mitigation (same as Docker):** keep hot I/O paths — DB data directories, build
caches, `node_modules` — in a **named volume** (in-VM storage), not a bind mount.
opossum namespaces named volumes per project, so this is a drop-in change. Bind
mounts are best kept for source you edit from the host.

### How to read this

- **Docker starts a single container faster** (~0.2 s vs ~0.8 s): its Linux VM is
  already running, so `docker run` just launches a process inside it. Apple
  `container` boots a fresh lightweight VM per container, which costs ~0.6 s more
  — that is the price of per-container VM isolation.
- **Apple `container` is dramatically lighter at rest — but each running
  container is a whole VM.** Docker Desktop keeps a multi-gigabyte Linux VM
  resident the whole time it is running; Apple `container` has only ~58 MB of
  helper processes at idle and allocates memory **only while containers
  actually run** — but at **~250–400 MB per container** (a full guest kernel;
  the floor doesn't drop with `-m`). On a laptop that idles most of the day
  running a container or two, that is still a big win; once several containers
  run at once, Docker's shared pool is lighter — see
  [vs-docker-desktop.md](vs-docker-desktop.md) for the measured scaling table
  and the crossover (~2–3 containers).

**When Apple `container` (+ opossum) wins:** you want a compose-style workflow
without a heavy always-on VM, you value per-container VM isolation, or you'd
rather not depend on Docker Desktop's licensing. **When Docker wins:** you churn
many short-lived containers and per-container start latency dominates.

## Reproduce

```sh
# Single-container start (run several, take the median)
for i in $(seq 7); do /usr/bin/time -p docker    run --rm alpine:3.20 true; done   # Docker
for i in $(seq 7); do /usr/bin/time -p container run --rm alpine:3.20 true; done    # Apple container

# Idle host-side daemon memory (RSS, MB)
ps -Ao rss,comm | grep -iE "com.docker|Docker.app" | awk '{s+=$1} END{print int(s/1024)"MB"}'
ps -Ao rss,comm | grep -iE "container-apiserver|container-network|machine-apiserver|container-core|container-runtime" \
  | awk '{s+=$1} END{print int(s/1024)"MB"}'

# Per-container memory: each running container adds a
# com.apple.Virtualization.VirtualMachine process; its guest memory is fully
# attributed to that process (verified), so read its physical footprint:
pgrep -f com.apple.Virtualization.VirtualMachine          # diff before/after a run
vmmap --summary <pid> | grep "Physical footprint"         # ~270 MB per idle nginx VM
# (The helper-only grep above misses these VM processes — summing helpers alone
# understates per-container cost by >10x.)

# Docker's always-on Linux VM RAM
docker info --format '{{.MemTotal}}'   # bytes of guest RAM provisioned
```

Numbers move with hardware, image cache, and Docker Desktop's memory settings;
re-run before quoting them. The **shape** (Docker faster per start, Apple
`container` far lighter at rest) is what matters.
