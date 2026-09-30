# Getting started with opossum

This page is for you if you have never used Docker or Docker Compose and want to
run your first project on Apple's `container`. It takes you from an empty
directory to a web server and a database running side by side, then shows you
how to stop it and clear away its containers, network and data. If you already have a `compose.yaml`
from Docker, the [README quickstart](../README.md#quickstart-coming-from-docker-compose)
is faster.

**What opossum is:** you describe the pieces of your app (a web server, a
database) in one file called `compose.yaml`, and `opossum up` starts them all,
connected to each other. `opossum down` removes them again.

## 1. Get the machine ready

You need a Mac with Apple silicon on macOS 26 and Apple's
[`container`](https://github.com/apple/container) installed. Then:

```sh
container system start
sudo container system dns create opossum
opossum doctor
```

- `container system start` starts Apple's container system. If it is already
  running it says so; nothing is harmed.
- `sudo container system dns create opossum` registers a local DNS domain so
  your services can find each other by name. It asks for your password and only
  needs to be done once per Mac.
- `opossum doctor` changes nothing in your project and does not start the
  container system. Its network check does pull a small image (`alpine`) and run
  one throwaway container. You are ready when no line starts with ❌. A ⚠️ line (for example about disk space
  or the build VM's memory) is advice, not a blocker; a ❌ says what to fix,
  with the command on the next line.

## 2. Your first project

Make an empty directory and put this in a file named `compose.yaml`:

```yaml
name: hello-db
services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: demo
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - db_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 3s
      timeout: 3s
      retries: 20
volumes:
  db_data:
```

It describes two services: `web` (an nginx web server, reachable on your Mac at
port 8080) and `db` (a Postgres database that keeps its data in a named volume
called `db_data`). `web` waits until `db` reports healthy before it starts. The
same file lives in [`examples/hello-db/`](../examples/hello-db/compose.yaml).

Now run it, from that directory:

```sh
opossum up        # start everything; the first run downloads the images
opossum ps        # every service should say "running"
curl localhost:8080   # the nginx welcome page
opossum logs db   # what the database printed
opossum down -v   # stop and remove the containers, network and database data
```

`opossum up` finishes with exit code 0 when every service started. Leave `-v`
off `down` if you want to keep the database's data for next time: without it the
volume stays, and the next `up` finds your data again. The images you pulled
stay on your Mac either way; `opossum destroy` removes those too.

## 3. Writing a compose file that works the first time

These are the things that most often cost a retry:

- **Pick an image with an arm64 build**, or add `platform: linux/amd64` to the
  service. Official images for popular software (`postgres`, `redis`, `nginx`,
  `node`, `python`) almost always have one. If a small image has none, `up`
  tells you.
- **Keep a database's data in a named volume**, as above. That is what lets the
  data survive `down` and restarts. For Postgres, set `PGDATA` one level below
  the mount, as in the example: it works on every Postgres version.
- **Give each service its own volume.** One named volume can be attached to only
  one running container at a time.
- **Reach other services by their name in the compose file**, not `localhost`.
  Inside `web`, the database is at the host name `db`. `localhost` inside a
  container means that container alone.
- **Wait for readiness with a `healthcheck`**, not a sleep. A plain `depends_on`
  only waits for a container to start; `condition: service_healthy` waits until
  the check passes.
- **Publish host ports other than 5000 and 7000.** macOS's AirPlay Receiver
  listens on both, and `up` refuses them up front. Ports in the 3000s and 8000s
  are safe choices.
- **Don't mount the Docker socket** (`/var/run/docker.sock`). There is no
  Docker daemon here for it to reach.

## 4. Asking an AI to build it for you

If you would rather describe what you want than write the file yourself, give an
AI agent (Claude Code, for instance) the [`AGENTS.md`](../AGENTS.md)
file from the opossum repository. Its "Starting from nothing" section is written
for exactly this: an empty directory and a request. For example:

> Read AGENTS.md from the opossum repository. In this empty directory, use
> opossum to build a small web app with a Postgres database, start it, check
> with `opossum ps` that everything is running, and tell me the address to open.

The agent checks the machine with `opossum doctor`, writes a `compose.yaml`,
brings it up, and reports what it did.

## 5. Where to go next

- [Compatibility](compatibility.md): which compose features opossum supports,
  and what to do with an existing Docker Compose project.
- [Troubleshooting](troubleshooting.md): what an error code such as `OPSM-201`
  means and how to fix it.
- [Networking](networking.md): how services find each other and how to publish
  ports.
