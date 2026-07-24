# opossum / Apple `container` vs Docker Desktop

An honest, measured comparison for developers weighing a switch from Docker
Desktop. The goal is to be straight about the tradeoffs: some differences favor
opossum, some favor Docker, and a few are gaps we can close.

Measured on an Apple silicon Mac (M2, 8 cores, 16 GB), Docker Desktop 29.5.3 vs
Apple `container` 1.1.0 with opossum. Numbers are indicative of one machine —
reproduce with the commands shown. See also
[benchmarks.md](benchmarks.md) for idle footprint and single-run startup.

## Where opossum is ahead

- **Idle memory is 10–40× lighter.** With nothing running, Docker Desktop
  holds ~0.3–0.7 GB of host processes **plus its resident VM** (~1.1 GB
  right after boot — under "Virtual Machine Service for Docker"); Apple
  `container`'s background services hold ~50 MB and there's **no always-on
  VM** (it starts a VM per container, on demand). Idle CPU is ~0 % for both.

  ```sh
  ps -Ao rss,comm | grep -i docker      # ~490 MB host, + the VM
  ps -Ao rss,comm | grep -i container   # ~50 MB, no resident VM
  ```
- **No monolithic disk image.** Docker Desktop keeps everything inside one
  `Docker.raw` that grows and is hard to shrink; `container` stores images
  natively on APFS, so reclaiming space is per-image, not "compact a 50 GB file".
- Per-container VM isolation and no Docker Desktop license (see benchmarks.md).

## Memory: idle vs. at scale (the honest tradeoff)

The "10× lighter" idle number is real but it **flips once you run a few
containers at once** — the key difference isn't the total, it's the *allocation
model*: Apple `container` runs **one VM per container** (a fixed default cap of
**1 GiB** each, though it only uses what it needs), while Docker Desktop runs
**one shared VM** whose memory pool all containers draw from.

Measured host memory for N idle `nginx:alpine` containers on a 16 GB M2.

**Measure both VMs the same way — and beware the process name.** Both runtimes
use Apple's Virtualization framework, and macOS attributes each VM's guest
memory to a `com.apple.Virtualization.VirtualMachine` process (Activity Monitor
shows it as "Virtual Machine Service for …"). Attribution is verified in both
directions: writing 300 MB of incompressible data inside a VM grows that
process's physical footprint by exactly that (Apple: 254.1 M → 556.9 M; Docker:
1.1 G → 1.4 G). **A docker-name-only grep misses Docker's VM process
entirely** — an earlier revision of this doc made exactly that mistake and
understated Docker by ~1 GB.

The two runtimes still *behave* differently:

| | Apple `container` (exact, linear) | Docker Desktop (elastic base) |
|---|---|---|
| Nothing running | **~50 MB** (no VM) | helpers ~0.3–0.7 GB + VM ~1.1 GB fresh-boot (total ~**1.4–2.1 GB** warm; compresses down only over long idle) |
| Each added small container | **+~290 MB** (its own VM: ~270 MB + ~20 MB helper) | ~**+0** (shared pool; +6 nginx moved the total by ~10 MB) |
| After a memory-heavy workload | VM freed on container exit | VM **ratchets**: held its +300 MB after the container was removed |

Apple is cleanly linear (~290 MB × N; alpine floor ~235–255 MB/VM; occasional
ballooning to ~400 MB observed). Docker is a big elastic base with near-zero
marginal cost. So:

- **Idle or 1–2 small containers → Apple is clearly lighter.**
- **The crossover depends on Docker's VM state**: against a long-idle,
  paged-down Docker (~0.4 GB visible) it's ~2 containers; against a warm
  Docker (~1.4–2.1 GB) it's ~5–7. **At a typical 5–10 service stack the two
  are comparable** (Apple ~1.5–3 GB vs Docker ~1.4–2.1 GB); past ~10 services
  Docker's shared pool clearly wins.
- **Memory alone no longer decides the dev-stack question — speed does** (see
  the next section).

```sh
# Per-VM cost, SAME method for both runtimes: find the VM process and read its
# physical footprint (guest pages are attributed to it — verified ±300 MB):
pgrep -fl com.apple.Virtualization.VirtualMachine  # one per Apple container, one for Docker
vmmap --summary <pid> | grep "Physical footprint"  # ~270 MB per idle nginx VM (Apple)
# Don't sum only com.docker.* processes — Docker's VM lives under the
# Virtualization framework process ("Virtual Machine Service for Docker").
```

