# Constraining egress (agent sandboxes)

Run something you don't fully trust with no route to the internet.

When you run an untrusted workload — a coding agent, an LLM tool-runner, anything
that executes code it wrote — the question is what it can reach on the network.
There are two levels opossum lets you declare:

**Full isolation.** `network_mode: none` gives the container loopback only — no
egress at all, and no name resolution. Use it when the workload needs nothing off
-box:

```yaml
services:
  sandbox:
    image: my-agent
    network_mode: none
```

**Host-only, egress through a proxy you control.** Apple `container` has no
per-destination allowlist, but an **internal** network (`container network create
--internal`) removes the route to the internet while leaving the host reachable.
Put the agent on an internal network and it *physically cannot* reach the internet
directly — its only way out is a proxy you run on the host, reachable via
`${OPOSSUM_HOST_GATEWAY}`. Because the internet route is gone, the allowlist is
**enforced**, not merely advised (the agent can't bypass the proxy by dialing a
destination itself):

```yaml
networks:
  caged:
    internal: true          # host-only: no internet egress
services:
  agent:
    image: my-agent
    networks: [caged]
    environment:
      # the only route out — a host allowlist proxy at :8080
      HTTPS_PROXY: http://${OPOSSUM_HOST_GATEWAY}:8080
      HTTP_PROXY: http://${OPOSSUM_HOST_GATEWAY}:8080
```

Run an allowlist proxy (e.g. a filtering forward proxy) on the host bound to
`0.0.0.0:8080`, and only the destinations it permits get through. Pair this with
`cap_drop: [ALL]` and a non-root `user:` to keep the workload from reconfiguring
its own networking.

Two things to know about internal networks:

- **No name resolution.** The DNS resolver sits on the network gateway, which an
  internal network can't route to — so peers can't resolve each other by service
  name. Reach the host proxy by `${OPOSSUM_HOST_GATEWAY}` (an IP), and address any
  in-network peer by IP.
- **Changing `internal:` on an existing network needs a `down` first.** opossum
  doesn't reconfigure a network that already exists; `opossum down` then `up`
  recreates it with the new setting.

[← back to the README](../README.md)
