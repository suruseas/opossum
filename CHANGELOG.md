# Changelog

All notable changes to opossum are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and this project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.24.8] - 2026-09-08

### Changed

- A key opossum does not read in a long-form list item — a port's `mode`
  or `name`, a bind mount's `bind` options, a secret's `uid`, an env file's
  `format` — or in a dependency's mapping (`restart`, `required`) is now
  named among the ignored fields by entry number or dependency name
  (`ports entry 1.mode`, `depends_on.db.restart`). Before, each was dropped
  in silence.
- A long-form mount, port, secret or env file entry that leaves out the
  key it cannot do without (`target`, `source`, `path`) is refused naming
  the entry by number and saying what to write, the way other entry
  refusals do — \`ports entry 2 of 2 has no target — write the container
  port, as in `target: 80`\` — where it used to say only "port entry is
  missing a target". The same key written with nothing after it is
  refused as bare.
- A key opossum does not read under `build`, `healthcheck`, `deploy` or
  `develop` is now named in full among the ignored fields — `build.labels`,
  `healthcheck.start_interval`, `deploy.replicas`,
  `deploy.resources.limits.pids` — the way a watch rule's extra key already
  was. Before, a key under `build`, `healthcheck` or `develop` was dropped
  in silence, and anything extra under `deploy` was reported only as
  `deploy`. (docker compose refuses a key it does not know; opossum keeps
  loading the file and says what it left out.)
- `opossum config` now shows a bare variable name (`environment: [A]`,
  `build.args: [A]`, or `A:` with nothing after it) with the shell's
  value, the way docker compose shows it (`A: x`; opossum's list form is
  `- A=x`): the shell's value when it has one, `A=` when it is empty. Left unset by the shell, the name stays bare under
  `environment` (the runtime is still told it) and is left out of
  `build.args` (what `up --build` passes). Before, every bare name was
  shown as written, which was not what `up` and `build` passed.

### Fixed

- With several `-f` files, each file is now checked on its own before the
  merge, the way docker compose validates them: a mistake in one file is
  refused naming that file, even when a later file writes over it, and a
  shape the merge used to absorb — a network listed twice in one file, a
  service written with nothing under it in one file — is refused too. A key
  a later file writes with nothing after it is still "not given" and keeps
  the earlier file's value.
- A service's `ports` are checked at load the way docker compose validates
  them, and a bad one is refused with its entry number and what to write:
  a container port that is not a number from 1 to 65535 (a word, `80.5`,
  `0`, `65536`, a padded or hex number), a host port above 65535, a range
  written high-low, a host address that is not an IP (`localhost:80:80`,
  a fourth `:` part), a protocol other than tcp, udp or sctp, a container
  range without a host range of the same length, and a spec with no
  container port (`80:`). Before, each reached `container run -p` as
  written — `ports: [a]` was passed as `a:a`. In the long form each key
  is checked by its own name, so `target: 0`, `published: a` or
  `protocol: foo` is refused there too (docker compose lets those through
  and fails at `up`).
- A build arg written as a bare `NAME` (`args: [A]`, or `A:` with nothing
  after it) now takes the shell's value, and is left out when the shell
  has none, as docker compose passes it. Before, it reached
  `container build --build-arg A` as written, and the builder gave the
  Dockerfile an empty `A` — even over an `ARG A=default` (measured with
  `container` 1.3.1).
- `build.args` written as a list (`[A=1, B]`) is now taken, as docker
  compose takes it; before, only the mapping form loaded and the list was
  refused as the wrong shape. A bad item there (`[42]`) or a single value
  (`args: foo`) is still refused, and the message now names `build.args`
  rather than `environment`. Across several `-f` files the two forms merge
  by variable, as `environment` does: a list in one file and a mapping in
  the next keep every variable, the later file winning by name.
- A variable written with a list or a mapping as its value in the mapping
  form of `environment` or `build.args` (`A: [1]`, `A: {b: c}`), or with
  an infinity or a NaN, is now refused naming the variable, as docker
  compose refuses it. Before, the value reached the container as Go wrote
  it out — `A=[1]`, `A=map[b:c]`, `A=+Inf`.
- `labels` written as one value (`labels: x`), with a list item that is not a string, or with a value that is a list or a mapping (`labels: {a: [1]}`) is now refused at load the way docker compose refuses it, naming what to write instead, rather than being read past and listed among the ignored fields. An empty `env_file` item (`- ` alone) is refused the same way, where before it was read as an empty path and failed opening the project directory.
- A resource limit written as a list, a mapping or a blank — `cpus: [1]`,
  `mem_limit: {}`, `cpus: ""`, `deploy.resources.limits.memory: ""` (or
  what an unset `${VAR}` leaves) — is now refused, naming the field and
  what to write, as docker compose refuses it. Before, each was read as no
  limit, in silence: `config` showed nothing and the container ran
  unlimited.
- A mount written in the long form without its `type` (`- {source: ./a, target: /x}`, or `type: ""`) is now refused at load the way docker compose refuses it, naming the entry and the three types to choose from, rather than being passed on with the kind of mount left for the runtime to guess.
- With several `-f` files, a service's `build` now merges as one mapping
  whichever form each file used, as docker compose merges it: a path
  (`build: ./other`) written over a mapping changes only the context, and a
  mapping written over a path keeps the path as its context. Before, the
  later file replaced the earlier value whole — a path dropped the
  dockerfile, args and target in silence, and a mapping dropped the path.
- A key written with nothing after it in a long-form mount (`source:`,
  `type:`), a port (`published:`), a dependency (`condition:`), a network
  or volume declaration (`internal:`, `name:`) or a secret (`file:`) is now
  refused naming the entry and its line, as docker compose refuses it; before, it was
  read as left out — a mount without a source, a port without a host port,
  a dependency merely started. With several `-f` files, a bare top-level
  `services:` in any file is refused too, and a bare key a later file
  writes that no earlier file gave a value to (a new network's
  `internal:`, a `build.context:` no base has) is refused naming that file,
  as docker compose does — where the same key over an earlier value still
  keeps it.
- A bind mount written in the long form without its `source` (`- {type: bind, target: /x}`) is now refused at load the way docker compose refuses it, naming the entry and what to write, rather than being started as an anonymous volume — a different kind of mount from the one written.
- A variable whose name YAML reads as a number, a boolean or null (`environment: {1: a}`, `{~: a}`, `"": a`, and the same under `build.args` and `labels`, a mapping brought in by `<<:` included) is now refused at load the way docker compose refuses it, naming the line or how to quote it, rather than becoming a variable named `1` or an empty name. An `env_file` that is the empty string (`env_file: ""`, or a `${VAR}` that expands to nothing) is refused too, where before it was read as an empty path and failed opening the project directory.
- A boolean written as the quoted word (`read_only: "true"`, `init: 'True'`, a mount's `read_only: "true"`, an env file's `required: "false"`, a network's `internal: "true"`) is now read as the boolean, the way docker compose reads it, instead of being refused as a string; `"1"`, `"t"` or `""` is still refused, as docker compose refuses it, and a declaration's `external` now refuses those too rather than reading `"1"` as true.
- A top-level network, volume or secret declaration whose `name`, `file` or `driver` is written as a number, a boolean, a date, a list, a mapping or nothing (`name: 42`, `driver: [a]`, `driver:` alone) is now refused at load the way docker compose refuses it, naming the key and the line, rather than read as the name `42` or passed over. A key in a secret's declaration that opossum does not read is now listed among the ignored fields, as a network's or a volume's already was.
- What a `${VAR}` reference expands to is now read as text, the way docker compose reads it: `TAG=42` under `image: alpine:${TAG}` is the tag `42` rather than a number refused where a string belongs, `V=[1]` under `environment:` is the value `[1]` rather than a list, `F=1.50` stays `1.50`, and a boolean field takes `RO=true` as the word. A reference written inside an anchor or alias name (`&${NAME}`) is no longer read, as docker compose does not read it either.
- A `.env` or `env_file` value now ends where docker compose ends it: an unquoted value at the first ` #` (a space, then a hash — `KEY=with # hash` is `with`; `a#b` stays whole), and a quoted value at its closing quote, with a comment after it dropped (`KEY="q" # note` is `q`). Before this the whole rest of the line was the value, quotes included.

## [0.24.7] - 2026-09-06

### Changed

- When `up` fails partway, the output now says which services it rolled
  back (stopped and removed), so a service that was reported as starting a
  moment earlier is not read as still running.
- The comment `up --from-docker-compose` writes on the `PGDATA` entry
  (OPSM-101) now says what the entry is for with the image at hand. For an
  image that keeps its cluster somewhere other than the mount — Postgres 18
  keeps it under `/var/lib/postgresql/18/docker` — it says the entry is what
  makes the server use the volume at all: without it the volume stays unused,
  and the Postgres 18 images refuse to start on finding data at the old path.
  Before, every image got the same "belt and braces" wording, which invited
  deleting the one line an 18 database needs. When the image is not here to
  be asked, the comment says so and keeps both readings.
- The note `up --from-docker-compose` writes for a service that mounts a host
  device or session socket (`/dev/...`, an X11 or PulseAudio socket) no longer
  reads `What to expect: expect ...`; the line now says what will happen: this
  service's device-dependent features will not work. Same meaning, one word
  fewer to read twice.

### Fixed

- A service that uses `extends:` is now refused with a message that names
  it and the service it extends, and says what to do (copy that service's
  settings in and remove `extends:`). Before, a service without an image of
  its own failed as `must set either image or build`, pointing at a typo
  that was not there, and one with its own `image:` loaded and ran without
  the settings it extended — `config` listed `extends` among the ignored
  fields, but not what it would have brought in. That second file no
  longer loads.
- An item of `volumes:`, `networks:`, `secrets:`, `env_file:` or
  `environment:` that YAML reads as a number, a boolean or a date (`- 42`,
  `- true`, `- 2024-01-01`) is now refused, naming the entry and how to
  quote it, the way docker compose validates it; before, it became a mount,
  a network, a secret, an env file or a variable named after the number —
  and, with several `-f` files, a bad `environment:` item fell out of the
  merge unseen. A bare `- ` under `environment:` is refused too instead of
  being dropped. `ports:` keeps taking bare numbers.
- A service whose `networks:` names the same network twice — in the list
  form, through a YAML alias, or as a repeated key in the map form — is now
  refused, naming the two entries, the way docker compose validates it;
  before, the container was started with the `--network` flag repeated. A
  bare `- ` in the list is refused too instead of being dropped. The check
  applies when one file writes the service's `networks:`; when several
  `-f` files do, they are joined by name first and a repeat inside one of
  them is absorbed by that join.
- With several `-f` files, a variable named `environment` or `labels` inside
  a service's `environment:` (map or list form) or `build.args` (map form)
  now merges like any other variable — the later file's value wins. Before,
  when both files set it, the value came out as an empty mapping
  (`environment=map[]`).
- A service field written with nothing after it (`volumes:` alone, or
  `ports:`, `networks:`, `environment:`, `depends_on:`, `build:`, …) is now
  refused, naming the field and what it takes, the way docker compose
  validates it; `command:` and `entrypoint:` alone still mean "no command".
  Before, the field loaded as if the key were absent — and a bare
  `external:` on a volume or network declaration read as "not external",
  so the volume was created as the project's own instead of being used as
  the pre-existing one. That is refused too.
- A service key with nothing under it (`services: {web: }`) is now refused
  with `service "web" must be a mapping`, the way docker compose reads it.
  Before, `config` and `up` crashed on it. An empty item in `volumes:` or
  `ports:` (a bare `- `, a `- null`, a quoted `""`, or a `${...}` that
  expanded to nothing) is refused too, naming the entry; before, it reached
  the runtime as an anonymous volume with an empty target, an empty
  `-p`, or (after a multi-file merge) a mount or port named `null`.
- `build.context:`, `build.dockerfile:`, `build.target:`, `build.args:` and
  the `deploy.resources` limits and reservations (the mappings, `memory`
  with a number, `cpus` with a boolean) are now refused when written with
  nothing after the key or with a value of the wrong kind, the way docker
  compose validates them; before, `build.context: 42` built from a context
  named "42" and a bare `limits:` silently set no limit. A bare number for
  `build:` itself is refused too.
