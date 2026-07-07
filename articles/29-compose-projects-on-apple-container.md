---
title: "I ran 29 real docker-compose projects on Apple's container runtime. Here's what broke"
published: false
tags: docker, macos, devops, containers
canonical_url:
cover_image: # upload assets/cover-29-compose-survey.png when posting
---

Docker Desktop keeps a **~7.8 GB Linux VM** provisioned the entire time it's
running — even when zero containers are up. Apple's new
[`container`](https://github.com/apple/container) runtime idles at **~58 MB** of
helper processes and only spends memory while a container actually runs, because
it boots a lightweight VM *per container*, on demand. It hit 1.0 in June, and on
macOS 26 containers can finally talk to each other, which makes multi-service
dev stacks possible.

So the question stopped being "is it interesting?" and became: **is it
compatible enough to run the `docker-compose.yml` files people actually have?**

Instead of guessing, I measured. I took Docker's own
[awesome-compose](https://github.com/docker/awesome-compose) repository — the
official collection of sample compose projects (WordPress, React+Express+Mongo,
Spring+Postgres, Prometheus+Grafana, …) — and ran **29 of its samples,
unmodified**, on Apple `container`. An alphabetical slice, no cherry-picking:
every sample from `nextcloud-postgres` through `wordpress-mysql`.

Here's the breakdown, and — more useful — *why* each failure failed.

## Setup

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

## The results

| Outcome | Count | |
|---|---:|---|
| ✅ Ran **as-is** — zero changes | **14** | WordPress, React+Express+MySQL/Mongo, Spring, nginx+Go+MySQL, pgAdmin, Kafka, … |
| 🔧 Ran after a **one-line** compose change | 4 | 3× Postgres data dir, 1× amd64-only image |
| 🏗️ Broken **upstream** — the sample itself has rotted | 5 | would fail regardless of runtime |
| 🚫 Can't run **by design** | 3 | Docker socket ×2, Linux kernel access ×1 |
| ⚙️ Environment / setup | 2 | placeholder path, host port already taken |
| 🐌 Build outlasted my timeout | 1 | a 10–15 min Rust→Wasm build (no errors — I got impatient) |

The headline: **18 of 29 real-world stacks run on Apple's runtime today, 14 of
them without touching a single line.** And of the 11 that didn't, *not one*
failed because of the runtime's container execution or networking — every
failure traced to the sample itself, to Docker-specific host features, or to my
environment.

Let's look at the categories, because a couple of them teach you something
about how Apple's runtime actually differs from Docker.

## The one-line fixes (and what they reveal)

### Postgres refuses its data volume (3 samples)

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

### amd64-only images (1 sample)

`nginx-nodejs-redis` uses `redismod`, an image published only for x86-64. The
fix is the same line you'd add for Docker on Apple silicon:

```yaml
platform: linux/amd64
```

opossum passes the platform through and automatically enables Rosetta
translation for the container, so the x86-64 image runs on the arm64 VM.

## The ones that were broken before I arrived (5 samples)

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

## Can't run by design (3 samples)

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

## The rest

- `plex` ships a placeholder bind path (`/media/your/plex/path`) you're meant
  to edit — environment, not compatibility.
- `pihole-cloudflared-DoH` wants host port 53, which my Mac was already using.
  (opossum checks published ports up front and names the conflict instead of
  failing mid-startup.)
- `wasmedge-mysql-nginx` compiles Rust to WebAssembly inside the build —
  10–15 minutes of `cargo build`. It was progressing without errors when my
  90-second patience ran out, so it goes in the "almost certainly fine" pile,
  not the failure pile.

## Gotchas beyond this sample set

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

And the trade-offs cut both ways — this isn't "Docker but better":

| | Docker Desktop | Apple `container` |
|---|---|---|
| Idle memory | ~373 MB host procs + **~7.8 GB VM** | **~58 MB**, no always-on VM |
| Single container start | **~0.19 s** | ~0.81 s (boots a fresh micro-VM) |
| Bind-mount small-file I/O | slow | slightly **slower** (same host↔VM model) |
| Isolation | shared VM kernel | per-container VM |
| License | paid for larger orgs | open source |

Docker still wins if you churn many short-lived containers or hammer bind
mounts. Apple's runtime wins on footprint and isolation. (Numbers from one
machine — [method and caveats here](https://github.com/suruseas/opossum/blob/main/docs/benchmarks.md).)

## Try it on your own compose file

The whole experiment, reproduced on your project, is three commands:

```sh
brew install suruseas/opossum/opossum
cd your-project        # with its existing docker-compose.yml
opossum up
```

It's safe to try next to Docker: Apple's runtime keeps entirely separate
images, containers, and volumes, so your Docker state isn't touched. `opossum
config` shows you up front which compose fields (if any) it will ignore, and
`down` cleans everything up.

Based on this sample: **48% of real-world stacks ran with zero changes, 62%
with at most one line** — and the failures concentrated in stale samples and
Docker-specific tooling, not in the runtime.

Repo: <https://github.com/suruseas/opossum> — and I'd genuinely like to grow
this survey: if you run `opossum up` on a real compose file and it breaks,
tell me what broke (here or in an issue). The failure catalog is the product.
