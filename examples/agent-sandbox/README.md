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

## Giving the agent tools (MCP servers) — the box and its toolbox in one file

The compose file already declares what the agent can *touch* (files, secrets, how
far it reaches). It can also declare what tools it *has*. Put an MCP server in the
compose and name it on the agent with **`x-opossum-mcp-tools`**, and opossum
generates a `.mcp.json` and mounts it into the box at `/run/opossum/mcp.json` — no
hand-writing server URLs.

The `agent-tools` variant does exactly this with a `terraform` MCP server:

```yaml
  terraform:                                    # an ordinary HTTP MCP server
    image: hashicorp/terraform-mcp-server
    command: ["streamable-http", "--transport-host", "0.0.0.0"]
    ports: ["8080:8080"]
    profiles: ["tools"]
  agent-tools:
    # …same box as `agent`…
    x-opossum-mcp-tools: [terraform]            # ← the tools face of the manifest
    depends_on: [terraform]
    profiles: ["tools"]
```

Run it, passing the generated config to Claude Code with `--mcp-config`:

```sh
opossum build   # build the agent image once
opossum --profile tools run --rm agent-tools \
  claude --dangerously-skip-permissions --mcp-config /run/opossum/mcp.json \
  -p "use the terraform tools to …"
```

opossum turns `x-opossum-mcp-tools: [terraform]` into
`{"mcpServers":{"terraform":{"type":"http","url":"http://terraform:8080/mcp"}}}`
— the agent reaches the server by name on the shared network. `--mcp-config` (not a
project-scoped `.mcp.json`) is what lets a headless `--dangerously-skip-permissions`
run use the tools without an approval prompt.

Declaration forms for each entry:

- **`service`** — another compose service; the URL is built from its single
  published port (path defaults to `/mcp`).
- **`service:port`** / **`service:port/path`** — pin the port/path explicitly.
- **`name=url`** — an explicit URL, for a server reached some other way. The
  **caged** agent (below) uses this: it has no name resolution and its only internet
  is the proxy, so it points at the tool *through the host gateway*.

**Scope:** HTTP-transport MCP servers only. A stdio server has no cross-container
stdin path, so it's out of scope here (run those with `opossum run --rm <server>`).

### A tool on the *caged* agent (egress fenced, one tool open)

`agent-caged` shows the hardest case: the internet is fenced to an allowlist, yet
one host tool is open. Because it's on a host-only network, it can't reach the
`terraform` server by name, so its tool is declared as an explicit URL through the
host gateway (terraform publishes host port `8091`), and that gateway is added to
`NO_PROXY` so the tool traffic bypasses the egress proxy — the tool is a *granted
capability*, not open internet:

```yaml
  agent-caged:
    networks: [caged]                                    # host-only: no direct internet
    x-opossum-mcp-tools: ["terraform=http://${OPOSSUM_HOST_GATEWAY}:8091/mcp"]
    environment:
      HTTPS_PROXY: http://${OPOSSUM_HOST_GATEWAY}:8080   # internet only via the allowlist
      NO_PROXY: ${OPOSSUM_HOST_GATEWAY}                   # …but the tool bypasses it
```

```sh
opossum --profile caged run --rm agent-caged \
  claude --dangerously-skip-permissions --mcp-config /run/opossum/mcp.json \
  -p "use the terraform tools to …"
```

So on the **internet** the caged agent reaches only what the allowlist permits
(e.g. `api.anthropic.com`), plus the `terraform` tool through the host gateway.

Two honest caveats: `NO_PROXY` matches the gateway *host* (not just port 8091), so
any service your Mac exposes on `0.0.0.0` is reachable to the agent too (see the
"No internet of its own…" note above) — don't run host services you don't want it
touching. And `terraform`'s port is published on `0.0.0.0:8091`, so on an untrusted
LAN other machines can reach that MCP server; bind it to a specific address or fence
the LAN if that matters.

## Try, then roll back (workspace snapshots)

Let the agent try something risky, and reset in an instant if it goes wrong. The
box (the VM) is already disposable; the only state worth keeping is `./work`, so
that's what you snapshot:

```sh
opossum ws snapshot before-refactor   # save ./work (name is optional)
opossum run --rm agent claude --dangerously-skip-permissions -p "…a risky change…"
opossum ws ls                          # list snapshots
opossum ws rollback before-refactor    # didn't like it? reset ./work
opossum ws prune                       # clear out the auto-saved rollback points
```

Snapshots are APFS **copy-on-write clones**: taking one is near-instant and costs
almost no extra disk until the workspace and the snapshot diverge, so you can save
liberally. `rollback` saves the current state first (as a `before-rollback-…`
snapshot), so a rollback is itself reversible. They're stored in a
`.opossum-snapshots/` directory beside `./work` (already git-ignored here, and not
part of the bind mount). `--path` targets a different directory. Tidy up with
`ws rm <name>` or `ws prune` (which clears the auto-saved `before-rollback-…`
points; `--all` clears the ones you named too).

Snapshotting while the agent is mid-write is best-effort — an in-flight file is
captured as it is at that instant — so snapshot between runs for a clean point. On
a non-APFS filesystem it falls back to a full copy and tells you.

## Check what happened in the box (`run --audit`)

Declaring what an agent *may* do is only half of it — you also want to see what it
*did*. `run --audit` reports that after the run:

```sh
# The caged agent (its egress is fenced), audited:
opossum --profile caged run --rm --audit agent-caged \
  claude --dangerously-skip-permissions --mcp-config /run/opossum/mcp.json \
  -p "…a task…"
```

```
Audit of `agent-caged` … — exit 0
  files:  2 changed, 1 added, 0 deleted under …/work
    changed  src/main.py
    added    notes.md
    …
  egress: 1 destination(s) (via proxy): api.anthropic.com:443
  resources: unobserved (…)
```

- **files** — the workspace (`./work`) diff: what the run added, changed, or deleted
  (with content hashes), computed from an APFS snapshot taken just before the run.
- **egress** — where it connected, read from the allowlist proxy's log. This only
  works for the **caged** variant (which routes through the proxy); a plain agent on
  a NAT network gets `egress: unobserved` — opossum says so rather than let a blank
  read as "no traffic".
- **resources** — not captured yet (marked unobserved).

Add `--audit-format json` for a machine-readable report (same shape as
`doctor --format json`) — the container's own stdout goes to stderr, so the JSON on
stdout stays clean for `| jq`.

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