- `healthcheck` fields written with nothing after the key (`test`,
  `interval`, `timeout`, `start_period`, `retries`) are now refused the way
  docker compose validates them, and so is a bare `develop.watch:`.
- A watch rule that is empty (a bare `- `), or whose `path`, `action` or
  `target` is bare or not a string, is refused too; before, an empty rule
  watched the whole project. `deploy.resources` `memory` and `cpus` (limits
  and reservations) written as a list or a mapping are refused instead of
  silently dropping the limit.
- A number, a boolean or a date written where `image:`, `user:`,
  `working_dir:`, `platform:` or `network_mode:` expect a string is now
  refused, naming the field and how to quote it; before, it was read as
  text (`image: 42` pulled an image named "42"). `cap_add:` and `cap_drop:`
  written as one bare value are refused too (docker compose takes only a
  list there), and so is a bare number for `deploy.resources.limits.memory:`
  (it takes a string with a unit, as in `"512m"`).
- A number or a boolean written where `profiles:`, `cap_add:`, `cap_drop:`,
  `tmpfs:`, `command:`, `entrypoint:` or `healthcheck.test:` expect a string
  (`profiles: [42]`) is now refused, naming the entry and how to quote it,
  the way docker compose validates it; a date is refused too, and an item
  with nothing in it. Before, a number became a name — and a numeric
  profile, never active, made the service disappear from `config` without a
  word. `expose:` keeps taking bare numbers.
- A watch rule's `action` is checked at load the way docker compose checks
  it: only `sync`, `rebuild`, `sync+restart`, `restart` and `sync+exec` are
  taken (a typo, `SYNC` or `""` is refused, naming the rule but not the word), and the three
  that copy files are refused without a `target` (or with an empty one).
  Before, a misspelt action reached `opossum watch`, which named it as not
  automated at the first change, and `sync` without a target copied files
  to the container's root. `restart` and `sync+exec` load (docker compose
  takes them) but are not automated yet, as before.
- A blank `healthcheck.interval`, `timeout` or `start_period` (`""`, or
  what an unset `${VAR}` leaves) is now refused as not a duration, as docker
  compose refuses it; before, a blank was read as the default silently.
- A watch rule under `develop.watch` is read closer to how docker compose
  reads it: `ignore:` takes one glob as well as a list, a rule without a
  `path` (or with an empty one) is refused instead of watching the whole
  project, a non-string `ignore` item is refused, and a key a rule does
  not have is listed with the ignored fields (docker compose refuses it).
- `healthcheck.retries: "3"` (a quoted whole number) is now taken, as docker
  compose takes it. A word, a blank, a boolean or a negative number there
  is refused; a negative used to be read as the default silently.
- A YAML alias (`*name`) that stands for a string, used as an item of
  `volumes:`, `ports:`, `secrets:` or `env_file:`, is now read as that
  string. Before, the load failed with a type error, so the usual `x-`
  anchor reuse worked for whole fields and for mapping items but not for a
  string item. `networks: *nets` also reports its ignored per-network
  fields (aliases, addresses) the way the plainly written form does.
- `external:` on a top-level volume or network can now be written as a YAML
  alias (`external: *shared`, standing for `true` or for a mapping with
  `name:`), and the mapping's `name` key may itself come through an alias
  or a `<<:` merge key. Before, the aliased forms were refused as a value
  of the wrong shape or as an unknown key. (An external secret is still
  refused as unsupported, aliased or not.)

## [0.24.6] - 2026-09-05

### Fixed

- `up --from-docker-compose` no longer writes overlay entries — or notes —
  about services gated behind a profile that is not enabled in this run.
  Those services are not started and `config` does not show them (docker
  compose leaves them out the same way), so an entry about one asked the
  reader to judge whether a change to something that was not running was
  theirs to care about. The plan now follows the run: a service is looked at
  when its profile is enabled (`--profile`, `COMPOSE_PROFILES`) or when it is
  named on the command line, and the services left out are named up front. A
  later run with the profile enabled finds the overlay in place and reports
  what those services would need.
- With several `-f` files, a service's `networks:` now merges across the two
  spellings the way docker compose reads them: a file that lists `[back]` over
  one that wrote `back: {aliases: [...]}` keeps the map's entries (so the
  per-field report still names the aliases opossum does not act on), and a
  network both files name is joined once, not passed as two `--network` flags.
  Before, the list form replaced the map wholesale at merge time, so the
  aliases disappeared before anything could report them.
- `opossum stats` (and `stats --host`) now ask the runtime only for the
  containers that exist. Apple `container` 1.3.1 fails the whole `stats` call
  when any name it is handed does not exist, so a project with one service
  that was never started showed nothing for the others; on 1.2.2 the missing
  name was skipped. When no service has a container at all, `stats` now says
  so (and points at `opossum up`) instead of handing the runtime an empty list.
  Also, `doctor`'s storage note named `container images ls`,
  a command 1.3.1 no longer has — it says `container image ls` now.
