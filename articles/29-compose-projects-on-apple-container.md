---
title: "Apple's container hit 1.0. Should it be your dev environment yet? I measured."
published: false
tags: docker, macos, devops, containers
canonical_url:
cover_image: # upload assets/cover-29-compose-survey.png when posting
---

Apple's [`container`](https://github.com/apple/container) runtime reached **1.0**
in June, and on macOS 26 containers can finally talk to each other — which is the
last piece needed to run a multi-service dev stack. The pieces are, genuinely,
all here now.

So I wanted an honest answer to the practical question: **should you switch your
daily dev environment to it today?** I measured two things that decide that —
*compatibility* (does your `docker-compose.yml` run?) and *performance* (is it
actually pleasant to live in?). The short version, up front, so I'm not burying
the lede:

> **Compatibility is real — about half of the compose files I tried ran
> unmodified, and ~60% within a one-line fix. But performance is behind
> today**, and for a normal multi-service dev stack that's the part you feel.
> If you want a Docker Desktop replacement *right now*, Docker itself, Colima,
> or OrbStack (the established alternatives — all built on the same shared-VM
> model as Docker) are the pragmatic picks. Apple `container` is the better
> choice today in exactly one shape: **a small image or two, run occasionally.**
> I'd watch it closely — but I wouldn't move my main dev stack onto it yet.

Here's the data behind that.

## Part 1 — Compatibility: better than I expected

