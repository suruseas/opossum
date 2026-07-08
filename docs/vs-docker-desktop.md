# opossum / Apple `container` vs Docker Desktop

An honest, measured comparison for developers weighing a switch from Docker
Desktop. The goal is to be straight about the tradeoffs: some differences favor
opossum, some favor Docker, and a few are gaps we can close.

Measured on an Apple silicon Mac (M2, 8 cores, 16 GB), Docker Desktop 29.5.3 vs
Apple `container` 1.1.0 with opossum. Numbers are indicative of one machine —
reproduce with the commands shown. See also
[benchmarks.md](benchmarks.md) for idle footprint and single-run startup.

## Where opossum is ahead

- **Idle memory is ~10× lighter.** With nothing running, Docker Desktop's host
  processes hold ~490 MB *plus a resident Linux VM*; Apple `container`'s
  background services hold ~50 MB and there's **no always-on VM** (it starts a
  VM per container, on demand). Idle CPU is ~0 % for both.

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

Measured host memory (actual RSS, not the reserved cap) for N idle
`nginx:alpine` containers on a 16 GB M2. VM memory ballooning makes point-in-time
RSS noisy, so treat these as ranges:

| N containers | Apple `container` | Docker Desktop |
|--------------|-------------------|----------------|
| 0 (idle) | ~50 MB (no VM) | ~640 MB (resident VM + host) |
| 1 | ~400 MB | ~680 MB |
| 3 | ~800 MB–1.3 GB | ~760 MB |
| 6 | ~1.4 GB | ~890 MB |
| 10 (projected) | ~2.5 GB | ~1.06 GB |

Roughly: **Apple ≈ 250–400 MB × N** (each container is a full VM) vs **Docker ≈
640 MB + ~42 MB × N** (containers share the VM). So:

- **Idle or 1–2 containers → Apple is lighter** (no always-on VM tax).
- **The crossover is early — around 3 containers**, not the hundreds you'd
  expect if a container only cost tens of MB. Each Apple container is a whole VM.
- **A typical multi-service compose (5–10 services) uses *more* total memory on
  Apple `container` than on Docker.** Worth saying plainly.

```sh
# per-container host memory: diff the process list before/after `container run -d`.
# each container adds a `com.apple.Virtualization.VirtualMachine` process (~250-400 MB).
```

**One memory-hungry container** shows the flip side: Docker can hand a single
container 4 GB+ straight from the shared pool, while each Apple container is
capped at 1 GiB by default — you raise it per service with `mem_limit` / `cpus`
(passed to the runtime as `-m`/`-c`), which reserves a larger VM for just that
service. The upside of the per-VM model is isolation: a runaway container can't
starve its neighbors of the shared pool.

**Takeaway for the article:** don't oversell idle footprint. Apple `container`
wins when idle and for a handful of containers; Docker's shared pool wins once
several run at once. The real story is *per-container VM isolation with a fixed
cap* vs *a shared, over-committable pool*.

## Honest tradeoffs (Docker is faster or richer here)

| Dimension | Docker Desktop | opossum / `container` | Takeaway |
|-----------|----------------|------------------------|----------|
| **10× `run --rm` (throwaway), sequential** | 2.1 s | 8.3 s | ~4× slower — each container is a VM |
| **10× `run --rm`, in parallel** | 0.75 s | 7.6 s | gap widens to ~10× — a shared daemon parallelizes, per-container VMs don't |
| **First build in a session** | BuildKit always warm | +~6 s builder-VM cold start | the on-demand builder VM boots on first use |
| **Cached rebuild (no changes)** | 0.21 s | 0.17 s | parity — layer caching works |
| **Disk usage view / cleanup** | `system df` + `system prune` | per-image sizes (`image list --verbose`) + `image prune`; **no `system df`** | Docker gives an aggregate view; `container` doesn't |

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
| Aggregate disk usage (`system df`) | ❌ | a runtime gap; you can list per-image sizes but not a total |

## Article angles, by decision impact

1. **Memory: idle vs. at scale** — the sharpest, most honest story. ~10× lighter
   idle and for 1–2 containers, but the per-container VM model means it crosses
   over Docker's shared pool at ~3 containers; a 5–10 service compose uses more.
   Lead with the nuance, not just the idle number.
2. **Throwaway-container speed** — 4–10× slower, and parallelism doesn't help.
   Matters most for test suites that churn containers.
3. **Disk model** — native APFS vs a growing `Docker.raw`. A nuanced win, minus
   the missing aggregate `df`.
4. **Builder cold start** — the first build of a session pays ~6 s for the
   builder VM. Minor; just set expectations.