- With several `-f` files, a key an override writes with nothing after it
  (`working_dir:`, `ports:`, `environment:`, a network's `internal:`, a
  secret's `file:`) now leaves the earlier file's value in place, the way
  docker compose reads it. Before, the empty key won: the value was cleared,
  and a secret that lost its `file:` this way failed the load. `command:` and
  `entrypoint:` still clear the command, and `A:` inside `environment:` or
  `build.args` still means "take it from the shell" — both as docker compose
  does.

## [0.24.5] - 2026-09-04

### Fixed

- When `up --from-docker-compose` cannot write `compose.opossum.yaml` — a
  directory it has no permission to create files in, say — it now prints the
  overlay's text after the headlines, indented, minus the header that
  introduces the file to a reader of the file: the YAML it would have applied
  and the `Why:` (and, for notes, `What to expect:`) beside every entry, as they would have been
  written, so the file can be put in place by hand. It used to promise "what it
  would have contained" and then show one headline per entry, leaving the
  reasons nowhere. (The road where no overlay is written because it would only
  hold notes already printed its prose.)
- `up --from-docker-compose -f <file>` no longer tells you to "re-run without
  -f" to have the overlay written. That second run reads the compose file it
  discovers on its own — with its override file — rather than the file `-f`
  named, and may write nothing at all (an overlay already there, a file name
  discovery does not look for, `--dry-run` again, a directory it cannot write
  to) or something else. Instead the run prints the overlay's text after the
  headlines — the YAML and the `Why:` beside every entry, as it would have been
  written — and says how to use it: write it as `compose.opossum.yaml` next to
  the file `-f` named and pass both, `-f <file> -f compose.opossum.yaml`. A
  note's prose shown this way is not repeated as a warning by the `up` that
  follows.
- The older map form of `external` — `external: {name: x}` under a volume or a
  network — is now read the way docker compose reads it (as `external: true`
  with that name), instead of failing the load with a type error about
  unmarshalling a map into a bool. For a secret it means what it says, an
  external secret, and is refused as one.
- Network settings opossum reads past are now named when they are dropped,
  the way volume fields already were: `ipam`, `driver` and the like under a
  top-level `networks:` declaration, and `aliases` / `ipv4_address` under a
  service's map-form `networks:` entry, appear in `opossum config`'s ignored
  list and in `--verbose` warnings as `networks.<net>.<key>`. An alias that
  never resolved used to be the only sign it had been ignored.

## [0.24.4] - 2026-09-02

### Fixed

- A value with a line break in it — a multi-line PEM key in `.env`, say — no
  longer breaks the compose file that references it. Interpolation runs on the
  raw text, so the value's second line used to land in the document as YAML
  structure and the whole load failed with a syntax error; the value is now
  held aside during parsing and restored whole afterwards, so `${PEM}` yields
  the full multi-line string exactly as docker compose does (measured on
  Docker Compose v5.4.0). One visible shift: a reference written inside a
  double-quoted scalar used to survive with the value's line breaks folded to
  spaces — those now arrive as real line breaks too, which is what docker
  hands over.
- A line break inside a compose value no longer breaks what opossum writes or
  says about it. The applied/suggestion summaries printed on screen kept the
  rest of such a value on opossum's own line (it used to continue at column
  zero, where it read as opossum's words), and the overlay's comment blocks
  flatten it too — previously one multi-line value made the generated
  `compose.opossum.yaml` fail its own validity check, which cost every fix
  from that run, announced only as a YAML error on a file you never wrote.
  Notes were already flattened; now all three entry kinds are.
- The audit summary and the workspace-snapshot listing no longer let quoted
  values write lines of their own. A file path or destination reported by
  `opossum run --audit`, and a snapshot directory name listed by
  `opossum ws ls`, used to carry a line break onto the screen at the column
  where opossum's own findings start; both are flattened now, and
  `opossum ws snapshot` refuses names containing control characters outright.
  A snapshot that already carries such a name can no longer be addressed by
  name (`ws rm`, `ws rollback`, `ws diff` refuse it); its data is untouched,
  and `opossum ws prune --all` — or deleting its directory under
  `.opossum-snapshots/` by hand — still cleans it up.
- The Docker-socket warning now asks the same question as the migration note:
  does either end of the mount name `docker.sock`? It used to match the
  substring anywhere in the line, which warned people whose mounts carry no
  Docker socket at all — a directory like `docker.sock.d/`, a socket named
  `my-docker.sock`, and the anonymous form `- /var/run/docker.sock`, which
  mounts nothing from the host under Docker either (measured on Docker
  Compose v5.4.0: it canonicalizes to a plain anonymous volume), so there was
  no divergence to warn about.
- The formula-to-cask migration steps in the README ran the uninstall last,
  which could leave you with no `opossum` at all: the cask refuses to
  overwrite what already sits at `/opt/homebrew/bin/opossum` — the formula's
  own link included — so its install step could place nothing, and the
  uninstall then removed the only binary. The steps now uninstall the formula
  first (with a note about the brief gap), and the README gains a recovery
  section for anyone who followed the old order: `brew trust --cask` then
  `brew reinstall --cask suruseas/opossum/opossum` brings opossum back.
- `make test` no longer reports another run's temporary directories as leftovers from this one. The check compares `$TMPDIR` before and after, and a suite started in a second terminal appears in that difference exactly as a leak does; it said so, but left the reader with no way to tell which they were looking at. The names carry the pid of the process that made them — the sweep that reclaims them has always read it — so the report reads it too, and lists a directory whose maker is still alive separately, without offering to remove it. Anything whose maker has gone, and anything whose name has no pid to read, is reported as before.
- Warnings that embed a failure — watch's rebuild, restart, sync and setup
  errors, the volume-seeding warnings, the supervisor's stop-marker warning —
  now quote it the way the CLI's error output does: continuation lines are
  indented, and a line break inside a quoted value — a bind path, a service
  name — can no longer start a line of its own at the column where opossum's
  messages begin.

## [0.24.3] - 2026-09-01

### Fixed

- The advice printed when a bind mount's host source cannot be created — and its two siblings, the dangling-symlink refusal and the note left when a directory has to stand in for a file — now tells you to rerun the command you actually typed. Someone who typed `opossum run` used to be told to run `opossum up` again, one line after a `mkdir -p` that was worth copying exactly.
- A service you are not running no longer breaks the whole project. `env_file:`
  entries were read while the compose file was being loaded, which happens
  before profiles are applied, so a `profiles:`-gated service pointing at a file
  that was missing or that failed to expand made `config`, `up` and `ps` all
  fail — for a service none of them would have started. The failure is now
  raised where a service's environment is actually needed: `config` and `up`
  report it for the services they render and start, as does `run`, while `ps`
  and `logs`, which need no environment, no longer carry it at all. That is
  where docker compose draws it too, measured on Docker Compose v5.4.0:
  `config --services`, `ps` and `logs` all succeed against a service whose env
  file does not expand, while a full `config` of that service fails. Its `up`
  and `run` were not measured.
- `opossum up --from-docker-compose` no longer says the same thing twice about a Docker socket mount. The overlay's note and the up's own warning carried the same paragraph a screen apart, reading as two findings; when the note's prose has actually been shown in a run, the warning now stays quiet. Everywhere else it still speaks — when only the note's one-line headline was printed (an overlay already on disk, `-f` alongside real changes), and for mounts only the warning recognises, like an anonymous volume or a volume merely named after the socket.
- Build-failure hints (out of disk, builder out of resources, corrupted builder
  cache) now end by telling you to rerun the command you actually typed: a failed
  `opossum run` or `opossum build` no longer advises `opossum up`.

## [0.24.2] - 2026-09-01

### Changed

- The Homebrew package is now published as a cask rather than a formula. `brew install suruseas/opossum/opossum` works as before and still pulls in Apple's `container` runtime. An existing formula install hears about the move on its next `brew update`, but does not cross on its own: Homebrew wants a migration into a third-party tap trusted first, and stops at a warning. Take the order from the README's install section, which uninstalls the formula before installing the cask — not the order Homebrew prints in that warning. Installing the cask while the formula is still linked does not fail: Homebrew sees the formula's own symlink in the binary's place, skips the link, and still reports the cask installed, so uninstalling the formula afterwards takes away the only `opossum` on your `PATH`. If that has already happened, `brew reinstall --cask suruseas/opossum/opossum` puts it back. The cask clears macOS's quarantine attribute on install, so the unsigned binary runs without a Gatekeeper detour. This follows Homebrew's direction for pre-compiled binaries — the tooling that generated the old formula shape is being retired.

## [0.24.1] - 2026-08-28

### Changed

- The note for a mounted host device or session socket (`OPSM-106`) no longer says a session socket is unreachable. A device node still cannot be handed to a per-container VM and arrives as a path with nothing behind it. For a session socket the note now says what is known: it is mounted as a path, what would answer on it is the host's own session, and whether anything useful comes through has not been measured here.
- The line `opossum up --from-docker-compose` prints above its notes now says what is true of all of them — `opossum writes no YAML for` these, which is why there is nothing to uncomment — instead of claiming no compose change could fix them. That was true of some notes and false of others: a Docker socket mount has a way out, and the note has said so since the wording was corrected.

### Fixed

- The note `opossum up --from-docker-compose` writes for a Docker socket mount now looks at the file name rather than anywhere in the path, so `docker.sock.d/S.gpg-agent` and `my-docker.sock` no longer collect one.
- What `opossum` says about a `docker.sock` mount no longer claims the mount cannot help. Where that path is a symlink something put it there, and a container started here was measured reaching a Docker daemon through what the link points at on 2026-08-27 — though which of them put it there, and whether anything is listening, varies. The refusal for a symlinked socket now offers the same way out whoever owns the socket, and the note and the warning say the thing that is actually worth knowing: the daemon answering there is not the one running these containers, so a tool mounting this socket to watch its neighbours is given a different set if one answers at all. The earlier wording came from reading rather than measuring, and was wrong in exactly the situation that produces it.

## [0.24.0] - 2026-08-26

### Added

- `opossum doctor` now reports the networks nothing is running on. Apple `container` keeps a `container-network-vmnet` process resident for every network that exists, so a project whose `down` never ran goes on costing memory until someone removes it — and across many short sessions those accumulate unseen. The check separates the networks with no containers at all from the ones a stopped container still names, since the second may be a project you mean to start again; it offers to remove only the first kind, and removes nothing on its own. It does not claim to know who a network belongs to: one created by `opossum up`, one declared `external: true`, and one you made by hand look alike from here. The `--format json` report carries it as `leftover-networks`. The report's name column is now as wide as its widest name, so every check's detail still lines up.

### Fixed

- The lines opossum prints while working on a project now stay on the line they started on, whatever the compose file put in a service name, an image reference, a path, or a volume. A value containing a newline used to end its line early and continue at column zero — where opossum's own messages start — so a compose file could print a sentence that read as opossum reporting something it had never done.
- A failure now stays on the lines opossum gave it. An error message is built from a format opossum wrote and values a project supplied — a service name, a path, the runtime's own output — and a newline in one of those used to end the line and continue at column zero, where the message itself starts, so a compose file could put a sentence there and have it read as opossum reporting something. Messages that are deliberately more than one line keep their continuations; the two that used to begin a line at the margin — the advice under a host-port conflict and the hint after a failed build — now begin two spaces in, along with anything quoted from the runtime.
- `ps`, `images`, and `stats` now print one row per service, whatever the compose file called them. A service name containing a newline used to split its own row: the column being filled was lost, the rest of the name started at column zero — where opossum's own messages start — and every row below it stopped lining up. A tab did the same quietly, by opening a column of its own. The list of workspace snapshots is printed another way and is not covered yet.
- The last log lines of a crashed container are now quoted with their control characters replaced by spaces. A carriage return in a container's output moved the cursor back to the start of the line the block was printing on, and an escape sequence could move it further — up into what opossum had just written about the crash — so a log line could appear where opossum's own messages begin and be read as opossum reporting something it had never done. The cost is that a log indented with tabs loses that indentation, and colour codes are shown as the characters they are.

## [0.23.1] - 2026-08-26

### Fixed

- A project where everything opossum found is something it cannot fix now reads
  the same as one where it fixed something. Those findings — a mounted Docker
  socket, a host device, a Postgres cluster the image keeps somewhere the mount
  does not cover — are written into `compose.opossum.yaml` with the whole of what
  they mean: what will happen, and what to do instead. But that file is not
  written when such findings are all there is, since it is never overwritten once
  it exists and a comment-only one would use up that single chance. So the body
  went nowhere and a one-line summary was all that arrived, while the same
  finding in a project that also needed a real change was explained in full. When
  the findings are all of that kind, the file is still not written and what it
  would have said is now printed instead. (An overlay left unwritten for some other reason —
  one is already there, `-f` named the compose file, the run is a dry run — still
  reports only the summaries.)
- Running `up --from-docker-compose` with an explicit `-f` does not write an
  overlay, and opossum said so by sending you back for a second run without the
  flag — even where everything it found was something no compose change can fix.
  An overlay is never written for those alone, so the run you were sent back for
  had nothing of theirs to write down. Opossum now says that instead, and reads
  the findings out in full: what will happen, and what to do about it. What a
  second run would do with the rest of your project is not something opossum can
  tell from behind `-f`, and it says nothing about that here: with the flag it
  reads the files you named, and without it whatever discovery turns up, override
  files included.
- A Redis container that dies taking ownership of its data directory is now
  helped in the shape Redis is usually written in — a bind mount for the data and
  another for the config, or a bind for the data beside a volume for something
  else. What it prints is `chown: .: Operation not permitted`, which names a
  directory without saying where it is, so opossum could only work out which mount
  had died when the service had exactly one to choose from; with anything beside
  it, the crash report had to say it could not tell. The image knows where `.` is
  and now gets asked, the same way it is asked where a database keeps its data.
  `redis:7-alpine` declares `/data`, so the data mount is named and the swap is
  offered for that mount alone; `redis/redis-stack-server` declares no working
  directory, and there the report says it could not tell, as before. A
  `working_dir:` in the compose file wins over the image, since that is what the
  runtime is given. Asking can only add: where the answer lands nowhere useful —
  a relative `working_dir:`, an image that declares `/`, a directory nobody
  mounted — the report says exactly what it said before. The suggestion this
  writes says it is not a guess, and for a mount named this way that rests on one
  step more than it used to: the container said `.`, and the image says where `.`
  is. That holds unless something moved the process in between, which nothing
  here checks.

## [0.23.0] - 2026-08-23

### Added

- `up` now explains Postgres 18's refusal to start when the mount sits at the
  data directory 17 and earlier used. Postgres 18 keeps the cluster in a
  major-version subdirectory, so the image wants a single mount at
  `/var/lib/postgresql`; a mount at `/var/lib/postgresql/data` is data it will
  not use, and it exits saying so. The crash report now names the fix
  (`[OPSM-110]`): move the mount up one level, since the image creates the
  subdirectory itself — a host path works there, measured on Postgres 18. Data
  an earlier major version wrote is a separate question: the image asks for
  `pg_upgrade`, and moving the mount does not do that — 18 refuses a cluster an
  earlier version wrote even at the path it asks for.

### Changed

- `up --from-docker-compose` no longer turns a Redis, Valkey or Redis Stack bind
  mount into a named volume on sight. It did that because an image that takes
  ownership of its data directory cannot do so on a bind mount, but the images
  disagree about whether they try. Run on container 1.2.2 and recorded in
  `redis-family-chown-split.txt`: `redis:7-alpine` exits with `chown: .: Operation
  not permitted`, `redis:8-alpine` starts and writes to the host directory, and
  `valkey/valkey:8-alpine` ships the same entrypoint as Valkey 7 and exits the way
  redis 7 does. `redis-stack-server`, which was read rather than run, has no chown
  in its entrypoint at all. Nothing in the image says which of those an image is.
  Rewriting on the name meant a project that works today had its host directory
  swapped for a volume it cannot open, and the output read like success. Now the
  mount is left as written. If the container does die that way, opossum records
  which mount it died on where it can, and the next `up --from-docker-compose`
  says what it does about that mount — writing into `compose.opossum.yaml` when
  there isn't one yet, and on screen when there is, since an overlay that is
  already there is never overwritten. Where the mount cannot be told apart from
  the ones that are working, nothing is recorded and the crash report says so
  instead of naming a next step that would do nothing. Postgres, MySQL/MariaDB,
  ClickHouse and MongoDB, whose images take ownership of their data directory at
  startup, are unchanged.

  If an earlier version already moved one of these mounts for you, the
  `compose.opossum.yaml` it wrote still holds the swap and nothing changes. Delete
  that file, though — which is how you opt out of it — and this version will not
  write the swap again: the service comes back on the host directory, which is
  empty, and the data stays behind in the named volume the earlier version made
  (`<project>_<service>-data`). On Redis 7 and Valkey the container will not start
  at all, so this is hard to miss; on Redis 8 it starts and answers as though the
  data were gone. Both were watched on container 1.2.2. To get it back, put the
  mount back the way the overlay had it, or copy what is in that volume into the
  host directory.
- `up --from-docker-compose` now works out where a Postgres service actually
  keeps its data instead of assuming a path, and what it writes follows from
  that. The service's own `PGDATA` decides it when the compose file sets one (a
  container's environment overrides the image's); otherwise the image is asked,
  and the images declare it — 18 moved it into a version subdirectory. Where the
  cluster lands in the mounted directory itself, that mount still becomes a
  named volume. Where it lands *below* the mount, a host path works — measured
  on Postgres 17 — and the mount is now left alone, which is a change for anyone
  whose service sets `PGDATA` to a subdirectory. Where it lands somewhere else
  entirely — 18, at the path 17 used — the named volume only starts alongside
  the `PGDATA` written with it, so if that half cannot be written (the service
  sets its own `PGDATA`, passes one in from the environment, or mounts something
  below the data directory) opossum writes neither half and says what it found
  (`[OPSM-111]`). What that costs and what to change are in that code's entry,
  which lists the shapes this comes in — the cluster landing below the mount, at
  it, or somewhere else on 18 or on 17 — with the run each one was measured in.
  The comment on each change says where the answer came from: read from the
  service, read from the image, or assumed because the image was not on the
  machine yet.

### Fixed

- An error about the quoting or shape of a `command:` or `entrypoint:` now names which of the two it is. They are read by the same code, which is handed the value without being told the key it came from, so the message used to say `command` whichever it was — sending anyone with a bad entrypoint to the wrong line — and then said `command or entrypoint`, which was true but left you to check both. Where both are wrong in the same way it still says the pair, because there is no single answer to give.
- An image with no build for Apple silicon is told so again. The runtime has two ways of wording that failure and opossum recognised only one of them, so for the images that produce the other, the guidance went quiet: what came back instead was the raw error and a suggestion to read the logs, from a container that had never started. Both wordings are recognised now. Neither is answered with "ask for linux/amd64" when amd64 is what was asked for and what the image lacks — that failure is a different one, and repeating the request is not the fix for it.
- The message about a published port that is already taken no longer offers the runtime's built-in DNS on port 53 as the example of what the check before startup cannot see. That check does see port 53, and a port published by another project's running container. The blind spot it was pointing at is real — two services in one project publishing the same address are not both checked, and the second one's start is where this message comes from — but the example was wrong, and an example that does not happen is worse than none. What the message tells you about the port itself is unchanged.

