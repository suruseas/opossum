# `docker compose` and an `env_file` that does not resolve

The reference this repository holds itself to when it decides *where* an
unresolvable `env_file` becomes a failure. opossum raises it where a service's
environment is needed and stays quiet elsewhere (`internal/compose`,
`cmd/opossum`, `docs/compatibility.md`); the prose behind that says "measured on
Docker Compose v5.4.0", and this is the run it names, kept so anyone can take it
again.

**Last taken: 2026-09-02 / `Docker Compose version v5.4.0`.** No container is
started: every command here reads the project or the daemon's list.

Two shapes are taken, because the tests that cite this use the second: an
`env_file` that is **missing**, and one that **is there and cannot be expanded**
(`BOOM=${NOPE:?you must set NOPE}`). Every exit code below is the same for
both; only the message differs.

## Taking it again

```sh
mkdir /tmp/parity && cd /tmp/parity
cat > compose.yaml <<'YAML'
services:
  alpha:
    image: alpine
    command: ["true"]
  beta:
    image: alpine
    command: ["true"]
    env_file: gone.env
YAML
docker compose config --services; echo "exit=$?"
docker compose config;            echo "exit=$?"
docker compose config --profiles; echo "exit=$?"
docker compose ps;                echo "exit=$?"
docker compose ps --services;     echo "exit=$?"
docker compose logs;              echo "exit=$?"
```

`gone.env` is never created. For the other shape, create it and take the same
six again:

```sh
printf 'BOOM=${NOPE:?you must set NOPE}\n' > gone.env
```

For the gated tables, add `profiles: ["extra"]` to `beta` and take `config`,
`config --services`, `config --profiles`, `docker compose --profile extra
config`, `ps` and `logs`.

## An enabled service whose `env_file` is missing

| command | exit | what came back |
|---|---|---|
| `config --services` | 0 | `alpha` / `beta` |
| `config` | 1 | `env file /tmp/parity/gone.env not found: stat …: no such file or directory` |
| `config --profiles` | **1** | the same message |
| `ps` | 0 | the header row, no services running |
| `ps --services` | 0 | (nothing; none are running) |
| `logs` | 0 | (nothing) |

With `gone.env` present but unexpandable, the same six answer the same way; the
message on the two that fail is `failed to read …/gone.env: required variable
NOPE is missing a value: you must set NOPE`.

`config --services` and `config --profiles` differ: both print a list without
rendering a service, and only the first answers. That is docker's, not a
conclusion drawn from it — opossum's `config --services` answers, and it has no
`--profiles`.

## A service `profiles:` keeps out, whose `env_file` is missing

Same file with `profiles: ["extra"]` on `beta`:

| command | exit | what came back |
|---|---|---|
| `config` | 0 | the project with `alpha` only |
| `config --services` | 0 | `alpha` |
| `--profile extra config` | 1 | `env file … not found` |
| `config --profiles` | 0 | `extra` |
| `ps` | 0 | the header row |
| `logs` | 0 | (nothing) |

The unexpandable shape answers the same, with the message above. Note that
`config --profiles` answers here while it failed in the table before: what it
reads is the profiles of a service it does not have to render.

A service kept out of the run is not read for; naming its profile brings it in,
and then it is. opossum draws the same line (`cmd/opossum`:
`TestABrokenEnvFileOnAGatedServiceDoesNotBreakTheProject`,
`TestABrokenEnvFileReachesTheCommandsThatNeedTheEnvironment`).

## What this does not say

- Nothing about `up` or `run`. Both start containers, so taking them here would
  leave more behind than a reference is worth; opossum raises the failure for
  both, and that is its own decision.
- Nothing about *why* the two shapes agree. They are both taken here and the
  exit codes match, but that is one version's behaviour, not a rule docker
  states. opossum treats them as one thing — a caller cannot tell them apart —
  and that equivalence is opossum's own.
- Nothing about other versions. The claims this backs name v5.4.0; a different
  version is a different measurement, and this file says which one it is.
