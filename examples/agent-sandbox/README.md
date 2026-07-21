# agent-sandbox — an autonomous coding agent, boxed in a VM

Run [Claude Code](https://docs.anthropic.com/en/docs/claude-code) fully
autonomously (`--dangerously-skip-permissions`) inside an Apple `container` VM, so
the blast radius of whatever it does is the VM — not your Mac.

The idea worth taking away is that **the compose file is the declaration of what
the agent is allowed to touch**:

| Boundary | Declared by | Here |
|----------|-------------|------|
| Files it sees | `volumes:` (bind mount) | `./work:/work` — nothing else on your disk |
| Secrets it holds | `environment:` from `.env` | the auth token, and only that |
| How far it reaches | `networks:` | direct internet (default), or host-only + an allowlist proxy (caged) |
| Reaching the host | `${OPOSSUM_HOST_GATEWAY}` | the caged agent's route to the proxy service |

Everything an agent needs to be boxed in is already ordinary compose vocabulary.

## Quickstart

```sh
container builder start                         # the image builder VM (once)
cd examples/agent-sandbox
opossum build                                   # builds agent-sandbox-agent:latest

cp .env.example .env                            # then put a token in it (see Auth)

mkdir -p work && git -C work clone <your-repo>  # give the agent something to work on
opossum run --rm agent \
  claude --dangerously-skip-permissions -p "fix the failing test in ./repo and commit"
```

The agent works inside the VM; its changes land in `./work` on your Mac (a bind
mount), so you review them with normal `git`/editor tools afterwards. `--rm`
throws the container away when it exits — the workroom is disposable.

`opossum run` wires the resources (`mem_limit`/`cpus`), `user:`, `working_dir:`,
and network for the one-off exactly as `up` would.

## Auth (bring your own)

Claude Code on your Mac stores its login in the **macOS Keychain**, which can't be
bind-mounted into a Linux VM. So you pass credentials in explicitly — pick one and
put it in `.env` (git-ignored; never commit it):

- **`CLAUDE_CODE_OAUTH_TOKEN`** — a long-term token for your existing Claude plan.
  Generate it on the host with `claude setup-token` and paste the result.
- **`ANTHROPIC_API_KEY`** — an [API key](https://console.anthropic.com/) (API
  billing instead of your plan).

`compose.yaml` references these as `${…:-}`, so the values live only in `.env`.

> Note: `opossum --verbose` echoes each `container` invocation, which includes
> `-e CLAUDE_CODE_OAUTH_TOKEN=<value>`. Don't share `--verbose` output — it
> contains your token.

## Why non-root

Claude Code refuses `--dangerously-skip-permissions` when it runs as **root**. The
image therefore runs as its built-in non-root `node` user (`user: node` in the
compose file) — which is the clean way to let the agent run unattended inside the
already-isolating VM, rather than reaching for a root escape hatch.

## Constraining egress (the caged variant)

By default the agent reaches the Claude API — and the rest of the internet —
directly. When you don't want that, the `agent-caged` variant fences its egress to
an allowlist, and **the whole cage is declared in this one compose file** — no
proxy to set up on the host:

- **`agent-caged`** sits on a **host-only** network (`caged: internal: true`): it
  has no direct internet at all.
- **`proxy`** is a small [tinyproxy](https://tinyproxy.github.io/) forward proxy on
  the normal network (so *it* can reach the internet), republished to the host. The
  agent reaches it at `${OPOSSUM_HOST_GATEWAY}:8080` via `HTTPS_PROXY`.
- **`proxy/allowlist`** is the declaration of where the agent may go — one host
  regex per line, **default-deny**. As shipped it permits only `anthropic.com` /
  `claude.ai`. Add a line to widen it.

Because the agent has no internet route of its own, the proxy is its *only* way out,
so the allowlist is enforced, not merely advised.

```sh
opossum build                       # builds the agent and proxy images
opossum --profile caged run --rm agent-caged \
  claude --dangerously-skip-permissions -p "…task…"
```

The `--profile caged` opts into the cage: without it, neither the internal network
nor the proxy is created, and a plain `opossum up` (or `run agent`) never starts
them. `run` brings the `proxy` dependency up first, then runs the agent.

Sanity-check the fence yourself (the image includes `curl`, which honours
`HTTPS_PROXY`):

```sh
# allowed → reaches the API (an HTTP status, e.g. 404 for a bare GET)
opossum --profile caged run --rm agent-caged \
  curl -s -o /dev/null -w '%{http_code}\n' https://api.anthropic.com/
# not on the allowlist → refused by the proxy
opossum --profile caged run --rm agent-caged \
  curl -sS -o /dev/null https://example.org/ ; echo "exit $?"
```

Notes:

- It's a **CONNECT** proxy: it gates HTTPS by destination host without decrypting
  it (no MITM, no CA to install) — enough to fence *where* the agent goes.
- The proxy port is published on the host (`0.0.0.0:8080`), so on an untrusted LAN
  other machines can use it as a relay to the allowed hosts. For a shared network,
  add a tinyproxy `Allow` line (or bind the port to a specific address).
- "No internet of its own" fences the *internet*: the agent can still reach
  services your Mac exposes on `0.0.0.0` via `${OPOSSUM_HOST_GATEWAY}` (that's how
  it reaches the proxy). Don't run other host services you don't want it touching.
- Pair it with `cap_drop: [ALL]` if you want to stop the agent from reconfiguring
  its own networking.
- See the main README's
  [Constraining egress (agent sandboxes)](../../README.md#constraining-egress-agent-sandboxes)
  for the underlying network model.

## Limits — what the VM does and doesn't protect

1. **The VM protects your host, not the secrets you hand the agent.** The token in
   `.env` and whatever lives under `./work` are *inside* the blast radius by
   design — the boundary is around the rest of your Mac, not around those.
2. **No true nesting.** The VM has no `/dev/kvm`, so the agent can't run Apple
   `container` (or a nested VM) from inside — a task that shells out to
   `container`/`docker` won't work here.
3. **No host Docker bridge.** There's no `docker.sock` to mount in; handing the
   agent the host's Docker would defeat the whole isolation, so it's a non-goal.
4. **A dockerd *inside* the VM (containers-in-VM) is feasible but not wired here.**
   The guest has cgroup v2, overlayfs, and `--cap-add ALL` available — the pieces
   a rootful dockerd needs — but running one is left as future work.
5. **Egress is declarative.** `network_mode: none` (full block) or an `internal:`
   network + the bundled allowlist proxy let you decide how far the agent reaches,
   all in the compose file — see the caged variant above.

## Resources

The default guest is ~1 GiB RAM / 5 vCPU, which is tight once the agent runs
`npm install` + build + test. This example sets `mem_limit: 4g` / `cpus: 4`; bump
them for heavier repos.