## [0.22.1] - 2026-08-22

### Fixed

- `ports`, a service's `volumes`, and a service's `secrets` now say what they found in words when it is not a list. They answered with the YAML tag — "must be a list, got !!str" — which is the spec's own notation and no more use than the decoder's numbering was to the fields that have already stopped using it.
- When more than one compose file goes into a project, a failure at the final check no longer names the first file as though the problem were in it. More than one is easier to end up with than it sounds: several passed with `-f`, or any override file found next to your `compose.yaml` — including the one `opossum adapt` writes for you. They are merged into a single document before that check, so the line the parser reports counts in the merged text, and no record survives of which file a value came from. Naming the first one sent you to a file that need not contain the problem, at a line you could not find. All the files are named now, and the line is marked as belonging to the merged document. A single file still names itself and a line you can count to.
- What is wrong and where to find it are still there, and there is more of it than before. A rejected memory or CPU limit now says which of the two keys it read, a rejected mount type names the mount, and a healthcheck that rejects one of its durations now names the service it belongs to instead of leaving you to guess which of them it meant.
- A field given the wrong shape now says what it found in words. `networks`, `env_file`, `environment`, `depends_on`, a healthcheck's `test`, and `command` all answered with the number the YAML decoder uses internally — "got yaml kind 4" — which is nothing anyone can act on. They say "a mapping", "a list", "a single value".

### Security

- A `${` with no closing `}` now says where it is instead of quoting what came after it. What came after it is the rest of whatever was being expanded: in a compose file that is the tail of the file, and in an env file it is the value — where a password or a token can be. In a compose file the message now names the line the reference starts on, counting lines where YAML breaks them rather than only at a newline, so a file written with the old Mac line ending is counted the same way the parser counts it. In an env file the file and line were already there, so the message adds nothing to them.
- The fields with a small, fixed set of accepted values no longer read the value back when they reject it — the memory and CPU limits, a healthcheck's durations, a mount's type, `restart`, and a `depends_on` condition. What they are given comes out of the compose file, where a `${...}` reference can put a password or a token, and the error goes to the terminal, the CI log, and whatever issue the output is pasted into; unlike the parser's own message, these printed the value in full. Each of these messages already lists what the field accepts, so the value it was given added nothing it needed to say. Fields whose value is free text — a `command`, an env file's path — still quote what they were given, because there the text is the only way to see what is wrong with it.
- An error about a `command:` or `entrypoint:` no longer reads the command back. A command is free text and can hold a token — `sh -c 'curl -H "Bearer ${TOKEN}"'` is an ordinary thing to write — and quoting the whole of it into an error put that in the terminal, the CI log, and any issue the output was pasted into. It now names the service and says which quote never closes, which is what you need to find it. Docker Compose quotes nothing here either, and names the exact field where this says `command or entrypoint`: the two are read by the same code, which is not told which one it is reading, so naming both is as close as this can get for now. It used to say `command` whichever it was, which sent anyone with a bad entrypoint to the wrong line.

## [0.22.0] - 2026-08-21

### Changed

- Verified against Apple `container` 1.2.2. Everything opossum reads out of the runtime — `system status`, `inspect`, `ls`, mount modes, the conditions it refuses a mount under — was re-measured whole on 1.2.2, and none of the refusals turned into a false positive. The one difference found is cosmetic: `system start` now writes its progress to stderr instead of stdout. Requirements say which version the project is verified against; the numbers in the benchmark pages still name the version they were measured on, which is older, because a number is only true of the version it was taken from.

### Fixed

- A byte-order mark at the start of an env file is no longer read as part of the first key. Editors on Windows write one without being asked, and it used to turn the first `A=1` into a variable named `<BOM>A` — so `${A}` found nothing and expanded to empty, with no error anywhere to say why. A mark that ends up inside a variable name ends the same silent way, so that is now refused, naming the file and the line it is on; the message does not quote the line, because env files hold secrets. A mark inside a value, or inside a comment, is left exactly where you put it. This matches docker compose, which drops a leading mark, keeps one in a value, and refuses one in a name.
- A variable that expands to nothing now leaves an empty value wherever it was
  written, not only beside a key. An item of a sequence written in flow style —
  `command: [server, --port, ${PORT}]` with `PORT` unset — used to vanish
  entirely, so the container was handed `--port` with nothing after it and ran a
  different command than the file describes; in any position but the last, the
  file failed to load instead. A value written under its key rather than beside
  it was left inheriting from the host, which is the thing an emptied value is
  meant to stop. Both now arrive as the empty string, which is what Docker
  Compose reads them as (measured against v5.3.1).

  Two more differences with Compose close with them. A reference inside a quoted
  string spanning several lines used to lose the space in front of it when the
  lines were folded together. And a value like `image: alpine:${TAG}` with `TAG`
  unset used to fail the load outright — the colon it left behind read as a
  mapping — where it now arrives as `alpine:`, which is what Compose reads it as.

  A compose file may not contain U+E000, a private-use character used to carry
  "this expanded to nothing" through the parser. A file holding one is refused
  rather than read.
- A compose file that parses cleanly but holds a value of the wrong shape is no longer announced as invalid YAML. The common way to reach it is a `${...}` reference to a variable nobody set: `data: ${NOPE}` under `volumes:` leaves an empty string where a mapping belongs, and the error used to say the file was not valid YAML and to go check the indentation and quoting — sending you to hunt for a syntax mistake in a file that has none. It now says the file parsed and the value does not fit the field, and points at unset references as the usual cause.
- The same key set twice now says so, and names the file it is in, instead of being reported as a value of the wrong shape or as invalid YAML.
- A failure in a single compose file that also has a `${...}` reference expanding to nothing now names the line you would count to. Putting the emptiness back used to mean rebuilding the document, and the rebuild changed how many lines it had — blank lines went away, a `|` block collapsed, a `[a, b]` sequence opened out — so the line a later error named could be several off from the line in your file, in either direction, and further off the longer the file. The document is no longer rebuilt, and a `|` or `>` block, blank lines, and the order and style of what you wrote all survive the trip. This covers the errors the YAML parser positions itself, which is where the line numbers come from; a value that expands to something containing a newline still shifts the lines after it, because the text really does grow, and passing several files with `-f` still merges them into one document before the last check, so line numbers from that check are lines in the merged text.

### Security

- An error about a malformed line in an env file no longer prints the line itself. Env files are where passwords and tokens live, and a token pasted onto a line of its own is exactly the shape that triggers this error — so the message went to the terminal, the CI log, and any issue the output was pasted into. The error now names the file and the line number and says what is missing, which is what you need to fix it; open the file to see the line.
- The decoder's own message about a value of the wrong shape no longer reads that value back. It used to quote the value it could not use, and that value can have come from a `${...}` reference — which is where passwords and tokens live. Anything over ten characters was shortened to seven and an ellipsis, which was little comfort: a short password went out whole, and the first seven characters of a long token still say what kind it is. The line, the kind of thing you wrote, and the field it did not fit are all still there, and a key reported as set twice is still named in full. Fields that check their own values — `mem_limit`, `cpus`, a healthcheck's `interval` — still quote what they were given.

## [0.21.0] - 2026-08-21

### Fixed

- A compose value that is nothing but a variable reference, where that variable is
  unset, now reaches the container as an empty value instead of being inherited
  from the host. `MOUNT: ${NOSUCHVAR}` under `environment:` used to arrive at the
  parser as `MOUNT:` — YAML null, which means "take this one from the host" — so a
  service could be handed a value the compose file never mentions. Writing `KEY:`
  yourself still means exactly that, because the file says so — including when the
  comment beside it happens to mention a variable; what changed is that an emptied
  reference no longer looks the same. Measured against Docker Compose v5.3.1,
  which reads such a value as the empty string. A value that is a reference with
  something after it — `x${VAR}`, or a `.env` entry ending in a colon — is not
  touched, and neither is text the parser reads as a string, such as the inside
  of a `|` block.

  Two consequences beyond `environment:`. A mapping entry under `volumes:`,
  `networks:` or `depends_on:` whose value is an unset reference is now a load
  error rather than being read as an empty declaration — Compose rejects those
  too. And a `healthcheck.test:` that empties out becomes a check that always
  passes, so a `service_healthy` dependency waiting on it no longer waits for
  anything; give the variable a value, or drop the healthcheck, if that gate
  matters.

## [0.20.0] - 2026-08-19

### Changed

- The hints opossum decodes out of a failed start now carry a diagnostic code, so
  they can be looked up in `AGENTS.md` rather than only read: an image with no
  arm64 build is `OPSM-412` (new), while a host port the pre-flight could not see
  reuses `OPSM-201` and an unresolvable file bind mount reuses `OPSM-107` — same
  cause and same fix as the pre-flight checks that name them, just caught later. A
  start failure opossum cannot decode stays uncoded, so a diagnosed failure and an
  undiagnosed one still look different.
- `up` and `run` now stop when a bind mount's host source is a symlink pointing at
  a socket, naming the path the link resolves to — mounting that instead is the way
  through, because a socket reached by its own path mounts fine, and so does a
  symlink to a file or a directory. It is the combination that Apple `container`
  1.1.0 refuses, and until now opossum attempted it and passed on four levels of
  nested `internalError` about `errno 95`. The common way to meet this is
  `/var/run/docker.sock` on a machine where Docker Desktop has linked it. The check
  only reads the host, so `--dry-run` reports it too.

### Fixed

- `.env` values that reference other variables are now expanded, matching Docker
  Compose (measured against v5.3.1). A line like
  `DATA_PATH=${DATA_ROOT:-/mnt/data}/psql` used to reach the compose file verbatim,
  so a mount written as `${DATA_PATH}:/var/lib/postgresql/data` split at the colon
  inside the default and the runtime was handed `${DATA_ROOT` as a volume name. The
  rules follow Compose: only keys defined above the line are in scope, an unresolved
  reference expands to empty, a single-quoted value is left alone, and the shell
  still wins over the file. Values in a service's `env_file:` are expanded the same
  way. Where several env files apply, they fill one map top to bottom — a later file
  overrides an earlier one, and a file's own line wins over a file read before it —
  while an outer level always wins. The levels, outermost first: the shell, the
  project's `.env` (or `--env-file`), a service's own `environment:` block, then that
  service's `env_file:` files.

  Two kinds of env-file line that used to load now fail — in a `.env`, an
  `--env-file`, or a service's `env_file:` alike, in both cases the way Compose
  already fails on them: a value holding a required reference (`${VAR:?message}`)
  when that variable is unset, and a value holding an unterminated `${`. A value can
  also change meaning wherever env files are read — `$` now starts a reference, so
  `PASSWORD=s3cr3t$pass` reads `$pass` as a variable. Write `$$` for a literal `$`,
  or single-quote the value.

## [0.19.1] - 2026-08-07

### Changed

- `up` now stops when a bind mount's host directory could not be created, instead
  of warning and starting the service anyway. It used to report the problem and
  then attempt the mount, so the warning was followed a moment later by the
  runtime's own `path '…' does not exist` with nothing tying the two together.
  Nothing is lost by stopping: the start was going to fail, and a failed start
  rolls the project back regardless.
- A bind mount whose host source is a symlink pointing at nothing now says so,
  rather than reporting that the directory could not be created and suggesting a
  `mkdir -p` that fails the same way.

### Fixed

- A volume whose mount target contains a `$`, a backtick, or a tab or newline —
  `data:/app/$(id)` — is now seeded as the path it is. The script opossum runs to fill a new volume
  quoted those paths the way Go does, which a shell reads as its own quoting: the
  copy would look somewhere other than the path you wrote and silently leave the
  volume empty, or run what was written in it.
- `up` no longer reports success over a service that died a moment after
  starting. It used to look once, the instant the container was launched, which is
  before a service with a bad config has finished failing — a postgres with no
  password set, or refusing a data directory it cannot initialise, was still
  "running" at that instant, so `up` exited 0 and the failure only showed up as
  `stopped` in `opossum ps`. It now looks again a second later and reports the
  exit with the service's logs. `OPOSSUM_CRASH_GRACE` sets that window;
  `OPOSSUM_CRASH_GRACE=0` gives the second back and, with it, gives up catching
  anything that doesn't die instantly.