**One memory-hungry container** shows the flip side: Docker can hand a single
container 4 GB+ straight from the shared pool, while each Apple container is
capped at 1 GiB by default — you raise it per service with `mem_limit` / `cpus`
(passed to the runtime as `-m`/`-c`), which reserves a larger VM for just that
service. The upside of the per-VM model is isolation: a runaway container can't
starve its neighbors of the shared pool.

**Takeaway for the article:** don't oversell idle footprint — and don't
oversell the crossover either. Apple `container` wins when idle and for a
couple of containers; at dev-stack scale (5–10 services) total memory is
**roughly comparable** and state-dependent; past ~10 services Docker's shared
pool wins. The durable memory story is the *model*: exact, linear,
per-container VMs with a fixed cap vs one big elastic, over-committable pool.
The decisive dev-stack difference is speed, not memory.

## Honest tradeoffs (Docker is faster or richer here)

| Dimension | Docker Desktop | opossum / `container` | Takeaway |
|-----------|----------------|------------------------|----------|
| **10× `run --rm` (throwaway), sequential** | 2.1 s | 8.3 s | ~4× slower — each container is a VM |
| **10× `run --rm`, in parallel** | 0.75 s | 7.6 s | gap widens to ~10× — a shared daemon parallelizes, per-container VMs don't |
| **First build in a session** | BuildKit always warm | +~6 s builder-VM cold start | the on-demand builder VM boots on first use |
| **Cached rebuild (no changes)** | 0.21 s | 0.17 s | parity — layer caching works |
| **Disk usage view / cleanup** | `system df` + `system prune` | `system df` (aggregate + reclaimable) + `image prune` / `volume prune`; no single `system prune` | parity on the view; cleanup is split per-resource. `opossum doctor` flags large reclaimable storage |

```sh
# throwaway throughput
for i in $(seq 10); do docker    run --rm alpine echo hi >/dev/null; done   # sequential
for i in $(seq 10); do container run --rm alpine echo hi >/dev/null; done
# builder cold start
container builder stop; time container build -t x .   # first: ~6 s; again: ~0.15 s
```

The throwaway-container gap is architectural (one lightweight VM per container),
so it won't close — worth being upfront about for test-heavy workflows that spin
up hundreds of short-lived containers.

## Daily-op gaps for a docker compose user

opossum covers the common verbs: `up`, `down`, `ps`, `logs -f`, `exec`, `build`,
`pull`, `run`, `restart`, `stop`, `kill`, `stats`, `config`, `images`. Known
gaps:

| Operation | Status | Notes |
|-----------|--------|-------|
| `cp` (copy files to/from a service) | ❌ | `container cp` exists underneath — thin wrapper planned |
| `watch` (live sync/rebuild on change) | ❌ | dev-loop convenience; planned as a `develop.watch` MVP |
| Restart policies (`restart: always`) | ❌ ignored | auto-restarting a crashed container needs a supervisor; a real limitation, not just a missing flag |
| GUI / dashboard | ❌ | opossum is CLI-only — a different tool class than Docker Desktop |
| One-shot `system prune` | ❌ | `container system df` shows the aggregate (and `opossum doctor` flags large reclaimable storage), but cleanup is per-resource — `image prune` / `volume prune`, not one command |

## Article angles, by decision impact

1. **Throwaway-container speed** — 4–10× slower, and parallelism doesn't help.
   Matters most for test suites that churn containers. Now the sharpest
   dev-stack differentiator.
2. **Memory: exact-linear vs elastic-base** — much lighter idle and for 1–2
   containers; roughly comparable at 5–10 services (crossover ~2–7 depending
   on Docker's VM state); Docker wins past ~10. Lead with the model and the
   measurement pitfall (Docker's VM hides under "Virtual Machine Service"),
   not a single crossover number.
3. **Disk model** — native APFS vs a growing `Docker.raw`. A nuanced win, minus
   the missing aggregate `df`.
4. **Builder cold start** — the first build of a session pays ~6 s for the
   builder VM. Minor; just set expectations.
