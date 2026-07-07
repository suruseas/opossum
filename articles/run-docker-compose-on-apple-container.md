---
title: "Run your docker-compose.yml on Apple's container runtime"
published: false
tags: docker, macos, containers, devtools
canonical_url:
cover_image:
---

If you develop on a Mac and reach for `docker compose` to spin up a multi-service
stack, you might like **[opossum](https://github.com/suruseas/opossum)** — a small,
Docker Compose–like orchestrator for **[Apple's `container`](https://github.com/apple/container)**
runtime on macOS 26.

The pitch is simple: **point it at a `docker-compose.yml` you already have and run
`opossum up`.** The commands and mental model are the same ones you already know —
`up`, `ps`, `logs`, `down` — but everything runs on Apple's native `container`
runtime (a lightweight VM per container) instead of Docker Desktop.

Let me show you.

## Install

opossum ships as a Homebrew formula:

```sh
brew install suruseas/opossum/opossum
```

Two one-time steps (Apple `container` prerequisites):

```sh
container system start                    # start the runtime
sudo container system dns create opossum  # register a local DNS domain so
                                          # services can find each other by name
```

That's it. (Requires macOS 26 on Apple silicon, since the runtime's
container-to-container networking relies on macOS 26 features.)

## Just try it on a project you already have

The fastest way to get a feel for it: `cd` into a directory you already run with
`docker compose` and run `opossum up`. For a lot of stacks it just works — same
file, same command, nothing to change. (opossum finds your `compose.yaml` or
`docker-compose.yml` automatically.)

And it's **safe to try side by side with Docker.** opossum drives Apple's
`container` runtime, which is entirely separate from Docker — its own images,
containers, and volumes — so running `opossum up` won't touch your Docker
containers or data.

To follow along in this post, here's a tiny stack:

```yaml
services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    depends_on:
      - redis
  redis:
    image: redis:7
```

## `opossum up`

```console
$ opossum up
Creating network intro-net
Starting redis (redis:7)
redis.intro.opossum
Starting web (nginx:alpine)
web.intro.opossum
  ↳ web on the host: localhost:8080
```

It starts the services in dependency order (redis before web, because `web`
`depends_on` it) and tells you where to reach a published service from the host —
`localhost:8080`.

## `opossum ps`

Same output you'd expect from `docker compose ps`:

```console
$ opossum ps
SERVICE  CONTAINER            IMAGE         IP            PORTS                 STATUS
redis    redis.intro.opossum  redis:7       192.168.67.2  -                     running
web      web.intro.opossum    nginx:alpine  192.168.67.3  0.0.0.0:8080->80/tcp  running
```

Open `http://localhost:8080` in your browser (or curl it) and you get nginx:

```console
$ curl -s http://localhost:8080/ | head -4
<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
```

## Services find each other by name

Just like Compose, peers reach each other by their **service name** over the
project's network. From inside the `web` container, `redis` resolves:

```console
$ opossum exec web sh -c "getent hosts redis"
...  redis.intro.opossum  redis
```

So your app connects to `redis:6379` — no IPs, no hardcoding.

## `opossum logs` and `opossum stats`

Follow logs like usual:

```console
$ opossum logs redis
...
1:M 06 Jul 2026 16:43:09.983 * Ready to accept connections tcp
```

And there's a `docker stats`–style live view of resource usage per service:

```console
$ opossum stats --no-stream
Container ID         Cpu %  Memory Usage          Net Rx/Tx             Block I/O             Pids
redis.intro.opossum  0.74%  29.91 MiB / 1.00 GiB  16.18 KiB / 0.57 KiB  25.68 MiB / 0.00 KiB  6
web.intro.opossum    0.00%  15.48 MiB / 1.00 GiB  15.15 KiB / 2.14 KiB  9.94 MiB / 4.00 KiB   6
```

## `opossum down`

```console
$ opossum down
Stopping web
Stopping redis
```

Tears everything down in reverse order and removes the project network. Add `-v`
to also drop named volumes.

## It's *almost* the same as docker compose

If you know Compose, you already know opossum. The everyday commands map 1:1:

| You'd type in Compose | With opossum |
|---|---|
| `docker compose up` | `opossum up` |
| `docker compose ps` | `opossum ps` |
| `docker compose logs -f web` | `opossum logs web -f` |
| `docker compose exec web sh` | `opossum exec -it web sh` |
| `docker compose down -v` | `opossum down -v` |

opossum also has `stats`, `images`, `pull`, `build`, `run`, `start`/`stop`/
`restart`/`kill`, and `config` — the usual toolbox.

A few honest differences, since it targets Apple's runtime rather than Docker:

- **One-time setup**: the `sudo container system dns create opossum` step above,
  which is what makes bare-name service discovery work.
- **Runtime**: it's Apple `container` (a per-container lightweight VM), not Docker
  Engine. Compose fields the runtime can't act on (like custom `networks:` or
  `container_name:`) are parsed and skipped, not silently dropped — `opossum
  config` (or `--verbose`) lists exactly what was ignored.
- **macOS 26 + Apple silicon** only.

## Try it

```sh
brew install suruseas/opossum/opossum
cd your-project-with-a-compose-file
opossum up
```

Repo: <https://github.com/suruseas/opossum>. If you have a `docker-compose.yml`
lying around, give `opossum up` a try and see how far it gets — for a lot of
stacks, the answer is "all the way."