- A new volume whose mount target begins with `-` — `data:-rf` — is filled from
  the image again. opossum copies the image's contents at that path into the fresh
  volume, and the copy read the target as options rather than as a path, so it
  failed and the volume came up empty behind a "couldn't fill the new volume"
  warning.

## [0.19.0] - 2026-08-05

### Added

- `volume: {nocopy: true}` now works, and so does its short spelling `src:target:nocopy`. opossum fills a fresh volume from the image the way Docker does, and this is how a compose file turns that off — for a dependency directory the image ships stale, say, or one that is populated another way. It was being dropped during parsing, so the volume was filled anyway and nothing said why the line had no effect; the short spelling was worse, reaching the runtime as if `nocopy` were a mount mode. Measured against docker compose, which leaves such a volume empty.
- `up` now says when it couldn't fill a fresh volume from the image. opossum emulates Docker's volume seeding by running `cp -a` inside a throwaway container built from that image, and an image with no shell — most distroless and scratch builds — has nothing to run the copy with. The volume then mounts empty, which looks exactly like a service that lost its data. The warning names the volume, says why the copy couldn't run, and points at the ways out: put the content there another way, or add `volume: {nocopy: true}` to record that an empty mount is what you meant (`OPSM-108`).

### Changed

- `up --from-docker-compose` no longer proposes a named volume for every empty read-write bind directory. It used to read an empty directory as "the app will put its data here"; measured over 156 real-world projects that guess produced 193 proposals and about half were wrong — `/config` files you edit, `/downloads` and `/media` you open, `/logs` you read. A suggestion that is a coin flip is worse than none, because it teaches you to skip the section that also holds the good ones. Suggestions are now driven by what actually happened: when a container dies taking ownership of a bind mount — the failure Apple `container` produces because bind mounts are host-owned — that service and that mount are remembered, and the next `up --from-docker-compose` proposes a named volume for exactly that mount — written into the overlay when there isn't one yet, and reported on screen when there is (opossum never overwrites an existing `compose.opossum.yaml`). The automatic fixes for databases whose behaviour is known (Postgres, MySQL/MariaDB, ClickHouse, MongoDB, Redis) are unchanged.
- Declining `opossum destroy` at the prompt now ends with a pointer to `opossum destroy --dry-run`. Saying no skips the after-removal report, so nothing ever told you about the things destroy leaves alone (the DNS domain, workspace snapshots, unclaimed volumes) — the one-line pointer keeps them discoverable without burying a "no" in output.
- The warning about Postgres's data directory (`OPSM-101`) now reports what opossum found instead of predicting from the shape of the mount. It used to fire whenever a named volume sat directly at `/var/lib/postgresql/data`; with `lost+found` now cleared from the volumes opossum creates, that mount is the one that works, and the prediction would have been wrong on every `up` of a healthy stack — a warning that is wrong half the time teaches you to skip the paragraph that also holds the true ones. In its place: before the service starts, opossum reads a volume that already exists and warns only if it holds `lost+found` and no cluster; and if a service dies anyway, initdb's own refusal is decoded into the same guidance. What is left is the case that is genuinely still broken — a volume opossum didn't create, made by an older opossum, by `container volume create`, or by another project — and the message names it, says what it costs to recreate it, and gives the `PGDATA` route as the alternative that keeps the data.

### Fixed

- Filling a fresh volume from an image whose default user isn't root — anything with a `USER` line, which is the node images and most database images — copied nothing at all, and said nothing about it. A fresh volume's root belongs to `0:0` and is mode 755, so the image's own user cannot create a single file in it: this was never a matter of ownership not surviving the copy, the volume simply came up empty, and a service that had lost its data looked exactly the same. The copy now runs as root, which is the privilege Docker seeds with — there the engine does the copying — so the contents and their ownership both arrive intact. Failures of the copy are no longer swallowed either: one that starts and fails is reported (`OPSM-108`), while a path the image doesn't have stays quiet, because that one is the ordinary case.
- The compatibility documentation said opossum does not seed volumes. It does, and has since volume support landed: a fresh named or anonymous volume is filled from the image's contents at that path before the service starts, which is what makes the bind-mounted-source plus `- /app/node_modules` pattern work. The docs told you to work around a limitation that isn't there — installing dependencies at container start, or not using a volume for them — so if you did that, you no longer need to. The docs now say what is actually emulated and what still isn't (an image without a shell is skipped, `external: true` volumes are never touched).
- A fresh named or anonymous volume now starts empty, the way it does on Docker. Apple `container` gives every volume its own ext4 filesystem, so one arrives already holding `lost+found` — where a Docker volume, being a directory on the host, holds nothing — and any program that checks whether its data directory is empty sees the difference. Postgres is the one people hit: `initdb` refuses to initialise into a directory that isn't empty, and says so by name. opossum now clears `lost+found` out of the volumes it creates, so a compose file that writes `pgdata:/var/lib/postgresql/data` works here as it does on Docker, with no need to move `PGDATA` into a subdirectory of the mount. Volumes opossum didn't create are left alone, and the clearing is an `rmdir`: a `lost+found` that holds anything — files an fsck recovered — makes it fail and stay, so a step meant to make a volume look like Docker's cannot destroy what it finds there.

## [0.18.2] - 2026-08-01

### Fixed

- Correction to the 0.18.1 note on `healthcheck: disable: true`. That note said the check stayed live and a service depending on it with `condition: service_healthy` waited on a check you had switched off. That is what happened when `disable: true` was written beside a `test:` that actually ran, and that case is genuinely fixed. Written on its own, the line never got that far: the healthcheck looked absent rather than disabled, so a `service_healthy` dependant was already turned away at load — with the same message, before and after the fix. What 0.18.1 changed for `disable: true` on its own is that `opossum config` now echoes it back instead of dropping it.

## [0.18.1] - 2026-08-01

### Fixed

- `healthcheck: disable: true` now works. Only the `test: ["NONE"]` spelling was read, so the compose spec's other way of switching a healthcheck off did nothing at all — the check stayed live, and a service depending on it with `condition: service_healthy` waited on a check the user had switched off. Both spellings now mean the same thing, and `disable: true` wins over a `test:` written beside it.

## [0.18.0] - 2026-07-31

### Added

- `up` now says so when a bind mount names a file that isn't there. A missing bind source has to be created or the container won't start, and a directory is the only thing that can be created — so a file you were meant to supply (`./init.js:/docker-entrypoint-initdb.d/init.js`) quietly became a directory, the service started anyway, and the init script simply never ran. The failure showed up later as something else entirely. opossum now names the path, says a directory is standing in for the file, and tells you how to put the real one there (`OPSM-107`).

### Changed

- `destroy` now names any `.opossum-snapshots/` directories it found, alongside the DNS domain and the builder cache it already reported. Snapshots are still never removed — a snapshot belongs to the directory that was snapshotted, and that directory outlives any number of projects — but they are usually the largest thing left on disk, and a command that promises to leave no trace shouldn't be the reason you find them a month later.

### Fixed

- `up --from-docker-compose` no longer suggests a named volume for a bind mount that passes a single file through (`./init.js:/docker-entrypoint-initdb.d/init.js`). A bind source that doesn't exist yet is created as a directory whatever it was meant to be, so a file you were told to supply looked exactly like an empty data directory — and the suggestion, if applied, would have hidden the file you were about to put there.
- A service that `up` left alone because it was already up to date now keeps its `restart:` supervision when a later service fails the bring-up. The failed start is rolled back, but an untouched container is not part of that rollback — it is still running, and opossum was reporting that nothing was left, so nothing watched it.
- `destroy --dry-run` now reports what it would leave behind — the DNS domain, the build cache, and any workspace snapshots — the same way the real run does. The preview is the mode you read before deciding, and it was the one that didn't say: when there was something to remove, the list of what stays came only after it was gone.

## [0.17.0] - 2026-07-30

### Added

- `restart:` is now honoured. A project that declares a policy gets a small supervisor of its own, started by `up` and stopped by `down`, that brings a service back when it exits — the job Docker's always-running engine does and Apple `container` has no equivalent for. `always` and `unless-stopped` behave as they do under Docker, including leaving a service you stopped on purpose alone. `on-failure` can only be approximated: the runtime doesn't report a container's exit code, so a crash and a clean exit look the same, and opossum retries a bounded number of times rather than looping a service that may have finished. `opossum config` says so for the services affected.
- `opossum destroy` removes everything opossum created for a project in one command: containers (including orphans), the project network, named volumes, images it built or pulled, the restart supervisor, the `.opossum/` state directory and the generated `compose.opossum.yaml`. Your compose file, `.env` and sources are never touched, and neither is anything shared — volumes declared `external: true` and other projects' containers stay, while the DNS domain and the build cache are reported with the command to remove them rather than removed for you. It lists what it will remove and asks first; `--force` skips the question for scripts and agents, `--dry-run` lists and stops, and `--keep-overlay` keeps `compose.opossum.yaml` in case you edited it.
- `opossum destroy` now reports volumes it cannot account for instead of leaving them invisible. A volume named for the project that no service claims — left behind when a service was renamed or removed from the compose file — is listed as kept, with the command to remove it, because opossum cannot tell such a volume from an `external: true` one by name alone. Previously the teardown reported that everything was gone while these stayed on disk.

### Changed

- The restart supervisor's log is now capped at 1MB. A service that fails permanently is restarted for as long as its policy asks, and each attempt writes a line — about 270KB a day, previously without limit, for a project you may have forgotten about. On reaching the cap the log keeps its newest half and says so on its first line (`[OPSM-410]`); the recent lines are the ones that explain why a service is down.

### Fixed

- A service declaring `restart:` is no longer left unwatched when `up` fails. A service that exits immediately after starting fails the up, but the rest of the stack stays running — opossum stopped there without starting its supervisor, so `restart: always` quietly did nothing for the containers that were still up. A bring-up that fails and is rolled back still starts no supervisor: nothing survives it to watch.
- `opossum up <service>` no longer stops watching the services it didn't touch. Bringing up one service used to replace the project's restart supervisor with one covering only that service, so anything else still running quietly lost the `restart:` policy the compose file gives it. A partial up now watches what it started plus whatever was already being watched and is still there.
- `opossum destroy -p <other-project>` no longer removes the current directory's generated files under another project's name. The runtime objects belong to the name you gave; `.opossum/` and the generated `compose.opossum.yaml` belong to the directory you are standing in, which is usually a different project. The plan now names that directory and says whose files they are, and `--force` refuses rather than act on the guess — add `--keep-local` to remove only the named project's containers, volumes, images and supervisor.

## [0.16.0] - 2026-07-29

### Added

- `compose.opossum.yaml` is now the whole compatibility picture for a project, not just the fixes: alongside the changes opossum **applied**, it records **suggestions** (a concrete change written out but commented — it alters what the project means, so it's yours to decide; uncomment the block to apply it) and **notes** (things no compose change can fix, like a Docker socket mount or a host device). Each entry says which of the three it is, with a stable marker. `up` reports the three separately, so a note is never counted as a change opossum made. Suggestions cover a named volume shared by several services (which Apple `container` can't attach twice) and an application's own data directory on a bind mount; notes cover Docker socket mounts and host devices. An overlay is written only when there is something to apply or suggest — findings that are notes alone are reported but don't create a file.

### Changed

- The automatic fix for a database data directory on a bind mount now also covers **ClickHouse**, **MongoDB** and **Redis/Valkey** (previously Postgres and MySQL/MariaDB only), each confirmed on the real runtime to fail the same way. Every chowned directory on a service is fixed in one pass — MongoDB has two (`/data/db` and `/data/configdb`), and fixing only one left the container still crashing. A service built on a database image but running a client or dump command (`redis-cli`, `mongodump`, a shell) is left alone: those read a bind mount fine, so rewriting one would have swapped real data for an empty volume.
- A `ports:` entry that names only a container port (`ports: ["3000"]`) no longer fails when the matching host port is taken. Compose leaves the host port to the engine for those, so opossum now falls back to a free one and says which (`opossum ps` shows the ports actually published) instead of refusing to start. The same-number mapping is still preferred whenever it's available, and an explicit `"3000:3000"` is never moved — that one still fails loudly, since it's a contract you wrote down.