I took Docker's own
[awesome-compose](https://github.com/docker/awesome-compose) repository — the
official collection of sample compose projects (WordPress, React+Express+Mongo,
Spring+Postgres, Prometheus+Grafana, …) — and ran **29 of its samples,
unmodified**, on Apple `container`. An alphabetical slice, no cherry-picking:
every sample from `nextcloud-postgres` through `wordpress-mysql`.

### Setup

Apple's `container` is a runtime, not an orchestrator: it has no `compose`
subcommand, no dependency ordering, no service discovery. For that I used
[opossum](https://github.com/suruseas/opossum), a small compose-like
orchestrator for Apple `container` that I've been building. It reads your
existing `compose.yaml` / `docker-compose.yml` and gives you the familiar
verbs — `up`, `ps`, `logs`, `exec`, `down`:

```sh
brew install suruseas/opossum/opossum
container system start                     # start the runtime (once)
sudo container system dns create opossum   # one-time: DNS domain so services
                                           # resolve each other by name
```

Method: macOS 26 on Apple silicon, Apple `container` 1.0. For each sample:
`cd` in, `opossum up`, give it ~90 seconds to come up, check `ps` and `logs`,
categorize, `down`, next. Every failure got a second look to find the actual
root cause — the interesting part is *whose fault* each one was.

### The results

| Outcome | Count | |
|---|---:|---|
| ✅ Ran **as-is** — zero changes | **14** | WordPress, React+Express+MySQL/Mongo, Spring, nginx+Go+MySQL, pgAdmin, Kafka, … |
| 🔧 Ran after a **one-line** compose change | 4 | 3× Postgres data dir, 1× amd64-only image |
| 🏗️ Broken **upstream** — the sample itself has rotted | 5 | would fail regardless of runtime |
| 🚫 Can't run **by design** | 3 | Docker socket ×2, Linux kernel access ×1 |
| ⚙️ Environment / setup | 2 | placeholder path, host port already taken |
| 🐌 Build outlasted my timeout | 1 | a 10–15 min Rust→Wasm build (no errors — I got impatient) |

The headline: **18 of the 29 samples run on Apple's runtime today, 14 of
them without touching a single line.** And of the 11 that didn't, *not one*
failed because of the runtime's container execution or networking — every
failure traced to the sample itself, to Docker-specific host features, or to my
environment.

Let's look at the categories, because a couple of them teach you something
about how Apple's runtime actually differs from Docker.

### The one-line fixes (and what they reveal)

#### Postgres refuses its data volume (3 samples)

`nextcloud-postgres`, `nginx-golang-postgres`, and `spring-postgres` all define
the classic:

```yaml
volumes:
  - db-data:/var/lib/postgresql/data
```

On Docker this works. On Apple `container` Postgres dies with *"initdb: error:
directory exists but is not empty"*. Why: the runtime mounts a named volume as
a real ext4 mount point — which contains `lost+found` — and `initdb` refuses a
non-empty directory. Docker's volumes don't surface that detail.

The one-line fix is the same one you'd use on any bare-metal mount:

```yaml
environment:
  PGDATA: /var/lib/postgresql/data/pgdata   # a subdirectory of the mount
```

This pattern is *everywhere* in self-hosted app composes (Gitea, Nextcloud,
…), so opossum detects it at `up` time and prints exactly that suggested fix.
MySQL and MariaDB tolerate the mount point, which is why the WordPress and
MySQL samples sailed through.

#### amd64-only images (1 sample)

`nginx-nodejs-redis` uses `redismod`, an image published only for x86-64. The
fix is the same line you'd add for Docker on Apple silicon:

```yaml
platform: linux/amd64
```

opossum passes the platform through and automatically enables Rosetta
translation for the container, so the x86-64 image runs on the arm64 VM.

### The ones that were broken before I arrived (5 samples)

This was the fun discovery. Five samples fail *on any runtime* today, because
they pin nothing and the world moved:

- `nginx-flask-mysql` — Flask backend crashes with `ImportError: cannot import
  name 'url_quote' from 'werkzeug.urls'`: the famous Flask/Werkzeug 2.1 break,
  hit because the sample doesn't pin Werkzeug. (The nginx "host not found in
  upstream" error that follows is just the dead backend cascading.)
- `prometheus-grafana` — the unpinned `prom/prometheus` image now rejects the
  sample's `api_version: v1` Alertmanager config.
- `nginx-wsgi-flask`, `react-rust-postgres`, `vuejs` — `pip install` /
  `cargo` / `yarn global add` failures during build, all dependency rot.

I went in expecting to find runtime incompatibilities; instead I found a small
museum of what happens to unmaintained compose files after a few years. If
your own stack pins its images and dependencies, this whole category doesn't
apply to you.

### Can't run by design (3 samples)

Full honesty, because this is where the model genuinely differs:

- **`portainer`, `traefik-golang`** bind-mount `/var/run/docker.sock` — they
  manage/inspect *Docker*. Apple's runtime has no Docker socket; anything whose
  job is talking to the Docker daemon is out.
- **`wireguard`** needs `NET_ADMIN` plus the host's kernel modules
  (`/lib/modules`). There's no shared Linux host kernel to reach into — each
  container has its own micro-VM. (Docker Desktop on a Mac can't satisfy the
  host-path part either.)

If your compose file is "a normal app + database + cache", none of this
touches you. If it's host-level infrastructure tooling, stay on Docker.

### The rest

- `plex` ships a placeholder bind path (`/media/your/plex/path`) you're meant
  to edit — environment, not compatibility.
- `pihole-cloudflared-DoH` wants host port 53, which my Mac was already using.
  (opossum checks published ports up front and names the conflict instead of
  failing mid-startup.)
- `wasmedge-mysql-nginx` compiles Rust to WebAssembly inside the build —
  10–15 minutes of `cargo build`. It was progressing without errors when my
  90-second patience ran out, so it goes in the "almost certainly fine" pile,
  not the failure pile.

### Gotchas beyond this sample set

Two more differences worth knowing before you try your own stack, found in
wider testing:

- **Fresh volumes mount empty.** Docker pre-fills a brand-new named/anonymous
  volume from the image's contents at that path; Apple `container` doesn't.
  That breaks the beloved `- /app/node_modules` trick. opossum replicates
  Docker's seeding behavior itself, so this pattern works — but it's a real
  runtime difference you'd hit with raw `container` commands.
- **cgroup-sniffing JVMs crash.** Elasticsearch 7.x's bundled JDK reads the
  host cgroup to size its heap, and the micro-VM doesn't expose the mount it
  expects — it dies at launch before any config applies. A runtime/JDK
  incompatibility with no workaround I know of.

## Part 2 — Performance: this is where it's behind today

Compatibility was the good news. Performance is the honest bad news, and it's
the part you actually feel day to day. All numbers below are from one machine
(M2, 16 GB) — reproduce them yourself; the point is the *shape*, not the third
decimal.

**Memory doesn't work the way the idle number suggests.** The headline "~50 MB
idle vs Docker's multi-GB VM" is true, but it's only the start of the story,
because the two runtimes allocate memory completely differently. Apple `container` runs **one VM
per container**, and that cost is exact and verifiable: an idle `nginx:alpine`
VM has a **~270 MB** physical footprint (a bare alpine idles at ~235–255 MB —
that's the fixed guest-kernel floor, and lowering the `-m` cap doesn't lower
it), it scales dead linearly (six containers = six VMs ≈ 1.6 GB + ~20 MB of
helpers each), and macOS genuinely attributes the guest's memory to the VM
process — I held 300 MB of incompressible data inside one and watched its
footprint grow by exactly that (254 M → 557 M).

Docker runs **one shared VM** all containers draw from — and measuring it
honestly has a trap I fell into myself: Docker's guest memory doesn't show up
under any `com.docker.*` process. It lives in a process Activity Monitor calls
**"Virtual Machine Service for Docker"** (both runtimes use Apple's
Virtualization framework), and the same 300 MB test grows *that* process by
exactly 0.3 GB (1.1 G → 1.4 G). Sum it correctly and Docker looks like this:

| | Apple `container` (exact, linear) | Docker Desktop (elastic base) |
|---|---|---|
| Nothing running | **~50 MB** | ~1.4–2.1 GB warm (helpers + VM; shrinks only over long idle) |
| Each added small container | **+~290 MB** | ~**+0** (six more nginx moved it ~10 MB) |
| Heavy workload ends | VM freed with the container | VM **keeps** its high-water mark |

So the honest memory verdict is subtler than either camp's pitch: Apple wins
clearly when idle or running 1–2 things; **at a typical 5–10 service stack the
totals are comparable** (Apple ~1.5–3 GB of exact, linear cost vs Docker's
~1.4–2.1 GB elastic base); past ~10 services Docker's shared pool wins. The
crossover lands anywhere from ~2 to ~7 containers depending on how warm
Docker's VM is. The per-VM model's real upside is isolation (a runaway
container can't starve its neighbors) and *predictability* — you can point at
the exact process each container costs you.

**What actually decides the dev-stack question is speed, not memory.** Same
machine:

| | Docker Desktop | Apple `container` |
|---|---|---|
| Single container start | **~0.19 s** | ~0.81 s (boots a fresh micro-VM) |
| 10× `run --rm`, sequential | **2.1 s** | 8.3 s (~4× slower) |
| 10× `run --rm`, in parallel | **0.75 s** | 7.6 s (~10× — a shared daemon parallelizes, per-VM doesn't) |
| First build in a session | warm | +~6 s builder-VM cold start |
| Bind-mount small-file I/O | slow | slightly **slower** (same host↔VM model) |

The throwaway-container gap is architectural — one lightweight VM per container —
so it won't simply close with a point release. For anything that churns
short-lived containers (test suites, CI-like loops) it's a real tax today.

So the pieces are all present, but the performance envelope says: **not yet for
a daily multi-service dev stack.** ([Full method and the wider comparison](https://github.com/suruseas/opossum/blob/main/docs/vs-docker-desktop.md).)

## The verdict: when to pick it (and when not to, yet)

| Your situation | Today's pick |
|---|---|
| Daily multi-service dev stack (app + DB + cache + …) | **Docker / Colima / OrbStack** — faster where it counts (memory is roughly comparable at this scale) |
| Churning lots of short-lived containers (tests) | **Docker** — per-VM start cost hurts here |
| Needs `docker.sock`, `NET_ADMIN`, host kernel | **Docker** — Apple `container` can't, by design |
| A small image or two, run occasionally, mostly idle | **Apple `container`** — genuinely lighter, cleaner isolation, no license |
| You want per-container VM isolation and no always-on VM | **Apple `container`** — the one thing nothing else here gives you |

That's the honest 2026 read: **the 1.0 pieces are in place, compatibility is
real, but the performance envelope means Docker and the established alternatives
are the practical choice for a normal dev environment right now.** Apple
`container`'s current sweet spot is narrow — small, occasional, idle-most-of-the-
time workloads — and its structural advantage (VM-per-container isolation with no
resident VM) is worth watching as the runtime matures. I'd revisit this in a few
releases; I wouldn't move my main stack over today.

## Try it yourself — it's safe next to Docker

If you want to see where your own compose lands, it's three commands, and it
won't touch your Docker state (Apple's runtime keeps entirely separate images,
containers, and volumes; `opossum down -v` only ever removes its own):

```sh
brew install suruseas/opossum/opossum
cd your-project        # with its existing docker-compose.yml
opossum config         # optional: preview which fields (if any) get ignored
opossum up
```

Repo: <https://github.com/suruseas/opossum>. I'd genuinely like to grow this
survey — if you run `opossum up` on a real compose file and it breaks, tell me
what broke (here or in an issue). And if you re-run these perf numbers on your
hardware and get something different, I want to know that too. The point is an
honest picture, not a pitch.
