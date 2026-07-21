# A disposable workroom for an autonomous coding agent: Claude Code running
# inside an Apple `container` VM, so the blast radius of anything it does is the
# VM — not your Mac. Built on node:22 (Claude Code ships as an npm package), with
# git for the agent's clone / commit / push workflows.
FROM node:22-bookworm-slim

# git for the agent's version-control work; ca-certificates for HTTPS to the API;
# curl both for the agent's own fetches and to sanity-check egress in the caged
# variant (it honours HTTPS_PROXY, so it proves the allowlist end to end).
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

# Claude Code, installed globally so it's on PATH for every user.
RUN npm install -g @anthropic-ai/claude-code

# Run as the image's built-in non-root `node` user. Claude Code refuses
# `--dangerously-skip-permissions` when it runs as root, so a non-root user is the
# clean way to let it run fully autonomously inside the already-isolating VM.
# /work is the bind-mount point (see compose.yaml), owned by node so the agent can
# write results back to the host.
RUN mkdir -p /work && chown node:node /work
USER node
WORKDIR /work

# Overridden by `opossum run --rm agent <cmd>`; the bare image just starts Claude.
CMD ["claude"]