### Fixed

- The host-port pre-flight (`OPSM-201`) now detects a port held by a listener bound to all interfaces over IPv4. It probed with a single dual-stack bind, which on macOS succeeds alongside such a listener — so the port read as free and the run failed later with the runtime's raw bind error instead. This was the case the check most needed to catch: the daemons that squat ports, AirPlay's receiver on 5000/7000 included, listen on IPv4. (A daemon bound only to `127.0.0.1` is still not detected; the runtime does bind such a port, but traffic reaches the other listener rather than the container.)

## [0.15.0] - 2026-07-28

### Added

- A `compose.opossum.yaml` (or `.yml`) next to a discovered compose file is now auto-merged **last, at the highest precedence** — after the base file and any `compose.override.yaml`. docker compose ignores this name, so the same directory works with both tools and your original files stay untouched: keep Apple-`container`-specific tweaks here. Merging one prints a one-line notice naming the file (delete it to opt out).
- `up --from-docker-compose` now **writes the fixes a docker compose project needs to run on Apple `container`** into a `compose.opossum.yaml`, then starts — so bringing a project over is one command instead of read-warning-edit-retry. It handles the two patterns that fail for runtime reasons rather than mistakes in your file: a named volume mounted at Postgres's data directory (`PGDATA` is pointed at a subdirectory of it) and a database data directory on a bind mount (swapped for a named volume — note this changes where the data lives; the host directory is left untouched). Every entry says what changed, why (with its diagnostic code), how to verify it, what to do if it still fails, and how to undo it. An existing `compose.opossum.yaml` is never overwritten, your own compose file is never modified, and nothing is written when there's nothing to fix.
- A Japanese README ([`README.ja.md`](README.ja.md)), and a diagram in the **Networking model** section (mermaid) contrasting the docker-compose and Apple-`container` network models side by side.

### Changed

- `up --from-docker` is now **`up --from-docker-compose`**. The flag is the switch for bringing an existing docker compose project up here, and the new name says so. The old `--from-docker` still works exactly as before and prints a one-line notice pointing at the new name, so existing scripts and examples keep running.

### Fixed

- `volumes` entries that mount the same container path now collapse to one, matching docker compose: the last entry wins, whether the duplicates come from one file or from a file and its override. Previously every entry was passed to the runtime, so two sources could end up mounted at a single path — and an override couldn't swap a bind mount for a named volume.

## [0.14.0] - 2026-07-24

### Added

- `opossum doctor` now checks **reclaimable storage**: it reads `container system
  df` and warns when a large amount of image/volume storage is unused by any
  running container, with the reclaim command (`container image prune -a`). Apple's
  `container images ls` doesn't list untagged images, so build/pull leftovers can
  fill the disk unseen — this surfaces them before that happens. The amount is
  shown even when it's within a normal cache.
- When a build fails because the host ran out of disk (`no space left on device`),
  `build`/`up --build` now decodes it into the fix — free space with
  `container image prune -f` and `container builder delete --force`, then retry —
  instead of forwarding the raw builder error. A real build pulls multi-GB base
  images and layers onto the host volume, so this is a common failure; the remedy
  is the opposite of the resource-starvation one (growing the builder would only
  use more disk). Found dogfooding real builds.

### Changed

- When a service fails to start for a reason the pre-flight can't catch, `up` now
  decodes the cryptic runtime error into the fix instead of just forwarding it:
  a host-port conflict the host probe can't see (Apple `container`'s built-in DNS
  holds port 53, so a DNS server like AdGuard/Pi-hole clashes) → remap the port;
  an image with no arm64 build (`does not support required platforms`) → add
  `platform: linux/amd64` (opossum runs it via Rosetta); a bind mount of a config
  file whose host source is missing → create the file first (opossum makes a missing
  bind source a directory, which can't mount onto a file path). Found dogfooding
  real self-host composes.

## [0.13.0] - 2026-07-24

### Added

- When a database container crashes because it can't `chown` a **bind-mounted**
  data directory (Apple `container`'s bind mounts are host-owned and not chownable —
  common with self-host composes that put data under `/mnt/docker-volumes/…`), the
  crash report (`[OPSM-401]`/`[OPSM-407]`) now points at the fix: use a **named
  volume** for that directory. Previously the raw `chown: … Operation not permitted`
  was shown without the remedy.
- `up` now fails up front (`[OPSM-205]`) when a service joins a network declared
  `external: true` that doesn't exist — opossum uses external networks by name and
  never creates them, so a missing one used to surface only as a raw "network not
  found" when the service tried to start. The error names the network and gives two
  fixes (create it, or drop `external:`). Common with reverse-proxy composes that
  expect a shared `proxy` network.
- `up` now catches a service that **exits right after starting** even when nothing
  gates on it (no healthcheck or `depends_on`). Previously such a service — a bad
  config, a failed Postgres `initdb`, a missing mount — left `up` reporting success
  (exit 0) over a dead container. Now `up` prints the crashed service's last log
  lines (`[OPSM-407]`) and exits non-zero, so "started" never masks "already dead".
  The containers are left up for inspection (not rolled back). A dependency crash
  caught by a health gate is still `[OPSM-401]`.

### Changed

- `opossum run` (a one-off) now gets the same named-volume conflict handling as
  `up`: it warns up front (`[OPSM-103]`) when a running container — including the
  service's own `up` container — already holds a volume the one-off needs, and if
  the run still hits the cryptic exclusive-attach VZError it's decoded to the same
  clear message naming the volume and holder. A one-off's ordinary non-zero exit is
  untouched, so `run` keeps propagating exit codes.
- More actionable error messages across the remaining lifecycle and teardown paths
  (second pass of the error audit): a failed `start`/`restart` says the container
  must exist first (`opossum up`); a failed `pull` names the image and points at
  registry auth/network; a generic service-start failure points at `opossum logs`;
  `logs` points at `opossum ps`; the `watch` rebuild/restart/sync/setup/error
  warnings each carry a fix (and sync now names the file and service); Docker-import
  failures explain what to check. Best-effort teardown (`down --volumes`/`--rmi`)
  no longer swallows a real failure silently — a volume or image that won't delete
  now warns with a next step, while a clean re-run (already gone) stays quiet.

### Fixed

- Variable interpolation now resolves a reference **nested inside another's
  default** (`${A:-${B:-x}}`) and handles a reference written across lines with YAML
  double-quoted `\`-continuations. Previously a nested `${…}` was truncated at the
  first `}` and a multi-line reference failed to parse — both are used by real
  self-host composes (e.g. rocketchat's MongoDB replica-set URL).

## [0.12.0] - 2026-07-23

### Added

- `up`/`run` now print a one-line `note:` when the compose file has fields opossum
  ignores (e.g. `dns_search`, `container_name`), pointing to `opossum config` for
  the full list — so a dropped field never silently looks like it took effect. It's
  a low-key note, not a warning; `--verbose` still lists each ignored field. AGENTS.md
  now also documents that `dns`/`dns_search` are ignored and that service discovery
  is automatic (bare service names under `<project>.opossum`), so there's no reason
  to set them.
- When a service fails to start because a named volume is **already attached to
  another running container** (Apple `container` attaches a named volume to only
  one running container at a time), opossum now decodes the cryptic virtualization
  error (`VZErrorDomain Code=2 "The storage device attachment is invalid"`) into a
  clear `[OPSM-103]` message that **names the volume and the container holding
  it** — including a holder from a *different* project — and tells you how to free
  it. The same conflict is also flagged as a pre-flight warning at `up` time when
  the holder is already running, so you see it before the failed start.
- `opossum run --audit` reports what a one-off did after it finishes — the
  "verify" half of declaring what an agent may do: the workspace file diff
  (added/changed/deleted + content hashes, from an APFS snapshot taken just before
  the run), the egress destinations (read from the allowlist proxy's log when the
  run routes through one; otherwise marked *unobserved* rather than a misleading
  blank), and the exit code. `--audit-format json` gives a machine-readable report
  (like `doctor --format json`); the container's own stdout goes to stderr so the
  report owns stdout.
- A service can declare the MCP servers an agent inside it should use with the
  `x-opossum-mcp-tools` compose extension, and opossum generates a `.mcp.json` and
  mounts it read-only at `/run/opossum/mcp.json` — so "which tools this agent has"
  is declared in the compose file, not hand-wired. Each entry is another service
  (`svc`, `svc:port`, `svc:port/path` — reached by name on the shared network) or an
  explicit `name=url`. Pass it to Claude Code with `--mcp-config /run/opossum/mcp.json`.
  MVP is HTTP-transport MCP servers. See `examples/agent-sandbox`.
- `opossum ws snapshot [name]` / `ws ls` / `ws rollback <name>` snapshot and roll
  back a workspace directory (`--path`, default `./work`) using APFS copy-on-write
  clones — a snapshot is near-instant and uses almost no extra disk, so an agent
  can try something risky and reset in an instant. `rollback` saves the current
  state first (reversible); snapshots live in `.opossum-snapshots/` beside the
  workspace; on a non-APFS filesystem it falls back to a full copy and says so.
  `ws rm <name>…` deletes named snapshots and `ws prune` clears out the auto-saves
  `rollback` accumulates (`--keep N` to keep the newest few, `--all` for every
  snapshot), so they don't pile up.
- `OPOSSUM_DOCKER_BIN` overrides the `docker` CLI that `opossum import` shells out
  to (mirroring `OPOSSUM_CONTAINER_BIN`) — useful when docker lives on a
  nonstandard path or you want `import` to use a specific docker-compatible CLI.

### Changed

- When the `container` runtime isn't running, a **mutating** command (`up`, `run`,
  `build`, `pull`, `down`, …) now **starts it automatically** (`container system
  start` — a light, idempotent launchd start) and proceeds, printing a one-line
  notice (`[OPSM-406]`) that also explains *why* it was needed. Previously `up`
  began work and then failed mid-way with the runtime's raw error. Read-only
  commands (`ps`/`images`) still report `[OPSM-405]` without starting anything (a
  read shouldn't have a side effect), and both messages now say why the runtime
  needs starting. Opt out of auto-start with `OPOSSUM_NO_AUTO_START`.
- Clearer, more actionable error messages across common failures (first pass of a
  broader audit): a malformed compose file now says it's invalid YAML and where to
  look (instead of a raw parser dump); `depends_on`/`secrets`/duration/memory/cpus
  mistakes now show the fix or a valid example; an unknown service name lists the
  services the project actually defines; `snapshot not found` points at `opossum ws
  ls`; and failing to create a bind mount's host directory now warns (`[OPSM-104]`)
  with the fix instead of failing silently later. The guiding rule — every error
  says what happened, why, and what to do next.
- The `examples/agent-sandbox` **caged** variant now shows a working MCP tool: an
  agent whose internet is fenced to an allowlist can still use one host tool. The
  tool is declared as an explicit URL through the host gateway (an internal network
  has no name resolution) and `NO_PROXY` keeps that traffic off the egress proxy —
  so the caged agent reaches only the allowlisted internet plus the one declared
  tool, and nothing else.

## [0.11.0] - 2026-07-21

### Added

- The `examples/agent-sandbox` "caged" variant now ships a **self-contained egress
  allowlist**: a small forward-proxy service and a `proxy/allowlist` file declare,
  in the compose file itself, exactly which hosts a sandboxed agent may reach
  (default-deny). The agent runs on a host-only network with no internet of its
  own, so the proxy is its only way out and the allowlist is enforced rather than
  advised — no host-side proxy to set up. See the example's README.

### Changed

- `opossum ps` and `opossum images` now fail with a clear error (`[OPSM-405]`) and a
  non-zero exit when Apple's `container` system is installed but **not running**,
  instead of printing an empty table / `PRESENT=no` that looks like "nothing is
  here". Start the system with `container system start` (or run `opossum doctor`).
- When Apple's `container` CLI isn't installed, **every** runtime command now
  fails the same way — a clear, actionable error (`[OPSM-404]` with the
  `brew install container` / `container system start` steps) and a non-zero exit —
  instead of some commands quietly succeeding with misleading output. Previously
  `ps` printed an empty table (looking like "nothing is running") and `images`
  reported `PRESENT=no`, both exiting 0. `config` still works without the CLI
  since it only parses your compose files.

## [0.10.0] - 2026-07-17

### Added

- opossum's warnings and recovery-relevant errors now carry a stable
  `[OPSM-NNN]` code (e.g. `[OPSM-101]` for the Postgres data-dir volume trap,
  `[OPSM-204]` for a Docker-socket mount). The codes are add-only and map 1:1 to
  `AGENTS.md`'s failure-signature / diagnostic-codes tables, so an agent (or a
  human) can jump straight to the fix. Message wording is unchanged apart from
  the prefix.
- `opossum doctor --format json` prints the environment checks as
  machine-readable JSON — a top-level `{healthy, checks[]}` where each check has
  `{id, status, detail, fix}` (status is `ok`/`warn`/`fail`) — so scripts and
  agents can decide from one call. The default output stays human-readable, and
  a failed check still exits non-zero in both formats.
- `AGENTS.md` — a high-density, facts-only reference for driving opossum from an
  AI agent: the command surface (with exit-code behavior), the
  supported/ignored/rejected compose fields, a failure-signature→fix table, and
  the sandboxing/egress vocabulary. README points agents at it. A test keeps it
  in sync with the actual CLI (every command must be documented).
- `opossum up --dry-run` **prints the plan without executing anything**: the
  service startup order, the recreate/skip decisions, and the exact `container`
  commands it would issue — but it creates, starts, and deletes nothing. It fills
  the gap between `config` (resolves the configuration only) and `--verbose`
  (shows commands while running them), so you can validate what `up` will do
  before acting. (A `--format json` variant may follow.)
- `up` now **warns when a service mounts the Docker socket** (`docker.sock`).
  Apple `container` has no Docker daemon socket, so the mount fails at runtime
  with an opaque error — the warning explains the real reason up front (tools
  that drive Docker over its socket, e.g. Portainer, can't work here).

### Changed

- When a dependency fails to become healthy because its container has exited,
  `up` now **embeds that container's last log lines in the error** — so you see
  the real cause (e.g. a Postgres `initdb` failure) immediately. It no longer
  points you at `opossum logs`, which wouldn't work: a failed `up` rolls back and
  removes the container.

## [0.9.0] - 2026-07-16

### Added

- `opossum stats --host` reports each service's **host** memory footprint — the
  resident size of its VM on your Mac — alongside the guest-view usage. Because
  Apple `container` runs each container in its own VM, this per-service cost to
  the host is a real number a shared-VM tool (Docker Desktop, Colima, OrbStack)
  can't break down per service. It's host-derived and approximate; a service
  whose VM can't be mapped shows `—` rather than failing.
- A service can now join **multiple declared networks** (`networks: [a, b]`) —
  each becomes a `container run --network`, in declaration order. Previously a
  service was limited to one network. (Per-network aliases are still not applied.)
- `examples/agent-sandbox`: run Claude Code fully autonomously inside an Apple
  `container` VM, where the compose file **is** the agent's permission boundary —
  a `./work` bind mount (the files it sees), a `.env` token (the secret it holds),
  `networks:` (how far it reaches, including a host-only `internal:` "caged"
  variant that forces egress through a host proxy), and `mem_limit`/`cpus`. Driven
  as a one-off with `opossum run --rm agent`. Includes a README covering the two
  bring-your-own auth options and what the VM does and doesn't protect.
- The **long mapping form of `ports:`** (`{target, published, protocol,
  host_ip}`) is now accepted, alongside the short string form. Real-world compose
  files that use it previously failed to load (`cannot unmarshal !!map into
  string`); they now normalize to the same `host:container` spec.

### Fixed

- An unsupported `network_mode` (e.g. `host`, which several real-world compose
  files use) no longer fails the whole file at load. It's ignored — the service
  joins the project network — and listed among the ignored fields, so a
  `docker-compose.yml` loads without surprises. Only `network_mode: none` is
  acted on.

## [0.8.0] - 2026-07-16

### Added

- **Declared networks, including host-only (`internal`) networks for egress
  control.** A top-level `networks:` block plus a per-service `networks: [name]`
  now place a service on a named network instead of the default per-project one.
  An `internal: true` network is created host-only (`container network create
  --internal`): no internet egress, though the host stays reachable — so an
  untrusted workload on it can only reach out through a proxy you run on the host
  (via `${OPOSSUM_HOST_GATEWAY}`), making the allowlist enforced rather than
  advisory. `external: true` (with optional `name:`) reuses a pre-existing network
  by its real name (never created or removed). opossum joins a service to at most
  one network today; on an internal network, peers can't resolve each other by
  name (use IPs). See the new "Constraining egress (agent sandboxes)" README
  section.
- `network_mode: none` now isolates a service from all networking (mapped to
  `container run --network none`): loopback only — no egress and no name
  resolution. It's the floor for sandboxing an untrusted workload, honored on
  both `up` and `run`, and toggling it recreates the container. Other
  `network_mode` values (e.g. `host`) are rejected at load rather than silently
  ignored.
- More compose run options are now applied, each a thin passthrough to the
  matching `container run` flag: `user` / `working_dir` (`--user` / `--workdir`),
  `init` (`--init`, a tini-like PID 1 that reaps zombies), `read_only`
  (`--read-only` root filesystem), and `cap_add` / `cap_drop` (`--cap-add` /
  `--cap-drop`). They're honored on both `up` and `run`, and a change to any of
  them recreates the container.
- `examples/mcp-stack` and a README section, "Run your MCP servers on Apple
  container": host MCP servers (small, idle, credential-holding) on Apple
  `container` instead of an always-on Docker Desktop. Shows the graduation ladder
  — a raw `container run` for a single secret-free stdio server, moving to a
  compose file for secrets (token in `.env`, not a committed `.mcp.json`),
  several servers, or an HTTP (streamable) server you `up`/publish a port and
  point a client at `http://localhost:8080/mcp`. Verified end-to-end with
  `hashicorp/terraform-mcp-server` (stdio and streamable-http).
