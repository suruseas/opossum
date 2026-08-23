# `container image inspect` fixtures

Real output from `container image inspect <ref>` (container CLI 1.2.2, 2026-08-23),
used to test what opossum reads back from an image rather than assumes about it.
Each file keeps the whole document except `history` and `rootfs`, which say nothing
about the environment and are long.

| file | image | why it is here |
|---|---|---|
| `postgres17.json` | `postgres:17-alpine` | declares `PGDATA=/var/lib/postgresql/data` — the path opossum used to hold as a constant |
| `postgres18.json` | `postgres:18-alpine` | declares `PGDATA=/var/lib/postgresql/18/docker` — the move that made the constant wrong |
| `postgres-pgdata-below-datadir.json` | built here: `FROM postgres:17-alpine` with `ENV PGDATA=/var/lib/postgresql/data/pgdata`, tagged `postgres:b480-below` | an image that initialises *below* the mount it is handed. What it then does is measured in `../error-wordings/pg-image-declares-pgdata-below-the-mount.txt` |
| `postgres-declares-no-pgdata.json` | built here: `FROM alpine`, tagged `postgres:b480-nodecl` | a Postgres-named image that declares no `PGDATA` at all — "told us nothing" has to read differently from "could not be asked" |
| `postgres-pgdata-with-trailing-slash.json` | built here: `FROM postgres:17-alpine` with `ENV PGDATA=/var/lib/postgresql/data/`, tagged `postgres:b480-slash` | the same directory written with a slash on the end — reading it as another one would leave a mount unfixed |
| `postgres-major-not-a-number.json` | built here: `FROM alpine` with `ENV PG_MAJOR=9.6` and a `PGDATA` elsewhere, tagged `postgres:b480-96` | a version that is real (9.6 shipped) but not a number to compare. Kept for the shape; nothing reads `PG_MAJOR` any more, since the note no longer says what a version means |
| `postgres-cluster-outside-the-usual-tree.json` | built here: `FROM alpine` with `ENV PG_MAJOR=18` and `PGDATA=/home/postgres/pgdata/data`, tagged `postgres:b480-elsewhere` | an image whose cluster is not under `/var/lib/postgresql` at all — `[OPSM-110]` names that directory and nothing else, so it is not the advice for this one |

The built images were deleted after their output was captured; the Dockerfiles are
one line each and are quoted above.
