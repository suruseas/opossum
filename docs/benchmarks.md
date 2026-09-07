# Benchmarks: Apple `container` vs Docker Desktop

Why run a compose stack on Apple's `container` (via opossum) instead of Docker
Desktop? The honest answer is **footprint and isolation, not raw start speed.**
These are indicative numbers measured on one machine — the commands are here so
you can re-measure on yours.

## Environment

- macOS 26.6.2, Apple silicon (Mac14,2)
- Apple `container` 1.3.1 (via opossum) — measured 2026-09-07; the first
  edition of this page was measured on `container` 1.0.0, and where a number
  moved the old one is kept in parentheses
- Docker Desktop, Docker Engine 29.7.2 (for comparison; 29.5.3 in the first edition)
- Image: `alpine:3.20` (pre-pulled in both); `nginx:alpine` for the per-container figure
- Raw output of every command below is kept with the measurement, per run

## Results

| Metric | Docker Desktop | Apple `container` |
|--------|----------------|-------------------|
| Single-container start (`run --rm alpine true`, median of 7) | **0.15 s** (1.0.0 edition: 0.19 s) | 0.83 s (0.81 s) |
| Idle host-side daemon memory (RSS) | ~551 MB of `com.docker.*` host processes (373 MB) | **~81 MB** of `container-*` helpers, 4 processes (58 MB) |
| Always-on Linux VM | **~8.2 GB** guest RAM provisioned (`docker info` `MemTotal` = 8,215,375,872 bytes), running whenever Docker Desktop is up | **none at rest** — a lightweight VM is started per container, on demand. One exception: the **builder VM** (`container builder`) starts on the first `build` and stays resident (~520 MB physical footprint measured) until `container builder stop` |
| Added memory per running container | shares the one VM | **~250–400 MB** (its own micro-VM; 273 MB measured for an idle nginx:alpine on 1.3.1, plus ~20 MB of helpers) |
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
| Bind-mounted host dir | ~4.6 s (1.0.0 edition: ~4.0 s) | ~6.4 s (~6.6 s) |
| In-VM (container fs / named volume) | ~0.8 s (~0.8 s) | ~2.4 s (~2.6 s) |

Wall time of the whole `run --rm` (container start included, so ~0.15 s of
Docker's and ~0.8 s of Apple `container`'s figure is the start itself).

Sequential large writes (`dd` 256 MB) are fine on both (VirtioFS-class
throughput); the penalty is specifically small-file / metadata operations on
**bind mounts**.

**Mitigation (same as Docker):** keep hot I/O paths — DB data directories, build
caches, `node_modules` — in a **named volume** (in-VM storage), not a bind mount.
opossum namespaces named volumes per project, so this is a drop-in change. Bind
mounts are best kept for source you edit from the host.

### How to read this

- **Docker starts a single container faster** (~0.15 s vs ~0.8 s): its Linux VM is
  already running, so `docker run` just launches a process inside it. Apple
  `container` boots a fresh lightweight VM per container, which costs ~0.7 s more
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

# "At rest" means: no container running, no builder VM, only the default
# network. The builder VM stays resident after the first build, and every
# extra network keeps a vmnet helper alive — check before reading the idle figure:
container ls -a; container network ls; pgrep -fl com.apple.Virtualization.VirtualMachine
container builder stop                                    # if a build left it running
```

Numbers move with hardware, image cache, and Docker Desktop's memory settings;
re-run before quoting them. The **shape** (Docker faster per start, Apple
`container` far lighter at rest) is what matters.

## What is this service actually costing my Mac? (`stats --host`)

Plain `opossum stats` shows the **guest** view: how much of its RAM limit each
container uses inside its VM. What you often really want on a Mac is the **host**
view: how much memory this service is taking from your machine. Because Apple
`container` gives every container its own VM, that's a real, separable number —
and `opossum stats --host` reports it per service:

```
SERVICE  GUEST MEM      HOST FOOTPRINT
web      1.9MiB / 1GiB  330.4MiB
db       1.9MiB / 1GiB  330.7MiB
         total          661.2MiB
```

A shared-VM tool (Docker Desktop, Colima, OrbStack) structurally can't break this
down per service — all containers live in one VM. The figure is the resident size
of the service's VM process (what Activity Monitor shows for "Virtual Machine
Service…"), read from the host; it's **approximate and host-derived**, and a
service whose VM can't be mapped shows `—` rather than failing. (`opossum doctor`
gives a rougher, introspection-free estimate.)

[← back to the README](../README.md)