- `opossum watch` now automates the `rebuild` and `sync+restart` actions
  (previously sync-only): a change under a `rebuild` rule rebuilds the service's
  image and recreates its container; `sync+restart` copies the file, then
  restarts the container. Rebuilds and restarts are batched, so a burst of edits
  triggers one per service.

## [0.7.0] - 2026-07-13

### Added

- `opossum watch` mirrors host file changes into running containers, like
  `docker compose watch`: it reads each service's `develop.watch` rules and, on a
  change under a rule's `path`, `action: sync` copies the file to `target` inside
  the container (honoring `ignore` globs). Start the stack with `up`, then run
  `watch` (Ctrl-C to stop). `rebuild`/`sync+restart` actions are parsed but not
  yet automated.
- `ssh: true` on a service (and `opossum run --ssh`) forwards the host's SSH
  agent into the container (`container run --ssh`), so a service can `git
  clone`/`push` private repositories over SSH with your host keys — without
  copying keys into the image.
- `${OPOSSUM_HOST_GATEWAY}` built-in interpolation variable expands to the
  address a container can use to reach a service running on the host (Apple
  `container` has no `host.docker.internal`), so a compose file can point a
  container at, e.g., a model server running natively on the host. Overridable
  via shell env or `.env`; a `examples/local-ai-stack` shows the pattern.
- `opossum run -T` / `--no-tty` disables the pseudo-terminal (like `docker
  compose run -T`), so `opossum run web cmd | jq` from a terminal isn't polluted
  by tty echo/CRLF.
- `opossum cp <src> <dst>` copies files between a service's container and the
  host (each path is a host path or `service:path`), like `docker compose cp` —
  a thin wrapper over `container cp` with service-name resolution.
- `opossum doctor` diagnoses the environment in one command: the `container`
  runtime, the DNS domain registration, outbound network/DNS from a probe
  container (catching a wedged default network), the build VM's memory, and — if
  a compose file is present — a rough memory estimate for the stack. Each check
  prints ✅/⚠️/❌ with a one-line fix.
- `up` warns when two services share the same named volume. Apple `container`
  attaches a named volume to only one running container at a time, so the others
  fail to start with an opaque VM error — the warning names the volume and the
  services and suggests a bind mount (or baking the data into the image).

### Fixed

- `run`'s stdout is now clean even when it starts dependencies or builds an
  image: dependency-startup, build, and volume-seeding progress go to stderr, so
  only the one-off's own stdout remains — completing the stdio bridge for tools
  like an MCP server speaking JSON-RPC over stdio (previously the build's final
  image tag leaked to stdout).

## [0.6.1] - 2026-07-13

### Fixed

- `run` now keeps the container's stdin connected (piped input reaches the
  process instead of hitting an immediate EOF) and prints its own progress to
  stderr, so the container's stdout comes through clean. Together these let
  stdio-based tools run as one-offs — e.g. an MCP server: point your MCP
  client's command at `opossum run --rm <service>`. A TTY is allocated only
  when opossum's own stdin is an interactive terminal (so `opossum run web sh`
  still gets a proper shell). One caveat: if the run first has to build the
  image or start dependencies, that output still reaches stdout — for a clean
  stdio pipe, use a service without a `build:` (or pre-build) and `--no-deps`
  (or pre-start the deps with `up`).

## [0.6.0] - 2026-07-09

### Added

- `opossum import [service…]` copies a service's Docker-built image into
  `container`'s store (`docker save` → `container image load`), so `up` starts it
  without rebuilding in Apple's builder — handy for onboarding (reuse images
  `docker compose` already built) or when Apple's builder can't handle a
  Dockerfile. A failed build now points to this fallback. `docker` is only
  invoked by `import`.
- `up --from-docker` does the import inline: for each service with a build, it
  imports the Docker-built image instead of building, then starts — a one-command
  onboarding path for a project you already `docker compose build`.

## [0.5.0] - 2026-07-09

### Added

- Multiple `-f` compose files are merged in order, and a `compose.override.yaml`
  (or `docker-compose.override.yml`) beside the base file is applied automatically.
- `logs --follow` across several services multiplexes their output into a single
  stream with per-service prefixes.
- `config` honors `--profile` / `COMPOSE_PROFILES`, showing only the services
  that would start.
- Resource limits are applied: `mem_limit` / `cpus` and
  `deploy.resources.limits.{memory,cpus}` are passed to the runtime as `-m` / `-c`.

- On an interactive terminal, `up` shows a "still working" spinner during long
  silent build phases (context transfer, base-image pull) so it no longer looks
  frozen. Piped/redirected output is unchanged.
- When a build fails from a corrupted builder cache or from the builder running
  out of resources, `up` prints an actionable hint (reset the builder, or give it
  more CPU/memory) instead of leaving you with the raw builder error.
- README troubleshooting for builds: giving the shared builder more CPU/memory
  when a heavy build is slow or fails with `Unavailable`/`EOF`, resetting a
  corrupted builder cache, and trimming a large build context.

### Fixed

- A bare container port in `ports` (e.g. `- "3000"`) now works: it's published
  as `3000:3000` instead of failing with `invalid publish value` (Apple
  `container` requires a host port).
- The Postgres named-volume warning is now actionable: it says the service
  won't start, names the fix (set `PGDATA` to a subdirectory), and tells you to
  re-run `up` — and no longer includes an internal tracking number.

## [0.4.0] - 2026-07-08

### Added

- `profiles` support: services in a profile don't start by default; enable them
  with `--profile <name>` or `COMPOSE_PROFILES`. `run` also honors profiles (a
  gated dependency is an error).
- `up --remove-orphans` removes containers for services deleted from the compose file.
- `--env-file <path>` overrides which file supplies interpolation variables
  (instead of the default `.env` next to the compose file).
- `up` is now idempotent: an unchanged service isn't recreated and an existing
  image isn't rebuilt, so re-running `up` is fast and non-destructive.
- Calmer `up` output: build progress is always shown; harmless warnings move
  behind `--verbose`.

### Fixed

