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
| How far it reaches | `networks:` | direct internet (default), or host-only + a proxy (caged) |
| Reaching the host | `${OPOSSUM_HOST_GATEWAY}` | the host proxy, for the caged variant |

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
directly. When you don't want that, the `agent-caged` service puts the workroom on
a **host-only** network (`internal: true`): it has no direct internet at all, so
its only way out is a proxy you run on the host, reached via
`${OPOSSUM_HOST_GATEWAY}`. Because the internet route is physically gone, the
allowlist the proxy enforces can't be bypassed — see the main README's
[Constraining egress (agent sandboxes)](../../README.md#constraining-egress-agent-sandboxes)
section for the full pattern.

```sh
# 1. run an allowlist forward proxy on the host, bound to 0.0.0.0:8080, that
#    permits only api.anthropic.com (and whatever else the task legitimately needs)
# 2. then:
opossum run --rm agent-caged \
  claude --dangerously-skip-permissions -p "…task…"
```

`agent-caged` is behind a `caged` profile, so a plain `opossum up` (or
`run agent`) never starts it and its internal network isn't created — you opt in
by naming it. Pair it with `cap_drop: [ALL]` if you want to stop the agent from
reconfiguring its own networking.

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
   network + a host proxy (allowlist) let you decide how far the agent reaches —
   see the caged variant above.

## Resources

The default guest is ~1 GiB RAM / 5 vCPU, which is tight once the agent runs
`npm install` + build + test. This example sets `mem_limit: 4g` / `cpus: 4`; bump
them for heavier repos.
