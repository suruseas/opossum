# Run your MCP servers on Apple container

Give an AI agent a tool server in its own VM, reachable by name.

MCP servers are exactly the shape Apple `container` is good at: small images,
several of them, ~99% idle, each isolated in its own VM — and third-party code
that holds your tokens, kept per-VM. If you're running a couple of them today,
you're probably keeping Docker Desktop (multiple GB of always-on RAM) alive just
for that. Apple `container` has no always-on base VM: an on-demand stdio server
costs memory only while it runs, and an HTTP server only while it's `up` (each
running server is its own ~250–400 MB VM, freed on `down`).

**Start with the raw command — opossum earns its place at a boundary, not before
it.** A single, secret-free, stdio MCP server needs no opossum:

```jsonc
// .mcp.json — a raw one-off, honestly the right tool here
{ "mcpServers": { "terraform": {
    "command": "container",
    "args": ["run", "-i", "--rm", "hashicorp/terraform-mcp-server"] } } }
```

Graduate to a compose file (see [`examples/mcp-stack`](../examples/mcp-stack)) when
you hit any of:

1. **a secret** — a token you don't want inline in a committed `.mcp.json`;
2. **several servers** — one file to `pull` / `config` / manage lifecycle;
3. **HTTP transport** — a long-running server you `up`/`down`/`ps`/`logs`.

**A token-bearing stdio server** — the token stays in `.env` (git-ignored), and
your `.mcp.json` just invokes opossum, which injects it:

```jsonc
// .mcp.json
{ "mcpServers": { "github": {
    "command": "opossum",
    "args": ["-f", "/path/to/mcp-stack/compose.yaml", "run", "--rm", "github"] } } }
```

**An HTTP (streamable) server** — `opossum up` it once, then point your client at
the URL (the server must bind `0.0.0.0`, e.g. `--transport-host 0.0.0.0`, so the
published port reaches it):

```sh
opossum -f mcp-stack/compose.yaml up          # starts the HTTP servers
```
```jsonc
// .mcp.json
{ "mcpServers": { "terraform-http": { "url": "http://localhost:8080/mcp" } } }
```

> **"Connected, but tool calls fail"?** stdio is transport-independent of the
> guest network, so a server can report *connected* while the container can't
> reach the internet — usually a **wedged default network** after long runtime
> uptime. Run `opossum doctor` (it probes exactly this) and, if flagged,
> `container system stop && container system start`.

[← back to the README](../README.md)