- `up` applies a healthcheck's `timeout` (clamped to 30s for 0/negative values),
  so a hanging probe no longer blocks `up` indefinitely.
- Ctrl-C during `up` rolls back cleanly, killing in-flight build/run/probe
  children and leaving no orphaned containers or network.
- `up --foreground` recreates and attaches even when the service is unchanged.

## [0.3.0] - 2026-07-07

### Added

- `--verbose` prints each `container` command opossum runs, for debugging what's
  sent to the runtime.

### Fixed

- `env_file` now parses multi-line quoted values and `:`-separated entries.
- `env_file` values with an unterminated quote now error clearly, matching
  docker compose, instead of being silently mishandled.

## [0.2.0] - 2026-07-06

### Added

- Bind mounts now expand a leading `~` to the home directory, and a missing bind
  source directory is created before start (matching docker compose) instead of
  failing with `path '~/...' does not exist` (e.g. `~/minecraft_data:/data`).
- `platform:` is passed to `container run --platform`; `linux/amd64` also enables
  Rosetta (`--rosetta`), so an x86-64-only image (e.g. `redislabs/redismod`) runs on
  Apple silicon instead of failing with "does not support required platforms".
- `up` pre-flights published host ports and fails fast with a clear message if one
  is already in use, instead of starting some services and then hitting the
  runtime's raw `bind: address already in use` on a later one. On macOS, a taken
  port 5000/7000 gets an AirPlay Receiver hint (a common surprise).
- `opossum images` lists each service's image, whether opossum builds it
  (`<project>-<service>:latest`) or pulls it, and whether it's present locally —
  the image-side counterpart to `ps`.
- `down --rmi local|all` removes images on teardown: `local` deletes the images
  opossum built for the project, `all` also deletes the pulled `image:` ones, so
  build artifacts from `up`/`build` can be cleaned up.
- Volume seeding + anonymous volumes: a fresh named or anonymous volume is now
  filled from the image's contents at its mount path before the container starts
  (mirroring Docker; Apple `container` mounts a fresh volume empty). This makes the
  common dev pattern — a bind-mounted source plus a volume to preserve the image's
  `node_modules` (e.g. `- /app/node_modules`) — work **unmodified**. A single-path
  entry is treated as an anonymous volume (namespaced per service, removed by
  `down -v`), not a bind mount. Existing volumes are never re-seeded, so data is
  preserved across re-ups.
- After starting a service that publishes ports, `up` prints the host-reachable
  address (e.g. `↳ web on the host: localhost:4200`), so it's clear where to open
  the service — the runtime echoes the container's `<svc>.<project>.<domain>` DNS
  name, which is for container-to-container resolution, not a URL the host can open.
- `opossum stats [service…]` streams live resource usage (CPU %, memory, net,
  block I/O, pids) for the project's containers, like `docker stats`; `--no-stream`
  prints a single snapshot.
- `up` warns when a service mounts a named volume directly at Postgres's data
  directory (`/var/lib/postgresql/data`) without redirecting `PGDATA` to a
  subdirectory — the mount point isn't empty, so `initdb` fails. It's the most
  common snag in real self-hosted app composes. (MySQL/MariaDB are unaffected.)
- tmpfs mounts (`container run --tmpfs <target>`, an in-memory filesystem) via
  either a `type: tmpfs` volume entry or the service-level `tmpfs:` field
  (string or list); both fold together, split out from bind/named `-v` mounts.
- `up` warns when a build context is somewhere Apple's `container` builder can't
  read — under `/private/tmp` or a symlinked directory — with a hint to build
  from the real path, instead of failing opaquely at `COPY` time.
- Long-form `env_file` entries (`{path, required}`) are accepted; an absent file
  marked `required: false` is skipped instead of erroring, so repos that gitignore
  a `.env` run without one.
- Long-form `volumes` entries (`{type, source, target, read_only}`) are accepted
  alongside the short `src:dst[:ro]` string, so real docker-compose files that
  use the mapping form parse and run as-is.
- `build.target` selects a multi-stage build stage (`container build --target`),
  so a service that pins a stage builds that one instead of the final image.
- File-based `secrets` are mounted read-only at `/run/secrets/<name>`, so images
  that read credentials via the `*_FILE` pattern (e.g. `POSTGRES_PASSWORD_FILE`)
  work. Short (`- name`) and long (`{source, target}`) service refs are accepted;
  `external` secrets are rejected and `uid`/`gid`/`mode` are not applied.
- Compose-file discovery: with no `-f`, opossum looks for `compose.yaml`,
  `compose.yml`, `docker-compose.yaml`, then `docker-compose.yml` in the working
  directory, so an existing `docker-compose.yml` runs as-is.
- `env_file` support: a service's `KEY=VALUE` env files are folded into its
  environment (explicit `environment` overrides them).
- `up` warns about compose fields it parses but doesn't act on (e.g.
  `container_name`, `restart`), so they aren't silently ignored.
- `opossum exec [-it] <service> <command>` runs a command in a running
  service's container; flags after the service name pass through to the command.
- `opossum build`, `pull`, `start`, and `kill` (`-s/--signal`) commands, each
  operating on the whole project or named services. See the README command
  support table.
- `opossum run [--rm] [--no-deps] <service> [command]` starts a one-off
  foreground container for a service (distinct name, no published ports); it
  starts dependencies first unless `--no-deps`.
- `opossum config [--services]` validates and prints the resolved compose
  configuration (interpolation and `env_file` applied), noting any ignored
  fields.
- `up` and `config` also surface ignored **top-level** compose keys (e.g.
  `networks`, `volumes`), not just per-service ones.
- `opossum down -v/--volumes` removes the project's named volumes after teardown.
- A top-level volume declared `external: true` is used by its real name (not
  namespaced per project) and is never removed by `down -v`, matching docker
  compose's protection of user-managed volumes.

### Changed

- `up --foreground` now errors immediately when more than one long-running service
  would start (it can only attach to one, and the runtime's foreground `run` blocks
  until the container exits, so the rest would never start). Use it with a single
  service, or drop it to start the whole stack detached. One-shot dependencies
  don't count.
- `ps` now lists only containers that exist: a service that was never created or
  was removed by `down` is omitted (rather than shown as a dead `stopped` row), so
  after a teardown `ps` is empty — matching docker compose. Existing stopped
  containers still appear as `stopped`.
- When a `service_healthy` dependency's container has exited while opossum waits
  for it, `up` now fails fast with `container is not running … check
  \`opossum logs <svc>\`` instead of an opaque "healthcheck did not pass".
- Named volumes are now namespaced by project (`<project>_<volume>`, matching
  docker compose), so concurrent projects that share a volume name no longer
  collide on one global volume — and `down -v` only removes *this* project's
  volumes. Bind mounts are unaffected. (Volumes created by an earlier opossum
  under the bare name are not migrated; recreate them or reference the old name
  explicitly as a bind/`-v` mount.)

## [0.1.0] - 2026-07-03

First tagged release. Everything opossum can do so far.

### Added

- **Dependency-ordered orchestration.** Parse a compose subset, topologically
  sort services by `depends_on` (cycles rejected), start them in order on a
  shared per-project network, and tear down in reverse.
- **Service discovery by bare name.** Each container is named
  `<service>.<project>.<domain>` and searches `<project>.<domain>`, so peers
  resolve one another by their bare service name over the project network.
- **Multiple projects at once.** The `<project>` segment namespaces containers,
  so stacks that share service names run concurrently under a single registered
  DNS domain, each on its own `<project>-net` — no per-project setup. A
  `opossum.project` label + pre-flight guard refuses to clobber another
  project's containers.
- **`depends_on` conditions.** `service_healthy` gates a dependent until the
  dependency's `healthcheck.test` passes (polled via `container exec`, since the
  runtime has no native healthcheck). `service_completed_successfully` runs a
  one-shot dependency to completion and gates on its exit code.
- **`healthcheck`** — `test` (`CMD` / `CMD-SHELL` / string), `interval`,
  `timeout`, `retries`, `start_period`.
- **`.env` / `${VAR}` interpolation** — `$VAR`, `${VAR}`, `${VAR:-default}`,
  `${VAR-default}`, `${VAR:?required}`, and `$$`; values from a `.env` file next
  to the compose file, overridden by the shell.
- **`command` and `entrypoint`** — list form verbatim, string form shell-word
  split; `entrypoint` overrides the image ENTRYPOINT.
- **Commands** — `up [service…]` (whole project, or named services plus their
  dependencies), `down`, `ps` (service / container / IP / ports / status from
  `container inspect`), `logs [service…]` (`--follow`, `-n/--tail`), `stop`, and
  `restart`.
- **Clean-failure semantics.** A failed `up` rolls back the containers it started
  and removes the network if it created it.
- **Two-layer verification.** A fake `container` shim
  (`testdata/fake-container.sh`, kept in sync with the real CLI via
  `testdata/real-cli-output.md`) drives fast, unattended tests of the emitted
  command sequences; a documented real-`container` review
  ([`docs/real-runtime-review.md`](docs/real-runtime-review.md)) confirms
  behavior on macOS 26.

### Fixed

- `ps` no longer reports a published port's `0.0.0.0` host address as a
  container's IP (typed inspect parsing preferring the interface IPv4/IPv6).
- `ps` STATUS now reflects the real `status.state` instead of being inferred
  from whether an IP was assigned.
- A string `command` is shell-word-split, so `command: sh -c "…"` reaches the
  runtime as argv instead of one opaque argument.
- `down` no longer warns when re-run against an already-removed network.

### Known limitations

- Named volumes are passed through untouched; only bind-mount host paths are
  resolved to absolute paths.
- `restart` reassigns a container's IP (the runtime does this on `start`); the
  name and config are preserved, so name-based discovery is unaffected.

[Unreleased]: https://github.com/suruseas/opossum/compare/v0.24.8...HEAD
[0.24.8]: https://github.com/suruseas/opossum/compare/v0.24.7...v0.24.8
[0.24.7]: https://github.com/suruseas/opossum/compare/v0.24.6...v0.24.7
[0.24.6]: https://github.com/suruseas/opossum/compare/v0.24.5...v0.24.6
[0.24.5]: https://github.com/suruseas/opossum/compare/v0.24.4...v0.24.5
[0.24.4]: https://github.com/suruseas/opossum/compare/v0.24.3...v0.24.4
[0.24.3]: https://github.com/suruseas/opossum/compare/v0.24.2...v0.24.3
[0.24.2]: https://github.com/suruseas/opossum/compare/v0.24.1...v0.24.2
[0.24.1]: https://github.com/suruseas/opossum/compare/v0.24.0...v0.24.1
[0.24.0]: https://github.com/suruseas/opossum/compare/v0.23.1...v0.24.0
[0.23.1]: https://github.com/suruseas/opossum/compare/v0.23.0...v0.23.1
[0.23.0]: https://github.com/suruseas/opossum/compare/v0.22.1...v0.23.0
[0.22.1]: https://github.com/suruseas/opossum/compare/v0.22.0...v0.22.1
[0.22.0]: https://github.com/suruseas/opossum/compare/v0.21.0...v0.22.0
[0.21.0]: https://github.com/suruseas/opossum/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/suruseas/opossum/compare/v0.19.1...v0.20.0
[0.19.1]: https://github.com/suruseas/opossum/compare/v0.19.0...v0.19.1
[0.19.0]: https://github.com/suruseas/opossum/compare/v0.18.2...v0.19.0
[0.18.2]: https://github.com/suruseas/opossum/compare/v0.18.1...v0.18.2
[0.18.1]: https://github.com/suruseas/opossum/compare/v0.18.0...v0.18.1
[0.18.0]: https://github.com/suruseas/opossum/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/suruseas/opossum/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/suruseas/opossum/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/suruseas/opossum/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/suruseas/opossum/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/suruseas/opossum/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/suruseas/opossum/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/suruseas/opossum/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/suruseas/opossum/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/suruseas/opossum/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/suruseas/opossum/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/suruseas/opossum/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/suruseas/opossum/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/suruseas/opossum/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/suruseas/opossum/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/suruseas/opossum/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/suruseas/opossum/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/suruseas/opossum/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/suruseas/opossum/releases/tag/v0.1.0
