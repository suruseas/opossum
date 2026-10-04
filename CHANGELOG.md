# Changelog

All notable changes to opossum are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and this project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.42.1] - 2026-10-04

### Fixed

- `logs <service>` and `start <service>` now refuse over the named service's own dependency on a service the file does not define, or whose profile is not active, as docker compose does (`restart`, `exec` and `port` do not read it, there or here). `stop <service>` and `kill <service>` now refuse the same way whether or not a container exists for it yet; they used to refuse only when one did. A dependency written `required: false` is not a fault for a gated service that only its name enables, as there. docker compose reads the file this way when run in its directory, with `-f`, or with `-p` and `-f` together; given a project name by `COMPOSE_PROJECT_NAME` (in the shell or `.env`), or by `-p` with no `-f`, it does not read the file at all and goes on, which opossum does not copy. Only the named service's own dependencies are looked at, where docker compose also follows a gated dependency's own.
- A `${VAR:-word}` or `${VAR:+word}` whose word has a `{` of its own now closes where docker compose closes it. `${CFG:-{x}}` has the word `{x}` (opossum used to stop at the first `}` and leave a stray `}` after the value: `ev}` where docker compose gives `ev`, and `}` for an empty `${E:+{x}}`), and `${E:-{{x}}}` closes one `}` early, as docker compose reads it. A `{` the word never closes, or a `{}`, takes the last `}` of the YAML value that holds the reference, so a `}` after the closing quote, in a comment or of a flow mapping is left alone. A reference whose word has no `{` of its own is read exactly as before. The same holds for values in a `.env` file, each read as one value.
- An `entrypoint:` list that starts with an empty word and has more words after it (`entrypoint: ["", "x"]`) is now refused with a message, as docker compose fails to start it (`exec: "": executable file not found`). Before, `container` 1.5.0 read the empty `--entrypoint=` as taking the next word on the line for its value, so a word of the entrypoint was pulled as if it were an image, and a different image could be started.
- An `entrypoint:` that names no program (`["", "sh"]`, `[""]` with no `command:`, `[]` with a command whose first word is empty, or `[]` with neither a command nor an image CMD to run) is refused as one line, without a failure's advice. Opossum refuses these itself, before it starts a container, so there is no runtime failure above it, no container to read logs from and nothing in the image, command or mounts to verify; the advice that said so (`there is no container left to read logs from — the failure above is what there is; verify the image, command, and mounts in the compose file`) was wrong for them, as was `exited non-zero — check its output above, or run it directly with opossum run` for a `service_completed_successfully` dependency. This holds under `up`, `up --dry-run` and a foreground `up`.

## [0.42.0] - 2026-10-02

### Fixed

- `down` no longer asks the restart supervisor of a project to stop twice. It stops it before the compose file is read, under the name it can work out without the file, and then once more after loading the file; when the first attempt could not confirm that the supervisor was gone, the second waited out the whole stop budget again (up to about 8 seconds in all) and printed the same `[OPSM-414]` warning a second time. The project is now asked once. A project whose file gives it a name of its own (`name:`) is another supervisor's and is still asked.
- `entrypoint: [""]` (a list of one empty word) together with a `command:` now takes the image's own ENTRYPOINT away and runs the command in its place, as docker compose does. Before, the service did not start: `container` 1.5.0 reads an empty `--entrypoint=` as taking the next word on the line for its value, so the image name was read as a command and a different image was pulled. With no `command:` it is refused, as docker compose refuses it (`no command specified`) — unlike `entrypoint: []`, which runs the image's own CMD.
- `up --dry-run` no longer plans a pull of an image the build makes, for a service that has `build:` and writes `entrypoint: []` with no `command`. The plan listed `container image pull <project>-<service>:latest` after the build, a step the real `up` never takes (the build has made the image, and no registry has it). A service with an `image:` of a registry still has its pull planned before the run.
- A short `volumes` mount with nothing before its first colon (`:/b`, `:/b:ro`, `:ro` — which is what `${DATA}:/data` gives when `DATA` is not set) or nothing after its second (`/a:/b:`) is now refused when the file is read, as docker compose refuses it (`empty section between colons`); before, the runtime took the empty source for an anonymous volume and started the service. Taking down a project that an earlier version started with such a mount warns and goes on rather than refusing the file. A bare `:`, a one-byte section after a leading colon (`:a`) and a middle section that is a single Unicode letter (`/a:a:`, `/a:é:`, or a Chinese or Greek letter, which docker compose takes for a Windows drive) are read as before; a middle section of any other one character (`/a:1:`, `/a:_:`, a space) is refused, as docker compose refuses it.

## [0.41.0] - 2026-10-01

### Changed

- Checked against Apple `container` 1.5.0 on a real runtime: `up`, `ps`, `images`, `doctor`, `down` and `config` behave as on 1.4.1, with no exit code changes and no JSON keys gone. The only wording change opossum could see is `container images ls` (plural) now saying `unknown command 'images'`, which opossum does not rely on. The README and the compatibility notes now name 1.5.0 as the verified version.

### Fixed

- `opossum stats --host` no longer refuses a file whose active services form a
  `depends_on` cycle: like `stats`, it only reads containers that are already there.
  `up`, `run` and the other commands that put the services in order still refuse it.
- In a file that is only extended from, a service that docker compose does not take is no longer refused for a value of the wrong kind in the keys docker compose asks nothing of there — `hostname`, `container_name`, `use_api_socket`, `attach`, `logging`, `blkio_config`, `extra_hosts`, `security_opt` and some thirty more: docker compose reads them as they are. The keys it reads into a type before it knows whether a service is taken (`ports`, `volumes`, `build`, `depends_on`, `env_file`, the numbers and booleans it casts) are still asked there, a few keys the loader reads itself (`command`, `image`, `user`, …) are still refused for a value of the wrong kind, and nothing changes for the service that is taken.
- When a service extends a service of another file (`extends: {file: …, service: …}`),
  only that service, and the services of the same file it extends in turn, are now read
  for their own `extends`. A file that names itself as the file to extend from
  (`extends: {file: compose.yaml, service: base}` inside `compose.yaml`) is no longer
  refused as a cycle, and another service of the extended file may extend a file that is
  missing, a service that is not there, or a file holding a number docker compose cannot
  read (`.inf`): docker compose reads none of that. A cycle in the taken chain, and a
  taken chain that runs into a missing file or service, are still refused.
- A key a later `-f` file writes with nothing after it (`dns:`, `hostname: ~`,
  `deploy: {mode: ~}` …) is refused when no earlier file gave that key a value, as
  docker compose refuses it. Where an earlier file did give one, the key is still read
  as "not given" and the earlier value stands — except for `depends_on`, `logging` and
  `networks`, and for `healthcheck.test`, `logging.driver` and each entry of `ulimits`,
  which docker compose refuses there too and so does opossum. Stopping, killing and taking
  a project down still read such a file and warn.
- `attach` written as a string that reads as no boolean (`attach: "7"`, `attach: abc`) on a service that docker compose does not take from a file only extended from is no longer refused; docker compose reads it there. It is still refused on the service that is taken and on the services it extends in turn, as before.
- A file that another service `extends` from is now refused for a `cpus` that reads as no
  number (`cpus: abc`, `"0x2"`, `" 2"`) in a service that is not the one taken, as docker
  compose refuses it there; `cpus: "1e2"` and `"1_0"` read, there and everywhere.
- A file that another file extends from is no longer refused for a value of the wrong
  kind in the `deploy.resources` of a service that is not the one taken (`limits: abc`,
  `limits: {cpus: true}`, `reservations: {memory: ~}` …): docker compose reads none of it
  there. The service taken, and the services it extends in turn, keep their checks.
- A key of a service's `deploy`, `build`, `healthcheck`, `networks` entry or `depends_on`
  entry written with nothing after it (`deploy: {labels: }`, `build: {network: }`,
  `healthcheck: {disable: }`, a network's `aliases:`) is now refused where docker compose
  refuses it, in a single file as in a later one with no earlier value for it. A label, a build
  argument and a network entry with nothing after them are still read, as docker compose reads
  them. Stopping, killing and taking a project down still read such a file and warn.
- On Linux, a project's watching supervisor is now recognised by its start tick from `/proc` rather than the start time `ps` prints. On a machine whose clock is being corrected (WSL2, for one) that time can read one second apart for the same process, which made a second supervisor take a live claim over as if it were stale.
- A build that fails because the builder ran out of memory (`Error: resourceExhausted: …`, as Apple `container` 1.5.0 reports it) now gets the hint about giving the builder more resources. Before, only the generic line about importing from Docker was printed: the hint matched other wordings (`rpc error: code = Unavailable`, `error reading from server: EOF`), not this one.
- `config` now prints `entrypoint: []` for a service that writes an empty entrypoint (`entrypoint: []` or `entrypoint: ""`), as docker compose does. It used to leave the key out, so the printed file did not say that the image's own entrypoint was meant to be taken away. A later file's empty entrypoint over an earlier one's words, an alias of an empty list, a merge key and an `extends` of a service that writes one are read the same way; a null still removes the key.
- `entrypoint: []` (or `""`) now takes the image's own ENTRYPOINT away when the service is started or run, as docker compose does. It used to be ignored, so the image's entrypoint ran with the command after it. `container run` has no way to say "no entrypoint" (an empty `--entrypoint` leaves the image's in place, measured on `container` 1.5.0), so opossum makes the command's first word the entrypoint — or, when the service has no `command`, the first word of the image's own CMD, pulling the image first if it is not here yet to read it. A service with neither is refused, as docker compose refuses it. A running service that writes `entrypoint: []` — one that was started by an earlier version too — is recreated on the next `up`. A command whose first word is empty is refused. `entrypoint: [""]` (a list of one empty word) is not this: it is passed on as it is, and the service does not start, because `container` 1.5.0 reads an empty `--entrypoint=` as taking the next word on the line for its value.
- `${VAR:+word}` and `${VAR+word}` — "word when the variable is set" — are now read, as docker compose reads them; they were refused as `invalid variable name`. `${VAR:+word}` gives the word when `VAR` is set and not empty and nothing otherwise, `${VAR+word}` when `VAR` is set at all (even to nothing); the word may itself hold `${…}` references and is read only when it is taken, so a `${F:?…}` in it asks for `F` only then. A line such as `command: ["serve", "${TLS:+--tls}"]` loads.

## [0.40.1] - 2026-10-01

### Fixed

- A file that another service `extends` from is no longer refused for a `deploy.mode` or a
  port `mode` that is not a string, a port `name` or `app_protocol` that is not a string, a
  `host_ip` or `protocol` that is null (`~`), or a `published` that is a list, a mapping or a
  `!!float` (`!!float 8080`), in a service that is not the one taken (nor one it extends in
  turn): docker compose reads none of those there. The service taken is still asked for all
  of it, and a short port entry that is a float is still refused anywhere.
- A file that another service `extends` from is no longer refused for a `deploy.replicas`
  of `1.5`, `true`, an empty value, a list or a mapping in a service that is not the one
  taken (nor one it extends in turn): docker compose reads only the services it takes of
  such a file, and asks the others for a whole number only when it is written as a string
  (`"two"`, `""`). The service taken is still asked for it all.
- A file that another service `extends` from is no longer refused for a byte size or a
  duration that reads as none (`stop_grace_period: abc`, `shm_size: abc`,
  `mem_reservation: "1x"`), for a number written as a string that is out of bounds
  (`oom_score_adj: "2000"`, `cpu_count: "-5"`, `cpu_percent: "150"`), or for a name of no
  characters (`sysctls: {"": 1}`), in a service that is not the one taken (nor one it extends
  in turn): docker compose reads none of those there. The service taken is still asked for all
  of it, and a string that reads as no integer, boolean or number is still refused anywhere.

## [0.40.0] - 2026-09-30

### Added

- A new `docs/getting-started.md` (Getting started) page walks someone who has
  never used Docker Compose from an empty directory to a web server and a
  database running together and back to no containers, network or data left, with the first-run
  pitfalls (arm64 images, named volumes, service names, ports 5000/7000)
  explained in plain terms. It is the first page on the documentation site and
  is linked from the top of the README's quickstart in both languages.

### Fixed

- The compose file loader now refuses a string docker compose does not read as a
  byte size in `shm_size`, `mem_reservation`, `memswap_limit` and `mem_swappiness`
  (`"abc"`, `"1x"`, `" 1g"`, `"0x10"`), and one it does not read as a duration in
  `stop_grace_period` (`"abc"`, `"10"`, `"1S"`, `"1 s"`). A number with a unit
  (`64m`, `1.5 GiB`) and a duration such as `10s`, `1m30s` or `1d` still load.
  `down`, `destroy`, `stop` and `kill` name it on stderr and go on, so a project
  an earlier version started from such a file still comes down (`shm_size` is
  also read by opossum itself, which used to take some values docker compose
  refuses — `" 1g"`, `"1  g"`, `"1ib"`, an empty string — and now refuses them,
  as docker compose does).
- The compose file loader now refuses a `cpu_percent` given as a string
  docker compose would refuse: one that does not read as a number at all
  (`cpu_percent: "abc"`), one with a fractional part (`"7.5"`), or one
  outside the 0–100 range docker compose's schema gives it (`"150"`,
  `"-1"`). A whole number in range, however it is spelled (`"7.0"`, `"7e0"`,
  digit separators like `"1_00"`), still loads, matching docker compose
  v5.5.1's own cast for this key.
- `opossum import` now skips a build service whose profile is not active, as
  `build` and `pull` do, instead of trying to bring in an image for it. Naming
  the service, or turning its profile on with `--profile` or `COMPOSE_PROFILES`,
  imports it as before.
- The compose file loader now refuses a string value outside the range docker
  compose's schema gives the key, for two more keys: `oom_score_adj` outside
  -1000 to 1000 (`"2000"`, `"-1001"`) and `cpu_count` below 0 (`"-1"`). Values
  on the edge (`"1000"`, `"-1000"`, `"0"`) and spelled with a sign (`"+5"`)
  still load, and keys the schema does not bound (`cpu_shares`, `pids_limit`)
  are unchanged.
- The compose file loader now refuses a negative `scale` and a negative
  `deploy.replicas` (`scale: -1`, `deploy.replicas: "-1"`), as docker compose
  does with `must be greater than or equal to 0`. The count is asked of the
  merged project, so a base file's negative value that an override sets back
  to zero or more still loads. Zero and a positive count, including one written
  with a sign (`"+2"`), still load, and so does a service behind `profiles:`
  (docker compose refuses that one only once the profile is active).
- The compose file loader now refuses a service that sets `scale` and
  `deploy.replicas` to two different numbers, as docker compose does
  (`can't set distinct values on 'scale' and 'deploy.replicas'`). A number and a
  string that reads as the same number (`"2"`, `"+2"`, `"02"`), one of them alone,
  and a service behind `profiles:` still load. `down`, `destroy`, `stop` and
  `kill` name it on stderr and go on, so a project an earlier version started
  from such a file still comes down.
- The compose file loader now refuses a `deploy.replicas` that does not read as a whole
  number (`replicas: two`, `1.5`, `true`, an empty value, a list), as docker compose does;
  `replicas: 2`, `"2"` and `2.0` are read as before. In a file given with `-f`, `down`,
  `destroy`, `stop` and `kill` name it on stderr and go on, so a project an earlier
  version started from such a file still comes down.
- `down`, `destroy`, `stop` and `kill` no longer refuse a compose file over a
  value of these kinds that an earlier opossum accepted and that the loader now refuses (a string
  outside a key's bounds such as `oom_score_adj: "2000"` or `cpu_count: "-1"`,
  a string that is not a number or a boolean, `use_api_socket: "true"`, a negative
  `scale` or `deploy.replicas`), wherever the file reads it from, an included or
  extended file too. They name it on stderr and go on, so a project started from
  such a file comes down from its own directory; `up`, `run`, `config` and the
  other commands still refuse it.
- `down`, `destroy`, `stop` and `kill` now also go on past three more things in the
  compose file that 0.38.0 started refusing and an earlier version took: a key
  written twice inside a mapping such as `sysctls`, `extra_hosts` or
  `logging.options`, an alias that refers to the block that contains it (in an
  `x-` extension), and a `---` after the one document the file holds. A project started from such a file
  comes down from its own directory with the first one named on stderr; `up`,
  `run`, `config` and the other commands still refuse it.
- `down`, `destroy`, `stop` and `kill` now also go on past a value of the wrong
  kind or a number outside its bound in a service key that the loader started
  refusing in 0.38.0 (`hostname: [a, b]`, `pids_limit: true`, `shm_size: [a]`,
  `oom_score_adj: 2000`, `cpu_count: -1`). A project that an earlier version
  started from such a file comes down from its own directory, with the value
  named on stderr; `up`, `run`, `config` and the other commands still refuse it.
- `down`, `destroy`, `stop` and `kill` no longer stop or remove another project's
  containers when a compose file of several YAML documents comes to a different
  project name than the one its first document gives (a later document's `name:`,
  including one written with `${VAR:-default}`, an alias or an empty value).
  0.38.0 started reading every document, so the later name won, and a project an
  earlier version had started from the first document was left running while the
  containers of a project named by a later document went down. They now refuse and
  ask for the name (`opossum -p <name> down`); a name given with `-p`,
  `COMPOSE_PROJECT_NAME` or the project's `.env` is taken as before, and every
  other command reads the file as it did.
- `opossum down` no longer stops a restart supervisor, or deletes its record, when
  it refuses a compose file of several YAML documents that comes to a different
  project name than the first document gives and asks for the name
  (`opossum -p <name> down`). The supervisor was stopped by a name guessed without
  the file, which left the project's containers running with nothing watching them
  or, where the guessed name was another project's, stopped that project's.
- The compose file loader now refuses a `scale` or `deploy.replicas` written as
  a whole number from 9223372036854775808 to 18446744073709551615, as docker
  compose does (it reads such a number as less than 0). A count from 0 up to
  9223372036854775807 loads as before.
- The restart supervisor's record of which services it is watching is now
  replaced whole, so a reader that looks while it is being written sees the old
  set or the new one and never an empty file. An empty read made `up` take a
  supervisor that was fine for one watching a different set and replace it.
- Two `opossum up` commands started at the same moment for one project no longer
  start two supervisors that restart the same containers: the supervisor's claim is
  taken under a lock and written whole, so a second claim never reads the first one
  as empty and takes its place.
- The compose file loader now refuses an infinity or a NaN written as a number
  (`.inf`, `-.inf`, `+.inf`, `.nan`) anywhere in a file, as docker compose does:
  it cannot write one into the model it checks, so it refuses the whole file. A
  string is read as before (`".inf"`, `!!str .inf`, and a value a `${VAR}` expands
  to). `down`, `destroy`, `stop` and `kill` name it on stderr and go on, so a
  project an earlier version started from such a file still comes down.
- The compose file loader now refuses a value tagged `!!float` that is not a
  number docker compose can read (`!!float abc`, `!!float inf`, `!!float 1.0e999`),
  as docker compose does; `!!float 1.5`, `!!float 0x10` and `!!float "1.5"` are
  read as before. In a file given with `-f`, `down`, `destroy`, `stop` and `kill`
  name it on stderr and go on, so a project an earlier version started from such a
  file still comes down; in a file that `extends` or `include` reads, every
  command refuses it, as every earlier version did.
- A file that another service `extends` from is no longer refused for an infinity or
  a NaN written as a number (`.inf`, `.nan`) in an `x-` key or a volume label of
  it, or in an `x-` key of a service that is not the one taken: docker compose
  reads only the named service of such a file, and now so does this check. In the
  named service, in the services of that file it extends in turn, and in what an
  alias in it points at, it is refused as before. A value the extending service
  writes again, or resets, is still checked in the file it came from, and so is a
  `.inf` in a `labels`, `environment`, `healthcheck` or `build` of a service that
  is not taken, which an older check of those keys refuses (docker compose does
  not).
- The compose file loader now refuses a value tagged `!!int`, `!!bool`, `!!null`,
  `!!timestamp` or `!!binary` that is not a value of that type (`!!int abc`,
  `!!int 1.5`, `!!bool yes`, `!!null x`, `!!timestamp x`), as docker compose does,
  and as it already did for `!!float`; `!!int 0x10`, `!!bool TRUE`, `!!null ~` and
  `!!timestamp 2001-12-14` are read as before. An integer tag over a `${VAR}`
  (`!!int ${PORT}`) is refused too, since docker compose reads the text before it
  expands it. In a file given with `-f`, `down`, `destroy`, `stop` and `kill` name
  it on stderr and go on, so a project an earlier version started from such a file
  still comes down.
- The compose file loader now refuses `!!int -0`, as docker compose does; `!!int 0`,
  `!!int +0`, `!!int -00` and `!!int -0x0` are read as before. In a file given with
  `-f`, `down`, `destroy`, `stop` and `kill` name it on stderr and go on, so a project
  an earlier version started from such a file still comes down.
- The compose file loader now refuses a mapping key that is not a string, wherever
  it stands (`1: a`, `true: a`, `~: a`, `!!int 1: a`, an alias to one), as docker
  compose does; a quoted key (`"1": a`), a `!!str` key and `yes:` are read as
  before. The most common place is an `x-` extension block. In a file given with
  `-f`, `down`, `destroy`, `stop` and `kill` name it on stderr and go on, so a
  project an earlier version started from such a file still comes down.
- The compose file loader now refuses a port written as a float (`ports: [!!float 80]`,
  or a long-form `published: !!float 8080`), as docker compose does: a port is an
  integer or a string. `!!int 80`, `!!str 80`, `80` and `"80"` are read as before. In a
  file given with `-f`, `down`, `destroy`, `stop` and `kill` name it on stderr and go
  on, so a project an earlier version started from such a file still comes down.
- The compose file loader now refuses a `<<` merge key that holds no mapping and no
  list of mappings (`<<: v`, `<<: ~`, `<<: [a, b]`), wherever it stands, as docker
  compose does; `<<: *anchor`, `<<: [*a, *b]` and `<<: []` are read as before. In a
  file given with `-f`, `down`, `destroy`, `stop` and `kill` name it on stderr and go
  on, so a project an earlier version started from such a file still comes down.
- The compose file loader now refuses a name of no characters in `sysctls`,
  `extra_hosts` and `annotations` (`sysctls: {"": 1}`), as docker compose does. In a
  file given with `-f`, `down`, `destroy`, `stop` and `kill` name it on stderr and go
  on, so a project an earlier version started from such a file still comes down.
- The compose file loader now refuses a number, a bool, an empty value or a list as
  `ports[].mode` or `deploy.mode`, as docker compose does (a string is wanted: `host`,
  `ingress`, `replicated`, `global`). In a file given with `-f`, `down`, `destroy`, `stop`
  and `kill` name it on stderr and go on, so a project an earlier version started from
  such a file still comes down.
- The compose file loader now refuses a long-form port whose `name` or `app_protocol`
  is not a string, whose `host_ip` or `protocol` is null (`host_ip: ~`), or whose `published` is a
  list or a mapping, as docker compose does. In a file given with `-f`, `down`,
  `destroy`, `stop` and `kill` name it on stderr and go on, so a project an earlier
  version started from such a file still comes down.

## [0.39.0] - 2026-09-29

### Changed

- The `[OPSM-206]` notice about an auto-assigned host port now says
  "opossum publishes it on `<port>` instead", not "published" — the past
  tense read as a settled fact even though the run can still be refused or
  fail to start afterward, in which case nothing was actually published on
  that port. The present tense states opossum's plan rather than an outcome,
  matching the notice's own pointer to `opossum ps` for what is actually
  running.

### Fixed

- `up` and `run` no longer fail with exit 64 ("Missing value for...") when a
  compose file's `environment:`, `labels:`, `user:`, `working_dir:`,
  `tmpfs:`, `entrypoint:` or a service's network name starts with `-`.
  container 1.4.1 reads a value starting with `-` as another flag when it is
  passed as a separate argument, so opossum now passes these as a single
  `--flag=value` argument instead, which container 1.4.1 reads correctly.
  `docker compose` already tolerated such values.
- `ps`, `images`, `logs`, `stop`, `kill`, `down`, `destroy`, `restart` and
  `stats` no longer refuse a project whose compose file has a dependency
  cycle among its active services, when run with no service named — they
  only touch containers already there and never start anything in
  dependency order, so a cycle is no longer their business to refuse. A
  project brought up before a cycle was introduced can now be brought back
  down through opossum instead of requiring `container rm`/`container
  network rm` by hand. Commands that start, build or bring a service into
  being — `up`, `run`, `pull`, `build`, `import`, `start` and
  `config --services` among them — still refuse such a file, as before.
- A service behind an inactive `profiles:` gate no longer makes `opossum`
  refuse the whole project over one of its own dependencies naming a service
  the file does not define anywhere. docker compose does not read a
  gated-off service's `depends_on` at all, and `opossum` now agrees; once
  that service's profile is active (`--profile`, `COMPOSE_PROFILES`, or
  naming the service directly), the same fault is refused again. This check,
  and the existing one for a dependency on another inactive profile, now run
  for every command that reads the project without starting, stopping, or
  removing it — `ps`, `logs`, `exec`, `images`, `port`, `volumes`, `restart`,
  `stats`, `cp`, `build`, `pull`, `import`, `start`, and `watch` — matching
  `up`, `run` and `config`, which already read the project this way. Given a
  service name, `stop` and `kill` still check neither fault (unchanged by
  this).
- `logs`, `start`, `restart`, `stop`, `kill` and `stats`, when given the name
  of a service that has no container yet (behind a profile never turned on,
  or one `up` did not reach), now pass it by silently instead of failing or
  claiming to act on it — matching docker compose, and matching how these
  commands already behaved when run over the whole project. Naming a service
  that is not defined at all is unaffected and still refuses.
- `down --rmi local` now removes an image built under a custom `image:` name
  when it was actually built by this project — opossum's own build labels it
  with both `opossum.project` and `opossum.service`, required together, or an
  imported docker compose build's `com.docker.compose.project` alone — instead
  of always leaving it behind. A build with no `image:` was already removed
  under opossum's own name; this closes the gap for one that names its image.
  `--rmi all` removes it regardless, as before. An image built by opossum
  before this labeling pair existed is recognized once rebuilt.
- A `volumes_from` naming a service the file does not define, or a
  `container:` entry (whose mounts opossum cannot read, since they live
  outside the compose file), no longer makes `opossum` refuse the whole
  project when the service that names it is behind an inactive
  `profiles:` gate. docker compose reads `volumes_from` as part of the same
  dependency graph as `depends_on`, and does not look at a gated-off
  service's `volumes_from` either, and `opossum` now agrees. Once that
  service's profile is active (`--profile`, `COMPOSE_PROFILES`, or naming it
  directly), the same fault is refused as before.
- The `[OPSM-201]` line that appears when a start fails on a host port
  conflict the pre-flight check could not see beforehand now names this
  project's own service if a running container of it holds the port (the same
  message the pre-flight check itself gives), instead of always sending the
  reader to remap a compose file that is not the problem. This is reachable
  when one service publishes a host-port range that overlaps a number another
  of the run's own services asks for individually: neither the pre-flight's
  own probe nor its check for two entries naming the same host port reads a
  range, so both pass beforehand, and the range's service binding first is
  what actually creates the conflict. A conflict with something genuinely
  outside the run still gets the previous "remap the file" wording, which is
  the right fix there.
- `up` no longer refuses to place an auto-assigned host port (`[OPSM-212]`,
  "could not place it on any of them") when another service's `ports:` range
  reaches all the way to 65535 and happens to leave nothing above the port
  the operating system first offered. The search now walks downward from
  there once it runs off the top, instead of asking the operating system for
  another ephemeral port — which tends to land back inside the same range,
  since both draw from the same high end of the port space. Verified on
  container 1.4.1: a claimed range up to 65535 that previously failed most of
  the time now places the port below the range every time (5 of 5 runs).
- `ps` and `port` now show every host port a published range actually covers,
  not just the first one. Apple `container` 1.4.1 reports a range as one entry
  with a count rather than one entry per port, and `ps`'s `PORTS` column and
  `port`'s lookup only read the first — so `ps` showed one line for a service
  publishing three ports, and `port <service> <container-port>` refused a
  container port in the middle of a range even though it was really bound,
  telling the reader it wasn't published when it was.
- `up --foreground` (and any other foreground/one-off run) now keeps both the
  start and the end of a failed run's captured stderr instead of only the
  start, so a hint like the arm64-build one still appears when a long pull's
  progress lines pushed the actual `Error:` line past the old 8 KiB
  head-only cap.
- `up`, after a failed bring-up, no longer hands a service it never got to —
  because an earlier one in the startup order failed first — to the restart
  supervisor just because the runtime could not answer whether it has a
  container. Whether that answer is trusted as "still there" now depends on
  whether the service already had a container when this `up` began: one that
  did keeps its supervision through the outage, as before; one that never had
  a container from this run or any earlier one is no longer added on the
  strength of an unanswered question alone.
- The compose file loader now refuses a service key whose value is a string
  docker compose cannot cast into the type it needs, for the keys opossum
  does not read itself and only checked for the right general kind (a string
  is the right kind for `cpu_shares: "abc"`, since docker compose reads
  strings into these keys — but `"abc"` does not read as an integer, and
  docker compose refuses it). Covers the boolean-cast keys (`attach`,
  `oom_kill_disable`, `privileged`, `stdin_open`) and the integer-cast keys
  (`cpu_count`, `cpu_period`, `cpu_quota`, `cpu_rt_period`,
  `cpu_rt_runtime`, `cpu_shares`, `oom_score_adj`, `pids_limit`, `scale`),
  matching docker compose v5.5.1's own cast rules (plain decimal integers
  only — no hex, digit separators, or fractional spellings of whole numbers;
  the YAML 1.1 boolean words, case-insensitively).
- `opossum run` now gives the same actionable hints `opossum up` does when the
  runtime refuses to start the one-off before making a container — an image
  with no build for this platform (`[OPSM-412]`), for example — instead of
  only the runtime's raw error. Verified on container 1.4.1: a `run` of an
  `amd64`-only image now prints `[OPSM-412]` with the `platform:` fix,
  matching `up`. This is applied only where the run made no container, so a
  job that happens to print similar words after starting is not misread as
  the runtime's refusal.
- `stop` and `kill`, given a service name, now refuse when that service
  already has a container here to act on and depends on one the file does
  not define at all, or on one behind a `profiles:` gate that is not active
  and required (`required: false` still excuses it) — the same faults `up`
  would refuse over. A named service with no container yet is unaffected: an
  earlier `opossum` never started it over the bad dependency, so
  `stop`/`kill` still go ahead with whatever else was named. docker compose
  refuses the same way, once it too has a container to resolve the
  dependency for (measured on v5.5.1).
- The host-port conflict message no longer suggests AirPlay when it has
  already named one of this project's own containers as the holder of the
  port. Showing both together read as contradicting advice — the message
  named the actual holder, then guessed it might be AirPlay Receiver instead.
  The guess is now shown only when there is no holder of this project's to
  name.
- `down`, `destroy`, and `up` (when it replaces a supervisor for a changed
  compose file) now warn (`[OPSM-414]`) when they asked this project's restart
  supervisor to stop but could not confirm it did within the time waited,
  instead of silently proceeding as though every watcher was already gone. Run
  `opossum ps` to check, and stop it by hand if it's still there.
- `volumes_from` naming a holder whose anonymous volume (`- /data`) would
  become a second, wrongly-named volume here — something opossum cannot
  mount the way docker compose shares it — no longer refuses loading the
  compose file for a service behind a `profiles:` gate that is off; the
  refusal is now deferred to the moment that service turns out to be active,
  the same way an undefined `volumes_from` target and a `container:` entry
  already were. A service that itself lends the same volume on to a further
  service is unaffected and still refused right away, whatever its own gate
  says.
- `down --rmi local` no longer removes an image built under a custom
  `image:` name based on an `opossum.project` or `com.docker.compose.project`
  label a `FROM` chain merely carried forward from an unrelated base image.
  Recognizing opossum's own builds now requires its `opossum.project` and
  `opossum.service` labels to both be present and match, instead of trusting
  either alone — an image descended from a base a different project built no
  longer reads as proof that this project made it.
- `down --rmi local` no longer removes a hand-built image that merely shares
  a tag with a service's `image:` and happens to descend from a base this
  same project built for a *different* service — opossum's own build labels
  now name the service as well as the project, and both are required to
  match before an image is recognized as this build's own.
- `up`, when it cannot confirm an old restart supervisor stopped and so
  leaves it in place instead of replacing it (`[OPSM-414]`), now also names
  any service this very run just gave a `restart:` policy that the old
  supervisor never knew about — those are watched by nobody until the old
  supervisor is stopped by hand and `up` is run again.
- `down`/`destroy`'s `[OPSM-414]` notice (printed when the old restart
  supervisor's stop could not be confirmed) now starts with `opossum: `, the
  same prefix `up`'s own equivalent notices already use — it used to print
  with no prefix at all, reading differently from every other command that
  can show this same warning.
- `build`, `pull` and `import` now check a gated (`profiles:`) service's own
  dependencies once it is named on the command line, the same way naming it
  does for `up`: a named service that depends on one the file does not
  define, or on another gated-inactive service, is refused instead of being
  silently built or pulled with a dependency `opossum` never checked. docker
  compose refuses the same way, whether or not a container already exists for
  the service (measured on v5.5.1). Not naming the service is unaffected — its
  dependency stays nobody's business while its profile is off. Naming any
  service for these three now also refuses a dependency cycle among the
  services already active (ungated) in the file, which naming used to skip
  entirely — as docker compose does too (measured on v5.5.1).

## [0.38.0] - 2026-09-27

### Fixed

- A network declared with a `name:` of its own (one that is not `external`) and
  joined by a service that lists it is now created under that name, as docker
  compose creates it, instead of as `<project>-<key>`, so another compose file or
  project can join it by that name. The network is made with the project's label,
  and `down` and `destroy` remove it only when that label is there: a network of
  that name that was already there, made by hand or by another project, is used by
  `up` and left alone. The `<project>-<key>` network an earlier version made for
  such a key is still removed. A `name:` the container runtime cannot create (upper
  case, `+`, more than 63 characters) is refused by `up` and `run` before anything is
  created, naming what it takes, and two networks with one `name:` are one network
  when declared alike and refused when declared differently.
- When `up` fails because the runtime refused to run a service and left no
  container of it — an image with no build for this machine, or a named volume
  that would not attach — the rollback line no longer says `Rolled back web —
  stopped and removed`, and for the image case the error no longer points at
  `opossum logs web`. Whether the run left a container is now asked of the
  runtime, not read off the failure's wording; when the runtime cannot be asked,
  the service stays on the list. A service whose previous container this `up`
  removed to replace it is still named.
- Whether a failed run-to-completion service (a `service_completed_successfully`
  target) stops `up` is now decided by the dependents `up` starts, as in docker
  compose. A required dependent behind a profile that is not on, or one you did not
  name (`up migrate` with the dependent left out), no longer makes the failure fatal
  and rolls the whole `up` back: the failure is noted and `up` goes on. Turning the
  profile on, naming the dependent, or a started required dependent still stops it,
  and so does `run` of a service that requires it.
- In a single compose file, a key written twice in the same mapping is now refused
  wherever the mapping is, as docker compose refuses it (`mapping key "k" already
  defined`). It was accepted in `logging` (and its `options`),
  `deploy.resources.reservations`, `sysctls`, `ulimits`, `extra_hosts`, a network's
  `driver_opts`, a gpu device and a mount's `bind:` block — read past and listed
  among the ignored fields, except in `ulimits`, where the second value was used. A
  block that an alias of its own contains (`x-a: &a {b: *a}`) is refused too
  (`cycle detected`).
- With container 1.4.1, pulling an amd64-only image for the first time fails with
  `Error: unsupported platform Platform(…)`, which `up` did not recognise, so the
  `platform: linux/amd64` hint (`[OPSM-412]`) did not appear; it appeared once the
  image was local. It now appears for a fresh pull too. An image missing amd64
  instead gets no arm64 advice.
- When the runtime refuses to run a run-to-completion dependency (a
  `service_completed_successfully` target) — an image with no arm64 build, a named
  volume another container holds — `up` now gives the diagnosis it gives for a
  long-running service for the same failure (`[OPSM-412]`, `[OPSM-103]`) instead of `did not complete
  successfully … exited non-zero`. A dependency that ran and printed such a line
  itself is still reported as having exited non-zero.
- `up --foreground` no longer reports a service as having hit a host port
  conflict (`[OPSM-201]`), an image with no arm64 build (`[OPSM-412]`) or a bind
  mount that could not be resolved (`[OPSM-107]`) because the service itself
  printed those words and exited. A foreground run's output includes the
  container's own, so these diagnoses (and `[OPSM-103]`, the volume another
  container holds) are now given only when the runtime refused the run and made no
  container — which is what a port it cannot bind, an image with no arm64 build, a
  mount it cannot resolve and a volume it will not attach do. A service that ran
  and failed reads as a plain failed start. A detached `up` is unchanged.
- A compose file that holds more than one YAML document (`---` between them) is
  now read as docker compose reads it: each document is merged into the ones before
  it, as several `-f` files are (the later value wins, `command` is replaced, `ports`
  append, `name:` is the last one's). The later documents used to be dropped without
  a word — a service the second one defined was never started. A document that is
  empty or not a mapping (a trailing `---`, two in a row) is refused as docker
  compose refuses it, and so is a later document that does not parse. Known
  differences: an alias to an anchor of an earlier document is refused, and an
  included or extended file of several documents is refused rather than read as its
  first alone.
- A service key opossum does not act on is now held to the shape docker compose
  gives it, as the keys opossum reads already were: `hostname: [1, 2]`, `dns: 7`,
  `sysctls: [1]`, `privileged: 7`, `container_name: x` (the pattern wants two
  characters) and the like are refused (`services.web.hostname must be a string`)
  where they loaded and were listed among the ignored fields. 58 keys; the shapes
  are those of docker compose v5.5.1's schema.
- The `default` network is now one network, as in docker compose: a service that
  lists no `networks:` and a service that lists `default` are on the same one, and
  the file's declaration of it (`name`, `internal`, `external`, `labels`, `ipam`) is
  that network's. A service may list `default` without declaring it (`networks:
  [default, backend]`), which was refused as an undefined network. Before, the two
  kinds of service were on different networks (`<project>-net` and
  `<project>-default`) and a declaration of `default` reached only the second, so
  with `default: {internal: true}` a service that listed no networks now becomes
  host-only, and a service that listed `default` moves from `<project>-default` to
  `<project>-net`.
- `up` and `run` now refuse a network that is already there in another mode than the
  file declares it (host-only where the file says not, or the other way round), saying
  to run `opossum down` and `up` again (`[OPSM-207]`). Before, they went on with the
  network that was there while warning that it was host-only. A project brought up by
  an earlier version with `default: {internal: true}` needs a `down` first.

## [0.37.0] - 2026-09-26

### Added

- `down` no longer leaves a project running because its compose file cannot be
  read, when you name the project (`-p`, or COMPOSE_PROJECT_NAME in the shell or
  the `.env`). If the file is refused (by this version, for something an earlier
  one accepted), gone or named wrongly, or has a syntax error, `down` takes the
  project down by its label, as docker compose does for `down -p <name>`: every
  container the runtime holds for it (another project's are spared) and its
  default network, saying so on stderr. Volumes, images and networks the file
  declares are left, and `--volumes` and `--rmi` say they were not done. A name
  the runtime holds nothing for says so. A project worked out from the folder is
  not taken down that way — the folder's name is a guess when the file is what
  could not be read — and the refusal names the command to type,
  `opossum -p <name> down`.
- `build.dockerfile_inline` is now built. It was read and listed among the ignored
  fields, and `up` failed looking for a Dockerfile in the context. The text is
  given to `container build -f -` on its standard input (container 1.4.1), in
  place of any Dockerfile in the context, as docker compose builds it; `config`
  shows it, and `dockerfile` written beside it is refused, as docker compose
  refuses the pair. An image an earlier `up` built from the context's Dockerfile
  while this key was being ignored is not rebuilt by a plain `up`, which builds
  only when there is no image: run `up --build` once.

### Fixed

- A Ctrl-C while `up` or `run` is filling a new volume from the image stops and
  removes the throwaway container and deletes the half-filled volume, and now
  asks the runtime whether each is gone. When one is still there, or the runtime
  could not be asked, the message names it and the command that removes it
  (`container delete --force <name>`, `container volume delete <volume>`),
  where `run` used to say the fill "was taken back" whatever became of them.
- A reference in a mapping key is no longer expanded, as in docker compose. Under
  `environment:` or `labels:`, `E${SFX}: "1"` names a variable or label literally
  called `E${SFX}` (it used to be `E1`), and `A$$B` as a key stays `A$$B` (it used
  to be read as `A$B`); the same file gave the container different names here and
  under docker compose, with the same exit code. Values are expanded as before.
  `config` prints such a key the way docker compose does (`E$${SFX}`, `A$$$$B`).
  A service or secret key that holds a reference (`web${SFX}`) is no longer read as
  the expanded name; docker compose refuses such a file, and this now reads it with
  the name as written.
  A reference in a key that itself fails (`${NAME:?msg}` with `NAME` unset, an
  unterminated `${`) still fails the load, where docker compose reads the key as
  written.
- When `up` cannot start a run-to-completion dependency (a
  `service_completed_successfully` target) because the registry refused the read
  of its image, it now says so — `check the image name … and that it's
  reachable` — instead of `exited non-zero — check its output above`, and the
  rollback line no longer names it as `stopped and removed`: it never ran. One
  whose previous container this `up` removed to replace it is still named.
- When `up` fails because the registry would not hand over the image of a
  service it starts, the rollback line no longer says `Rolled back web — stopped
  and removed` about a service whose container was never made. Services that
  were made and then removed are still named, and so is one whose previous
  container this `up` removed to replace it. When nothing was made, the line now
  reads ``Nothing this `up` started is left running``.
- `build.dockerfile` is now read as a path from the build context, an absolute
  one as it is, the way docker compose reads it. It used to be read from the
  directory opossum was run in, so `{context: ./sub, dockerfile:
  Dockerfile.alt}` failed here with `dockerfile does not exist` and builds under
  docker compose. A file written for the old reading, `context: ./sub` with
  `dockerfile: sub/Dockerfile.alt`, now fails as it does under docker compose:
  write the path from the context (`Dockerfile.alt`).
- On container 1.4.1, a build the runtime refuses because of the image name
  (`image: Abc/Def:V1`, `image: org/App:v1` — a repository path with upper case is
  refused, where a tag or a registry host may have it) now says the name is what is
  refused, not the Dockerfile, and where the name comes from (`image:`, or the
  project's and the service's names when it is not written); where the repository
  has upper case it says to write it in lower case. It used to end with the usage
  text and the advice to build the image with Docker and import it, which no
  Dockerfile changes the name for.
- `opossum down` now stops the restart supervisor of a project whose compose file
  is outside the working directory (`-f sub/x.yaml`, or `COMPOSE_FILE`) after that
  file has been moved or removed. It looked for the supervisor under the working
  directory's name, which is not the project's name when the file is elsewhere, so
  the supervisor was left running and only `-p <name> down` stopped it. The name
  is now read as the load reads it: from the file's directory (and its `.env`),
  and for `COMPOSE_FILE` from the working directory's `.env` first. A project
  named by `name:` inside the file still needs the file.
- When no compose file is found and the working directory's `.env` (or the
  `--env-file` given) cannot be read, `opossum` now reports the env file instead
  of `no compose file found`, as docker compose does: it reads the env file
  first. This is the case where `COMPOSE_FILE` in the shell names a file in
  another directory, and the `.env` failing to read had been why that value was
  dropped and the search for a file came up empty.
- A variable name in an env file (the project's `.env`, an `--env-file`, or an
  `env_file` read in the default format) that holds ASCII punctuation docker
  compose refuses (`/`, `$`, `!`, `@` and the rest outside `_ . - [ ]`), or a
  control character other than a tab, a vertical tab, a form feed or a carriage
  return, is now refused naming the file, the line
  and the character (`unexpected character "/" in variable name`). It used to be
  accepted and set a variable with that name. The value is not quoted back. Letters
  past ASCII are still taken; docker compose also refuses symbols and some spaces
  there, which this does not.
- A variable name with a space inside it (`A B=1`) in an env file — the project's
  `.env`, an `--env-file`, or an `env_file` read in the default format — is now
  refused, naming the file and line, as docker compose refuses it (`key cannot
  contain a space`). It used to be accepted and set a variable named `A B`. Only
  the space itself: a tab or a no-break space in a name, and a space before the
  `=`, are read as they were.
- An `env_file` entry written as a mapping with no `path` key is now refused for
  the missing path whatever its other keys hold (`required: []`, `format: {}`).
  It used to be refused with a YAML type message and a hint about an unset
  `${...}` variable, which pointed away from the mistake; docker compose asks
  for the path first.
- A cycle among `volumes_from` entries behind a profile that is not turned on no
  longer stops every command. It was refused as the file was read, on every
  service, so `ps` and `down` failed over a project whose gated corner held one,
  where docker compose does not read that corner until something enables it. The
  cycle is now read the way one among `depends_on` is: by the commands that put
  the services in order, over the services they read, and named in the same
  words (`dependency cycle detected: [a b] -> a`). One among services that are
  read still refuses `up`, `ps` and `down`. The commands that do not order the
  services do not read it, as for a `depends_on` cycle: `config` prints the file
  (`config --services` refuses), and so do `exec`, `cp`, `port` and, when given
  service names, `stop`, `kill`, `restart`, `logs`, `pull`, `build` and `stats`,
  where docker compose refuses them.
- A `depends_on` entry that writes a key twice (`required: false` and `required:
  true`) is now refused for the repeated key, as docker compose refuses it, when
  its `condition` is also of the wrong kind (a list or a mapping). It used to be
  refused for the `condition`, which docker compose never gets to: it fails at
  the parse, before it checks what the values are.
- A label whose value is a number, a boolean or a date, written as a mapping
  (`labels: {a: 0x10}`), now carries the value docker compose gives it when the
  file is read on its own: `16` for `0x10`, `1.5` for `1.50`, `true` for `True`.
  It used to carry the text as written (`0x10`), while the same file read with a
  second one merged over it carried `16`, so which label a container got depended
  on how many files were given. A quoted value and the list form (`- a=0x10`) are
  text and stay as written.
  A service that wrote such a label on its own is recreated by the next `up`,
  because the value it carries changed.
- A service that lists several `group_add` groups beside a `user:` is now told
  both problems at once. It used to be told to keep the one group the process
  needs, and after doing that was refused again because `--gid` does nothing next
  to `--user`. The message names the groups, the `user:` and the way out: drop
  `group_add`, or drop `user:` and keep one group.
- A key written twice in the same mapping inside an `x-` extension (`x-foo: {a: 1,
  a: 2}`, in the file, in a service or in a declaration, at any depth) is now
  refused when the compose file is loaded, naming both lines, as docker compose
  refuses it (`mapping key "a" already defined`). A single compose file was the
  one path that accepted it: an overlay file, an included file and an `extends`
  target were already refused.
- The advice at the end of the `OPSM-206` and `OPSM-212` notices ("to pin one,
  write it in the compose file as ...") now keeps the entry's protocol and
  address. It used to give `"<host>:3000"` whatever the entry was, so copying it
  into the file for a `3000/udp` entry published a tcp port instead, and for a
  `127.0.0.1::3000` entry published on every interface instead of loopback only,
  with `up` succeeding either way. It now reads `"<host>:3000/udp"` and
  `"127.0.0.1:<host>:3000"` for those entries.
- `opossum up` no longer says "host port already in use" (`OPSM-201`) about a
  port that nothing is listening on. The one real case is a `ports` entry that
  names an address and a port below 1024 (`127.0.0.1:80:80`): a user who is not
  root is refused that bind, Apple `container` 1.4.1 refuses the entry too, and
  the message used to send you to look for a listener that was not there. It is
  now refused up front as `OPSM-215`, with the reason the bind gave and, for an
  IPv4 address, the way out: publish a port from 1024 up, or drop the address,
  which publishes on every address and is a wider door than the file asks for.
  Any other refused bind that is not a port in use is refused as `OPSM-215` with
  the bind's own reason. For an entry that leaves the host port to opossum
  (`127.0.0.1::80`), the port is still moved to a free one on that address, and
  `OPSM-206` now says the bind was refused instead of calling the port in use.
- When a `ports` entry names a host address and leaves the host port to opossum
  (`127.0.0.1::80`, `[::1]::80`) and the port it mirrors is taken, the host port
  opossum picks instead is now looked for on that address rather than on
  `127.0.0.1`. A port held on one address can be free on another, so the old
  search could pass over a port that is free on the entry's address, or hand back
  one that is in use there. Entries that name no address, or a wildcard (`80`,
  `0.0.0.0::80`, `[::]::80`), are still looked for on IPv4. An entry whose address
  this machine will not bind — which gets this far only when the service's
  container is already running — is no longer reported with `OPSM-206` as moved
  to a second port of that address; the start fails as before, with the
  runtime's own error.
- A short-form mount whose target is empty (`volumes: ["./src:"]`) is now refused
  when the file is loaded, as docker compose refuses it (`empty section between
  colons`). It used to be accepted and `up` succeeded, but the runtime took the
  empty target as an anonymous volume and mounted an empty one at the source's
  own path, so the host directory never appeared in the container. The message
  says the target is missing and shows the spelling to write (`./src:/app`). A
  named volume (`data:`) and an absolute path (`/abs:`) are refused the same way.
- A service, secret or config name outside letters, digits, `.`, `_` and `-` (a
  space, a `/`, a `$`, an empty or non-ASCII name) is now refused, as docker
  compose refuses it (`services additional properties 'web x' not allowed`), and as
  a volume name already was here. A service's name is refused when the file is
  read, by every command (`up` already refused it before creating anything). A
  secret's or a config's name had been carried on into the container's mount
  path as written; it is now refused by every command except `down`, `destroy`,
  `stop` and `kill`, which name it on stderr and go on so that a project started
  under such a name can still be taken down (and `doctor`, which reports). A network key has no such rule in
  either tool and is still accepted.
- `up` says `Creating network <name>` only for a network it created. It used to
  say it before asking the runtime, so an `up` after the first, with the project's
  network still there, said it was creating the network before it said the service
  was up to date. A network that cannot be created, or one whose declared
  subnet no longer matches, is no longer announced as being created before the
  refusal. A dry run cannot tell a network that is there from one that is not, and
  says `Creating network` for every network of the project.
- `up` now refuses a service that takes another service's volumes (`volumes_from`)
  when that service is not started with it and has no container, as docker compose
  does (`cannot share volume with service b: container missing`). This happens for
  a holder behind a profile that is off that the borrower depends on with
  `required: false`, which leaves the holder out. `up` used to start the borrower
  with the holder's volumes mounted from a service that was never there. A holder
  that has a container from an earlier `up` (running or stopped) is shared with, so
  an `up` without the profile a first one had on, and the rebuild `watch` does of
  the borrower alone, go through. Turn the holder's profile on, or name it.
- A long-form mount whose `target` is a number, a boolean or a date
  (`target: 5`, `target: true`, `target: 2026-09-20`), including one brought in
  by a `<<` merge or an alias, is now refused when the file is read, as docker
  compose refuses it (`is missing a mount target`). It used to be read as the text
  path `5` and passed to the runtime as a mount at that relative path. A target
  written as text (`"5"`) is the path it says. A project already running on such a
  mount can be taken down by naming it (`opossum -p <name> down`); `stop`, `kill`,
  `destroy` and `ps` refuse the file until the target is fixed.
- A project started on a `.env` whose variable name has a space or punctuation
  (`A/B=1`) can be taken down again. Since the check for those names, `down`,
  `destroy`, `stop`, `kill` and `ps` all refused such a project, though an earlier
  version had read the `.env` without complaint, so a running project could be
  left with nothing to stop it but the runtime's own commands. `down`, `destroy`,
  `stop` and `kill` now say so on stderr and go on, as they do for a secret or
  config name outside the rule; the commands that start or print something still
  refuse it, and so does `ps`. The same holds for an included project's `.env` and
  for an `--env-file`.

## [0.36.0] - 2026-09-25

### Added

- A compose file naming a host address this machine will not bind is now refused before anything starts, with `[OPSM-214]` naming the service, the entry, the address, and the reason the bind gave. What happened before depended on the shape of the entry: one naming a single host port came back as `[OPSM-201] host port already in use`, of a port nothing was listening on, with advice that could not help — freeing a port nobody held, or moving the entry to another port on the same absent address; one naming a range carried no code at all and reached the runtime's own failure, which names neither the address nor the port — and the advice printed beside it was opossum's own, about verifying the image, the command and the mounts. A mirrored entry was moved to a second port on that same address first, and told so. All of those now arrive as one message about the address, asked with port 0 so that a port somebody holds and an address this host does not have are no longer the same answer. Entries that name no address, or spell out a wildcard, are not asked about; neither is a service this run does not start, nor one whose container is already running. That last one is wider than "nothing has changed": it holds even when the entry is edited or the container is about to be recreated, so a running service whose address is edited to one this host will not bind is still let through: `[OPSM-214]` is not printed, its container is replaced, and the replacement fails with the runtime's own error — which leaves the service down where it had been up. Where the edited entry names no host port AND names a different container port than the running container publishes, the old reading survives with it: the port it mirrors to is reported as in use and the entry is moved to another port on the same address it cannot bind, with `[OPSM-206]` saying so, before the start fails. Edited to the same container port, or to an entry that names a host port, nothing is said at all before the start fails. The reason for it is a machine that moves — an address that has gone away since the container started would otherwise turn every `up` into a refusal, including the ones that would have printed "up to date" and touched nothing.

### Fixed

- A line that fixes a host port on one address no longer speaks for that number on another one. When `up` picks a host port for a `ports` entry that names only a container port, it steps over the ports the compose file has spoken for — and that set was keyed by the number and the protocol alone, so `z: ["127.0.0.1:8080:80"]` made 8080 unavailable to every other service, on every address. The runtime starts `0.0.0.0:8080` beside `127.0.0.1:8080` when the two belong to separate containers, so the entry that was moved lost its port, and the config hash with it, for nothing. A host port this same `up` has already given to another service is read the same way: two services that mirror one container port now keep it where they write different addresses, where the second of them used to be moved. Two entries of one service still count as one claim whatever addresses they name, since a container whose published ports overlap is refused whatever it writes; the rule the pre-flight already used to refuse a file that publishes one host port twice is now the same rule the picking reads. An entry that names a host port itself is unaffected, and `8080/tcp` beside `8080/udp` is two claims as before.
- A running container of a service this command starts is now taken, when `up` works out where to put host ports, to be holding every port of a range it published rather than only the first. The runtime answers a published range as one entry — the first host port and a count of the span — and opossum read the host port alone, so every port of the range except the first read as a port none of this project's containers held. A `ports` entry that names only a container port keeps the host port its own container is on where a range is what the container published it as, as long as no line of the compose file asks for that host port on the same protocol — the range itself included, if the file still publishes it. Where a line does ask for it, the port is spoken for and the entry is moved instead. A host port held by such a container is known to be held wherever it sits in that container's range, so a line of the compose file naming one host port is judged the same way the range's first port always was — passed over for the service whose own container holds it, allowed where that container is recreated first and gives it up, and refused where it is not — and a refusal names the service holding it. A line that names a range of host ports is still left to the runtime to accept or refuse, as it was. Ports held by a container of a service this command leaves out are ports this run cannot account for, whether a range or a single entry published them. A container port a published entry names outright keeps that entry's host port, even where a range of the same container covers that container port as well. `opossum ps` still lists a published range by its first host port alone, and `opossum port` answers only for that port.
- A published port opossum chose is no longer moved because another line of the same service names the same container port. When a running container publishes one container port twice — once on a host port the compose file writes down, once on a port opossum picked for a bare entry — the next `up` keeps the bare entry on the port it is actually published on, whichever order the `ports` list writes the two lines in. Before, the order decided: with the fixed line written after the bare one, opossum read that line's host port as the bare entry's own, moved the entry off it because the file asks for that port, and reported that the service "was published on" a host port it had never been published on. The same holds for two published ranges that overlap on a container port, where the file has kept only one of them: the entry stays on the port the range the file no longer writes has it on.

## [0.35.0] - 2026-09-25

### Added

- The documentation site now publishes `/llms.txt`, the conventional entry point for AI agents arriving at a site ([llmstxt.org](https://llmstxt.org)). It carries the project's name and one-line summary, then a link to `AGENTS.md` — the reference written for agents rather than people — followed by the README and every page the site publishes. Each link points at the Markdown itself rather than at the page GitHub wraps around it, so an agent following one reads the document instead of an interface with the document inside it. The list is built from the same set of pages the site publishes, so a page added to the site appears there without anyone remembering to add it.

### Fixed

- A key under a mount's `bind:` that docker compose does not take is now refused here too, naming the first such key as the rest of the loader does (docker compose names every one of them). `volumes: [{type: bind, source: ., target: /app, bind: {subpath: sub}}]` loaded and started, and `subpath` did nothing — it is a `volume:` option, not a `bind:` one, and docker compose refuses the file outright. The whole block was listed among the ignored fields and its contents were never read, so a misplaced or misspelled option went through in silence. The four options docker compose takes there — `propagation`, `create_host_path`, `selinux`, `recursive` — are accepted as before, and the block is still named among the ignored fields, because opossum acts on none of them.
- A `ports` entry that names only a container port is now published on a host port no other entry the file fixed has asked for, wherever in the file that entry sits. The port it mirrors was measured against the entries opossum had already walked past, which made the same file start or fail to start depending on how the entries were arranged: `ports: ["8080", "8080:80"]` published 8080 twice and the runtime refused the pair (`host ports for different publish port specs may not overlap`), while the same two written the other way round started. Across services it was worse, because the order is the order services start — their names in lexicographic order, unless `depends_on` says otherwise — so a project that started yesterday stopped starting when a service was renamed, with nothing in `ports` touched. A published range is now read as the ports it holds, so a bare entry no longer lands inside one — including a range a bare entry asked for, which opossum cannot move and so leaves where the file put it. (One case is left: when a bounded number of tries find no host port outside the file's own that can be bound, the entry stays on the port it mirrors and `up` says so, `OPSM-212`.) A host port already held by a running container of this project is still kept as it is, ahead of this, for as long as no line of the file asks for that port. What a bare entry mirrors when the port is free, and the host ports the file itself names, are unchanged.
- A `ports` entry that names only a container port is now measured against every host port the compose file fixes, not only against the ones belonging to the services this command happens to start. `opossum up a` gave `a`'s bare entry a host port that service `z` names in the same file, so the plain `up` that followed could not start: `a` was already holding the port `z` was written for, and the refusal named a host port the file gives to `z` while `a`'s line says only a container port. A service a profile keeps out of this run was left out in the same way, so enabling that profile later broke a project that had been starting. The cost is that a bare entry can be moved off its mirrored port because of a service this command does not start — a host port nobody asked for either way, where the other reading is a project that stops starting once a profile is enabled. The notice about the move now says which of six reasons made the port unavailable, since only one of them is a question about the machine: another service's line asking for a port is not a port anything is listening on, and a line that names a range of container ports does not name a host port at all. docker compose has neither failure, because it never lets an entry like this pick a host port at all.
- A host port a running container of the project already publishes is now given up when a line of the compose file starts asking for it. Such a port is kept across re-ups, which is what makes the host port — and so the config hash — stable for a `ports` entry that names only a container port. But the file is the project: once a line fixes that port, it belongs to the service that wrote it down. Until now the running container kept it and the file's own line got the same number, and one host port cannot be published twice — so a project that had been starting stopped starting as soon as a host port was written down for a service that had been mirroring it. The container's entry moves instead. When the entry then lands on the very number it mirrors — the case that otherwise reads as no change at all — `up` names the port given up, the line that took it, and where the entry went; when that number is taken too, the notice is about the port it moved to and does not name the old one. Whether the pair starts after that depends on where the container holding the port sits in the startup order: `up` recreates in order, so a container recreated before the line that took its port has let go by then, and one recreated after it has not — that is refused before anything starts, naming the service holding the port. A port nothing in the file asks for is still kept as it was, so the stability this exists for is unchanged. A bare entry of another service does not count as asking for it, because it is a number opossum chose and may itself move; a bare range does, because opossum cannot move a range and the port would then be published twice. A host port and a protocol together make the claim, so a line asking for the same number on udp leaves a tcp port alone.
- A compose file that publishes one host port twice on the same address is now refused before anything starts, naming both entries. Two `ports` entries asking for the same host port and address can never both be published, but the pre-flight only probed each host address once — a saving, not a judgement — so the pair was walked past in silence. What happened next depended on where the two entries sat: two services failed when the second container could not bind, and one service publishing the same port twice was refused by the runtime with a message about the publish specs opossum had assembled. Neither named a line of the compose file. The refusal now says which two services and which two entries, since which one to change is yours to decide. Two entries of one service collide whatever addresses they name, because the runtime refuses overlapping publish specs within a container; two services collide only when they name the same address, and `127.0.0.1` beside the machine's LAN address, beside the IPv4 wildcard, or the IPv4 wildcard beside the IPv6 one, still starts both as it did before (docker compose refuses a pair with a wildcard in it — that difference is unchanged here). `8080/tcp` and `8080/udp` are still published side by side, a host port written `08080` is the same port as `8080`, an entry belonging to a service this command does not start is not counted, and neither is one belonging to a service that runs to completion before its dependent begins. A published range is still left to the runtime.
- The free host port opossum picks for a `ports` entry that names only a container port is now checked against the ports the compose file itself publishes. opossum asks the operating system for a free port, and the system answers about listeners — a line that fixes a port has nothing listening on it until that service starts — so the answer could be a port the file had already taken, including one inside the very range that caused the move. The runtime then refused the pair and the project did not start, after opossum had said it moved the port to avoid exactly that. opossum now steps past the ports the file has spoken for and asks the system only what it alone knows, whether the number it picked can be bound; the ports it steps over include the ones this run has just given to other entries and the ones a running container of the project holds, not only the ones the file writes down. A range of any width costs one extra question. When the walk runs off the last host port it asks the system again, since the answers are ephemeral ports and one near the end of that range has almost nothing above it. If a bounded number of tries all come to nothing, the entry is left where the file mirrored it and `up` says how many were tried, naming the service whose line took the port when the file names one: the run usually fails after that, whatever holds the port holds it still, but it fails saying which line to change rather than leaving the runtime to report a clash between specs opossum assembled.
- `up` no longer refuses to start because a host port is held by a container it is about to replace. The check that runs before anything starts asks whether each host port the file names can be bound, and a port a running container of the project holds cannot — so it skipped the ports of any service whose own container was running, which was enough while a host port stayed with the service that had it. Once a port moves from one service to another, the two questions that skip was answering come apart: an entry of the service whose container holds the port is looking at itself, while an entry of another service is looking at a port that may or may not be let go in time. The check now asks both separately. An entry is passed over a port its own container holds, since `up` recreates that container. A port held by another of this project's containers is taken as free only when that container is recreated earlier in the startup order — it has let go by then — and only when the service's own entries no longer publish it, since a container that keeps publishing a port takes it straight back, as does one `up` leaves alone because nothing about it changed. The other holders are refused: a container recreated after the entry that wants its port, a service this command was not asked to start or that a profile leaves out, a stopped container of this project, one holding that number on the other protocol, another project's container. The first of those is refused whatever the asking service is doing, and that is a refusal where there was none — an entry of a service whose own container was running used to be passed over whatever held the port, and a port held by another of this project's containers is refused here now rather than failing on the bind with a message about publish specs. The rest are ports this run cannot say who is holding, and there one case is unchanged and deliberately so: an entry of a service whose own container is running is still passed over. The probe cannot tell an occupant from an address this machine will not assign, and that is a question about the probe rather than about whose port it is. What is new in the refusals is a name: where the holder is a service this run starts and its container is running, the message says which one, since "free the port" cannot be done without stopping what is being started. A holder whose own entries no longer publish that number is holding it only until this run recreates it, so the message says to take the project down and bring it up again; a holder whose entries publish it takes it straight back however often the project is restarted, and there the message says to change one of the two lines — wherever in the startup order that holder sits.

## [0.34.1] - 2026-09-23

### Fixed

- A refusal for a `group_add` group past the largest gid now says what the group reads as whenever that differs from the entry the refusal names, including where the file quoted it. `--gid` reads what it is handed in decimal, so `group_add: ["+2147483648"]` and `["02147483648"]` are the group 2147483648 to the runtime — but the refusal named only the characters the file wrote, and nothing in it showed that opossum had read them as a number. Only entries the loader writes back in their decimal — a number written without quotes, in a file read on its own — used to be named both ways. An entry whose spelling is already the number it reads as is still named once, and so is one a second file settles into its decimal before this is read.
- A `group_add` list naming one group in two spellings (`[0x10, "16"]`) now reaches the same refusal whether or not another compose file is read beside it, and the project can be brought down either way. The same entry twice was looked for in the merged document as well as in each file, and merging writes the tree back out — so `0x10` arrived there as `16` and read as a repeat of the `"16"` the same file already wrote. That refusal is raised as the file is read, which stops every command, `down` included: a service started before the second file was added, or one left running while the files were edited, had no way to come down from them. The file is now asked about the entries it wrote, where the two spellings are two, and the shape is refused before the service starts instead — the refusal that asks for the group to be listed once, or, where a file beside it adds another group, the one that says which entries are the same group. What the refusal quotes is the entry as the merge settled it, as it was before. A file that names the same entry twice in the one spelling (`[16, 16]`) is still refused as it is read, naming that file, that service and those two entries; docker compose refuses it too.
- Two `group_add` entries that `--gid` reads as one group are now counted as one whatever the number's size. The reading they were folded by was an integer's, which put a boundary in the middle of it: `[9223372036854775807, "+9223372036854775807"]` was one group and the same pair one larger was two, so a file that had written one group once was told it named two and to keep one — and keeping one left it refused all the same, the group being past the largest gid the docker engine takes. Such a pair is now refused for that group, as a smaller pair is. docker compose has nothing to compare here: it refuses a file holding a number that large as the YAML is read (`expected type 'string', got unsigned integer`). An entry `--gid` cannot read as a number stands for itself, as before.
- `ports` entries that name the same host port and container port are now one published port when one of them writes `/tcp` and the other writes no protocol at all. A spec with no protocol is tcp, as docker compose reads it, but the two were compared as written — so `ports: [8080/tcp, 8080]` published the port twice, the second on a host port nobody asked for, and `[8080:8080/tcp, 8080:8080]` was refused by the runtime outright (`host ports for different publish port specs may not overlap`) although docker compose starts it. What is published is still the entry as the file wrote it, `/tcp` and all or neither, and a different protocol is still a different port: `8080/udp` beside `8080/tcp` stays two.
- A `ports` protocol written in upper case is now read as the protocol it names. `8080/TCP` and `8080/tcp` are one published port, as they are on docker compose, where they were two: the entry was published twice, and where both named the host port the runtime refused the pair (`host ports for different publish port specs may not overlap`) although docker compose starts it. Any spelling folds — `Tcp`, `tCp`, `UDP` — and the port is published with the protocol in lower case, the spelling docker compose settles on. A long-form entry's `protocol:` keeps the case it was written in, as it does there, so a file writing `{protocol: TCP}` beside a short-form `8080:8080/tcp` still has two entries in both. One shape that used to start now does not: a long-form `{protocol: TCP}` beside a short-form `"8080:8080/TCP"` was one published port while both kept their case, and is two now that the short form settles as `/tcp` — the runtime then refuses the pair. docker compose does the same with that file (measured on v5.5.1: `config` returns two entries and `up` fails to bind the second), and matching it is the choice made here. A short-form entry that names no host port is still published with the host port opossum supplied and nothing else added, as before: what each part of a spec is written back as follows docker compose, part by part, rather than one rule for the whole spec. A service already running with such a port is recreated on the first `up` after this, the spelling being part of what opossum compares to decide whether a container still matches its file.
- A host port another `ports` entry has taken is now read as taken for that protocol, and for that protocol only. The port a bare entry mirrors was checked against the host addresses the project had spoken for, which was wrong in both directions at once. An entry publishing `8080/udp` made the next bare `8080` entry land on some other port, although udp and tcp do not overlap and the runtime publishes both. And an entry publishing `127.0.0.1:8080` did not stop the wildcard `8080` being handed out, although the two do overlap — the runtime then refused the pair opossum had assembled (`host ports for different publish port specs may not overlap`) and the project did not start at all. This is read across the project rather than within the one service: an entry in a service that starts earlier takes the port for the services after it. A host port the file itself names is still never moved, and a bare port on a number nothing has taken still mirrors it.

## [0.34.0] - 2026-09-23

### Added

- `up` now says, for the services it starts, when a service on two or more networks cannot be reached by name from one of its peers — both of the two have to be among the services it starts, and a `run` says it for the dependencies it starts. Apple `container` 1.4.1 registers a container's first attachment in its DNS, so such a service answers with its address on the network it is attached to first; a peer that shares only a later network gets an address it cannot reach. The message names both services and a network they share that is not `internal: true`, and what to change — attach that network first (a list of networks attaches in the order written, a mapping and networks merged from several files in name order), or have the peer use the address. A peer whose own first network is internal is left out, since its resolver is that network's gateway and answers nothing at all; so is a pair all of whose shared networks are internal. Docker compose answers with an address the asking service can reach.
- A `run` now says when its one-off cannot be reached by name from a service it starts, or cannot reach one of them. Where a peer would look the one-off up, the message gives the name its container answers to (`<service>-run`) and says there that it is the container this run starts for that service, since no service of that name is in the file; where the one-off is the side that cannot reach, the name to use is the peer's, so the one-off is called the one-off this run starts for that service and not spelled out. The advice names the service the file spells, and where a peer would look the one-off up it says there that the one-off takes that service's networks in the same order. The one-off joins the service's own networks in the same order, so Apple `container` answers for it with the address on the network it is attached to first, as it does for the service itself; a peer that shares only a later network gets an address it cannot reach. The pairs among the dependencies are still said once, by the start that brings them up.

### Changed

- Where a refusal `up`, `run` or `run --audit` raises for `group_add` before it starts a service names the entry, it now quotes it in every one of them, and leaves a reading opossum made of that entry bare. What is quoted is the entry as the file spells it where one file is read with nothing to merge into it — no second `-f`, no `include:` of a file, no `extends:` on a service — and, where one of those merges the file first, the spelling as it settles there, which is what the rest of that refusal already named. Three of them used to leave the spelling bare, so a refusal about a group past the largest gid held the spelling and the reading in one sentence in the same shape (`the group 0x80000000 … reads as the group 2147483648`) and a reader had to know which was which.
- A refusal for two or more `group_add` entries now counts the groups the runtime would be given, not the entries the file writes. `--gid` reads what it is handed in decimal, so `0x10` and `"16"` — or `16` and `"016"`, or `16` and `"+16"` — are one group; a file that names one group more than once is now told to list the group once, where it used to be told to keep one of two — and, where the group those spellings fold into is one `--gid` does not take (negative, past the largest gid, or beside a `user:`), the refusal about that group comes instead. A file that names two groups over three entries now says which entries are the same group.

### Fixed

- A build whose Dockerfile takes from an additional build context (`build.additional_contexts`, as in `COPY --from=lib`) still fails where no image of that name exists — `container build` takes one context directory and has no way to add another — but the failure now says that the name the registry refused is one of the service's additional build contexts, and what to do instead, where it used to ask the reader to check that image's name and tag. A build that does not use them goes ahead as before.
- A named-volume mount of a part of the volume (`volume: {subpath: sub}`), in the service's own `volumes:` or borrowed with `volumes_from`, is now refused before anything is created, naming the service and the mount, instead of mounting the whole volume in its place — which gave the service other files than docker compose does. container 1.4.1 cannot mount a part of a volume. A `subpath` that names the whole volume (empty, `.`, `sub/..`), one at a target a later entry mounts whole, and a bind's or a tmpfs's `volume.subpath` go ahead as before, as docker compose reads them the same way.
- `group_add` now reads a number to its decimal — the group docker compose adds — whether the compose file is read on its own, with a second `-f` file, or through `extends`. Before this, a file read on its own kept what was written, so `group_add: [0x10]` was refused before the service started (`--gid` takes digits), while the same file read with an override that said nothing about `group_add` added group 16; and `group_add: [0755]` was refused outright, where docker compose adds group 493. A zero written with a minus keeps its sign, so it is still refused for being negative.
- The ignored fields `up` names before refusing a mount of a part of a volume (`volume: {subpath: sub}` on a named or an anonymous volume) no longer include that mount's `volume.subpath`, so the key is not first called ignored and then refused. `config` still lists it.
- A build that fails because the registry refused the name of an additional build context whose name holds a `/` and no tag — `org/lib`, or `example.com/org/lib` on a registry of its own — now says that the name is one of the service's additional build contexts, as it already did for a name without one, instead of asking the reader to check that image's name and tag. A name that is a Docker Hub reference spelled out (`library/lib`, `docker.io/org/lib`), which docker compose does not use a context for, keeps the ordinary hint.
- A `group_add` refusal now names the entry as the compose file spells it, where one file is read with nothing to merge into it — no second `-f`, no `include:` of a file, no `extends:` on a service. A number is read to its decimal — `0x10` is the group 16, as docker compose reads it — and the refusal used to name that reading, so a service with `group_add: [0x10, 0x20]` was refused for adding `"16", "32"`, naming neither line as the reader wrote it. Where one of those merges the file first, the spelling settles as the reading does and the refusal names that. A group past the largest gid is named both ways, the spelling being no help in seeing that the number is too large.
- A `run` of a service is now refused, before anything starts, when the project has a service named `<service>-run` — the name the one-off's own container carries. Starting it took that name: the container under it became the one-off's, whatever the service had there was gone, and a later `up` made the service another one; what `--rm` removed afterwards was the one-off, and without it the one-off was what stayed there, stopped. The refusal reads the compose file, so it comes whether that service is running, never started, or behind a profile that is not active, and it names the service to rename. docker compose does not meet this, its one-off being `<project>-<service>-run-<hash>`, so this is a difference from it.
- Two `group_add` entries written with the same characters are no longer refused as the same entry twice unless what opossum hands over for them is the same too. An integer `020` is YAML's octal — the group 16 — while the string `"020"` is the digits `--gid` reads as 20, and a file naming both was refused as it was read: every command, `down` among them, so a project already started from that file could not be brought down. The same held for a pair that is one group all the same, such as the integer `+16` beside the string `"+16"`, since the integer is handed over as `16`. Such a file is now read, and the check the services are started by is what refuses it — for naming two groups, or for naming one group twice.
- Two compose files writing the same `group_add` entry as a number are now one entry after the merge, as they are on docker compose. Only entries that arrived as strings were folded, so `group_add: [16]` in both `compose.yaml` and `compose.override.yaml` — no `-f` given — came through as two, and `group_add` then refused them as the same entry twice as the file was read: `up`, `ps`, `config` and `down` all stopped, and a project already started from those files could not be brought down. A number beside its string across files (`[16]` over `["16"]`) folds too, where docker compose keeps both when the number comes first and refuses the file when the string does; opossum folds either way, since one `--gid` is handed over in the end. A file naming a group twice on its own is still refused, wherever that file sits: docker compose refuses that too when the repeat is in the first file, and folds it away when a later file wrote it.

## [0.33.0] - 2026-09-20

### Added

- `volumes_from` is now read: a service mounts the named service's `volumes:` as its own (its own winning where both mount one path, and what the named service borrowed coming along), and starts after it, as on docker compose. opossum listed the key among the ignored fields and started the service with the paths empty. A `:ro` suffix does nothing, as it does nothing on docker compose; a service listed twice under one spelling is refused, as there. Known difference: the mounts are folded in as the file is read, so `run --no-deps` runs the service with them while the named service's container is not there, where docker compose refuses it. A named volume borrowed this way is one two services share, which the runtime attaches to one running container at a time, as the `[OPSM-102]` note says for any shared named volume. Refused, where docker compose reads them: a `container:<name>` entry, whose mounts live outside the compose file, and a named service with an anonymous volume at a path the borrowing service does not mount itself, which docker compose shares and opossum would make a second volume of — declare the volume and mount it by name in both. These refusals — a missing service, a `container:` entry, a cycle among `volumes_from` entries, an anonymous volume — reach a service whose profile is off, where docker compose reads such a corner only once something activates it (and then refuses the first two of these and reads the other two); and an anonymous volume at a path a later named service mounts is refused too, where docker compose leaves it out.
- `group_add` is now read. One numeric group on a service that writes no `user:` reaches `container run` as `--gid`, the one supplementary-group flag container 1.4.1 has, so a process can be given the group that owns a socket or a device. The shapes the runtime cannot take — two or more groups, a group by name, any `user:` beside it, or a group that is empty, negative or past 2147483647 — are refused before anything is created, naming the service and what to write, instead of being started without the group and failing later with permission denied. An unquoted number with a leading zero whose octal reading differs from its decimal one (`0755`, which is 493 to YAML and 755 to the runtime; `07` and `00` read the same both ways and pass) is refused as the file is read, naming both ways to write it. `config` prints `group_add` with each number quoted.
- `tty: true` is now read: `up` starts the service with `-t`, so an image whose command is a shell — `alpine`, `ubuntu` — keeps running the way it does on docker compose, instead of exiting at once and failing the `up`. `config` prints it, and changing it recreates the container. `run` keeps deciding `-t` by the terminal it was typed at, as docker compose does. `stdin_open` stays listed among the ignored fields: the runtime's `-i` does not keep a shell alive, and with `-t` it keeps the container from starting when stdin is not a terminal.

### Fixed

- A service with both `build:` and `image:` is now built under the name `image:` gives, as docker compose builds it, instead of always as `<project>-<service>:latest`. A second service that uses the first one's image by that name — a worker running the same image as the app that builds it, say — used to send `opossum up` to a registry for a name that only ever existed locally, and fail. `up`, `run`, `build`, `ps`, `images`, `down --rmi all` and `destroy` all use the one name, and so do `import` and `up --from-docker-compose`: an image brought over from Docker arrives under its `image:` name and is no longer given a second tag. Expect such a service to be built again and recreated on the next `up`, since nothing is under its new name yet. `down --rmi local` leaves an image built under an `image:` name — the name is one the compose file chose, and may hold something this project did not make — where docker compose's removes its own builds; `--rmi all` and `destroy` remove it. `--rmi local` does clear out the image such a service had under the old name. A service with `build:` alone is unchanged.
- A `depends_on` entry's `required: false` is now read: a dependency behind a profile that is not active is no longer a fault — `config` prints it as written, `up` and `run` go ahead without it — and one that is active is waited for as any other, its failure to become healthy or to complete noted and the dependent started anyway, as on docker compose (a failure to complete is passed over only when every dependent in the file that needs the completion wrote `required: false`; docker compose counts only the dependents it starts). opossum read `required` as nothing and refused the service for depending on one whose profile is off. `required` is written beside a `condition`, as docker compose requires, and read as the YAML 1.1 words are read there (`false`, `no`, `off`, `n` and their casings, quoted or not; any other word, `null`, a number, a list or a mapping is refused). A dependency the file does not define is refused whatever `required` says, as there. `config` now prints `required` on every dependency, `true` where the file left it out, as docker compose prints it.
- `COMPOSE_FILE` is now read, from the shell and from the `.env` (or the `--env-file` given), where docker compose reads it: after `-f`, and before a compose file is looked for. It used to be ignored, so a directory set up with it ran one file under docker compose and another — whichever `compose.yaml` was lying there — under opossum, without a word. Several files are separated by `:`, or by `COMPOSE_PATH_SEPARATOR`; files named this way are taken as chosen, like `-f`'s, so no override file is merged into them; one in another directory is read with the working directory's `.env` and then its own directory's, as docker compose reads it; and a file that is not there, a directory or anything else that is not a regular file, and an empty element are refused, as docker compose refuses them (a `COMPOSE_FILE` that is set and empty reads as not set, where docker compose refuses it, and `COMPOSE_FILE=-` is not read from standard input). The restart supervisor is started on the same files and the same `.env`, and what `opossum up --from-docker-compose` says to type keeps `COMPOSE_FILE` naming the files. If you set `COMPOSE_FILE`, what an earlier opossum started there came from the file it found instead. Where the named files are in the same directory, those services are now orphans of the same project: `opossum up` warns about them (`[OPSM-402]`) and `opossum down --remove-orphans` removes them. Where the named files are in another directory the project's name usually changes with them, and nothing points at the old one, which is under the name it had — the found file's `name:`, or the working directory's name: `opossum -p <that name> down --remove-orphans` stops and removes its containers and network (its volumes keep their data until `-v`).
- `COMPOSE_PROFILES` is now read from the `.env` (or the `--env-file` given) as well as from the shell, where docker compose reads it. It used to be read from the shell only, so a directory whose `.env` (or a run whose `--env-file`) enables a profile ran fewer services under opossum than under docker compose, without a word — so there the next `opossum up` starts the services that profile enables, which it did not start before. The order is docker compose's, and the first that says anything is the only one read: `--profile` (even one that names nothing, `--profile ''`), then the shell (a `COMPOSE_PROFILES` that is set and empty leaves nothing active), then the `.env`. That also changes a run given both `--profile` and a `COMPOSE_PROFILES` in the shell: the two used to add up, and now the flag alone counts, as with docker compose. A service started while they added up is still the project's — `opossum ps` lists it and `opossum down` removes it — but an `opossum up` given `--profile` flags no longer starts it unless one of them names its profile.
- `COMPOSE_PROJECT_NAME` is now read, from the shell and from the `.env` (or the `--env-file` given), where docker compose reads it: after `-p`, and ahead of the compose file's `name:` and the directory. It used to be ignored, so a directory set up with it was one project to docker compose and another to opossum, without a word — and `opossum down` there took down whatever had the directory's name. If you set it, this changes which project opossum acts on in that directory: what an earlier opossum started there is still under the old name (the file's `name:`, or the directory), where `ps` no longer lists it, `down` no longer stops it, and `up` starts the newly named project beside it with empty volumes. `up`, `down`, `ps` and `destroy` now say so on stderr when that project still has containers or volumes, and name the `-p <old name>` that reaches it — `opossum -p <old name> down` stops it, and its volumes keep their data until `-v` (add `--rmi local` for the images it built).
- An `env_file` entry's `format: raw` is now read: each value in such a file reaches the container as written — quotes kept, `$HOME` and `${VAR}` left as those characters, a ` # note` and trailing blanks part of the value — the way docker compose reads it. opossum listed `format` among the ignored keys and read the file as dotenv, so `Q="kept"` lost its quotes and `D=$HOME` reached the container as the host's home directory. Under `raw` a line with no `=` is skipped (a `KEY: VALUE` line among them), a name with a space or a tab in it — `export A=1`, `A B=1`, `A =1` — or no name at all is refused, as docker compose refuses it. An unknown `format` is refused when the file is read (`unsupported env_file format "foo"`), `format: ""` is the default reading, and a `format:` that is not a string is refused. Files of either format are read in order, and a `${VAR}` in a later file of the default format sees an earlier raw file's values.
- `opossum up --from-docker-compose` over a compose file named with `-f` prints the overlay it would have written and the command that uses it. For a file in another directory (`-f sub/x.yaml`) it said to write the overlay next to that file, and then printed a command that looked for it in the working directory, so following it failed. The command now names the overlay where the advice says to write it (`-f sub/x.yaml -f sub/compose.opossum.yaml`), and quotes what a shell would take apart, such as a path with a space in it.
- The commands opossum spells out for you to type now carry the root flags of the run that printed them — `-f`, `--env-file`, `-p`, `--profile` and `--dns-domain` — and quote what a shell would take apart, so they can be typed as printed: the migration's `rm compose.opossum.yaml && opossum up --from-docker-compose …` and the `-f`/`COMPOSE_FILE` form of that advice, the note's `opossum -p <former name> … down` about what is left under a project's former name, and `watch`'s `opossum up --build …` next steps. Typed without those flags in a project chosen with `-f`, they read the working directory's compose file instead, and reach other names (a `--dns-domain` is in every container's name). The refusal for a service that depends on one behind a profile that is not active now says how to enable it in the run that got it: beside the profiles the run already has active, and in the place the run reads them from — none of the places add up, so a run given `--profile` is not helped by `COMPOSE_PROFILES`, and a `COMPOSE_PROFILES` set in the shell replaces one in the `.env` rather than adding to it (following the old wording that way silently dropped the services the `.env` had enabled). Naming the service is a way out for `up` alone, and the refusal says so only there.
- A compose file that `COMPOSE_FILE` names in another directory is now read with both `.env` files docker compose reads it with: the working directory's first, then the file's own directory's for what the first does not set. opossum read the working directory's alone, so a `${VAR}` — or a `COMPOSE_PROFILES` — that only the file's directory's `.env` sets came out unset, without a word; and a `COMPOSE_PROJECT_NAME` there whose value refers to a variable (`COMPOSE_PROJECT_NAME=${X}`) was resolved without the working directory's `.env`, which could name the project differently than docker compose does. A variable the working directory's `.env` sets to nothing stays set, the shell is over both files, and with `-f` or an `--env-file` the one file is read as before. The restart supervisor reads the same two files. Since that second `.env` is now read, one that cannot be opened, or that has a line that cannot be read, is refused by every command, as docker compose refuses it — it used to be passed over without a word. Separately, a `.env` that is a directory — the working directory's or the compose file's, whatever chose the file — is now passed over as if it were not there, as docker compose passes over it; it used to be refused.

## [0.32.0] - 2026-09-17

### Fixed

- `--profile` is taken by every command, as docker compose takes it on
  every subcommand: `opossum --profile debug down` (and `ps`, `logs`,
  `pull`, `stop`, and the rest) no longer fails with `unknown flag`, and
  every command that decides which services it reads out of the compose
  file honors `COMPOSE_PROFILES` too, where before only `up`, `run` and
  `config` did (`doctor`'s memory estimate is of the whole file either
  way). `pull` and `build` now leave out a service gated behind a
  profile that is not active, as docker compose leaves it out, and a
  mount conflict in a service a profile enables is refused by the
  commands that read the project, and named on stderr by the ones that
  take it down (`down`, `destroy`, `stop`, `kill`), which go on. `logs`,
  `stop`, `start`, `restart`, `kill` and `down` act on every container of
  the project whatever the profiles say — a known difference from docker
  compose, which narrows them.
- `run` (and `run --audit`) refuses a dependency behind a profile that is
  not active before the one-off's own name held by another project's
  container, and before the names and mounts it would otherwise refuse,
  as docker compose v5.5.1 refuses it first; before, the one-off's
  refusal was given and the dependency's profile named only once those
  were fixed.
- `logs --follow` no longer goes on forever when a followed service's
  container exits, is stopped or is removed while it is followed: once
  the container has stayed not running for three seconds (a restart is
  followed on) and what the runtime still hands over has been written,
  the stream ends with a `web-1 exited` line, four to six seconds after
  the container's end, or later while lines are still handed over, and
  the follow ends with exit 0 once every followed stream has, as docker
  compose v5.5.1 does (container 1.4.1's `container logs -f` goes on
  after all three). A container already stopped when the follow began
  is still followed on, as there. docker compose also says the exit
  code, which container 1.4.1 does not report; a service with a
  `restart:` policy is still followed on after `stop` or `down`, where
  docker compose ends it, and a container recreated by `up` is not
  followed to the new container, whose lines are not shown, where docker
  compose says the old container exited and follows the new one.
- `run` and `run --audit` now refuse what reading the compose file
  refuses before anything about the one-off itself: a service that
  depends on one behind a profile that is not active, then a dependency
  cycle, wherever among the services it reads they sit and with
  `--no-deps` too, as docker compose v5.5.1 refuses them. Naming a
  service carries what it depends on behind its own profiles, as there.
  What the one-off and the services it depends on need is then asked for
  in docker compose's order: an external network that is not there, an
  external volume that is not there, a volume name container 1.4.1 cannot
  create, and a `tmpfs:` option the docker engine 29.8.0 refuses — so
  an external volume that is missing is now named for being missing
  even when its name is also too long. `--no-deps` looks at a dependency's named
  volumes and the external network and volume it names, which are looked
  for either way, and not at the anonymous volumes of its container,
  which is never made. `up` reads the file the same way and asks in the
  same order, so it too refuses what reading the file refuses under a
  service it was not asked to start, as docker compose does (what the
  services it starts need is asked for those services, there as here)
  — and `opossum up web` on a `web` behind a profile now starts the `db`
  it depends on behind the same profile, where it used to refuse it.
- `logs --follow` of a container that has written nothing yet no longer
  ends at once with exit 0 and nothing shown: it is followed on, and lines
  the container writes later are shown, as docker compose v5.5.1 does,
  and a stop ends it with the `web-1 exited` line (docker compose also
  says the exit code). container 1.4.1's `container logs -f` ends at once
  on an empty log unless it is given `-n`, so opossum asks it again with
  `-n` and every line.
- A dependency cycle among services behind a profile that is not active is no longer read: it is looked for among the services a command reads, as docker compose v5.5.1 looks for it (measured). Before this, a cycle added behind a profile nobody had turned on refused the commands that read the whole project — including `ps`, `stop`, `down` and `destroy` with no service named, which left a running project with no way to take it down. A cycle among the services a command does read still refuses it, and every service keeps its place in the order, so a project still comes down in the order it went up, reversed.
- A cycle that runs through a service behind a profile that is not active — where the rest of it is active — is no longer read as a cycle either, so such a project can be listed, stopped and taken down. `up`, `run` and `config` still refuse it, for the dependency on a service whose profile is not active; docker compose refuses it the same way, and refuses the commands that list and take down as well unless it is given a project name with `-p` and left to find the compose file itself. That difference is written in `docs/compatibility.md`.
- `opossum watch` now says what is wrong with a compose file whose services it cannot put in order, instead of saying the file has no `develop.watch` rules in it. A file with a dependency cycle among the services being read made it report no rules at all, which sent a reader looking for rules they could see they had written.
- When `opossum up` cannot start a service because the registry refused to hand over its image — it answered the read of the image's manifest or of one of its layers with a refusal — it now points at the image name and whether it can be reached, the way `opossum pull` does over the same failure, instead of telling you to read the logs of a container that was never made. The runtime's own answer is still shown above it: a registry that needs a login, an image that is not there and a tag that is not there each say something different, and some of them answer 401. A service the compose file builds is unchanged (its image is made locally, so `opossum build` is where its answer is), and so is a service another one depends on completing.
- A service nobody has started — one behind a profile that was never turned on, or one an `opossum up <service>` did not reach — is now passed by, instead of failing the command or being reported as work done. `opossum logs` with no service named printed nothing at all and exited 1 over such a project, so a compose file with a `profiles:` service made `logs` useless until that service had been started once; `start` and `restart` failed the same way, and `stop`, `kill` and `down` said they were stopping a container that was not there. Asking for such a service by name still answers for it, so a name typed wrong is not passed over in silence.
- A service that fails to start no longer points at `opossum logs <service>` when there is no container left to read. The rollback that follows a failed `up` removes what it started, so the container the message named was usually gone by the time anyone could look — `opossum logs` answered `container … not found`. The way out now follows what the teardown actually found: where the container is still there, or the runtime could not be asked about it, the logs are still where the answer is.
- A build that fails because a registry refused an image the Dockerfile names — a `FROM` or `COPY --from` with a tag that is not there, or a repository that is not there or cannot be seen — now says so, and points at where the Dockerfile names that image and at whether it can be reached. It used to end with the advice every build failure gets, to build the image with Docker and import it; Docker, asking the same registry for the same name, is refused the same way, unless it already holds that image locally or is logged in where the runtime is not. The runtime's own answer, which names the request, is still shown above it. Every other build failure keeps the Docker way round.

## [0.31.0] - 2026-09-16

### Added

- The YAML tags `!reset` and `!override` are read as docker compose v5.5.0
  reads them: a key tagged `!reset` (`ports: !reset []`, a whole service
  `db: !reset {}`) is taken away, and a key tagged `!override`
  (`tmpfs: !override [/u]`) keeps the later value whole instead of merging —
  across `-f` files, over an `include`, in an `extends`, and in a file alone.
  They used to be left unread or refused: `!reset []` and `!override [...]`
  merged like plain values, `!reset null` was read as the string `null` (a
  `tmpfs` path or a `command` of `null`), and `depends_on: {db: !reset
  null}` was refused. Not read yet: a tag reached through a YAML alias or
  merge key (`<<: *base`), and, in an `extends` from another file, a
  `networks:` or `depends_on:` list of the extended service.

### Changed

- A container of a service's name that carries no `opossum.project` label —
  one made outside opossum, such as with `container run --name db` — is no
  longer taken for the project's own. `up` and `run` refuse it, naming the
  container and how to free the name (a host port it holds that a service
  publishes is still reported first, as before), as docker compose v5.5.0
  refuses a container of the name without its labels (`Conflict. The
  container name … is already in use`); `down`, `stop`, `start`, `kill` and
  `restart` leave it and say so, `exec` and `cp` refuse it, `ps` gives it no
  row and names it on stderr, `port` reports no container of this project's,
  and the restart supervisor does not start it (`ps` and `port` treat another
  project's container of the name the same way). Before, `up` reused such a
  container, `down` removed it and `ps` listed it as the service. The
  containers opossum itself makes are not affected: every service container
  (since v0.1.0) and every one-off `run` makes carries the label.
- Two mounts at one container path that docker compose v5.5.0 refuses are
  refused here too, in its words (`services.web.volumes[0]: target /t
  already mounted as services.web.tmpfs[0]`): two `tmpfs:` entries not alike
  up to their first `=`, or a `tmpfs:` entry and a `volumes:` entry at its
  target. They are refused for the services the active profiles leave
  enabled — not for a `profiles:` service enabled by naming it on the
  command line, as docker compose does not refuse it — and by the commands
  that read the project, save `down`, `destroy`, `stop` and `kill`, which
  name the pair on stderr and go on, so a project an earlier opossum started
  still comes down (and `doctor`, which reads it for its memory estimate). They used to be passed on, and container 1.4.1 mounts
  the first of two `tmpfs:` entries and drops the second, or mounts both a
  `tmpfs:` and a `volumes:` entry with the `volumes:` one on top, silently.
  Commands that take no `--profile` do not read `COMPOSE_PROFILES`, so a
  gated service's pair is refused by `up`, `run` and `config` only.
- `logs` and `stats` read only this project's containers, as `ps` lists only
  those: a container of a service's name that belongs to another project,
  carries no `opossum.project` label (made outside opossum), or that the
  runtime gives no readable answer about is left out and named on stderr,
  where it used to be read or measured as the service. With every container
  asked for someone else's, they print nothing and exit zero, as docker
  compose v5.5.0 does. `ps`, `logs` and `stats` now exit non-zero, naming the
  containers, when the runtime gave no readable answer about any of them,
  as `down` does; `ps` and `port` used to read such a container as not there
  (no row, silently; `port` said "not running"), and `port` now reports no
  container of this project's.
- `logs` prefixes every line with its service as docker compose v5.5.1
  writes it off a terminal (`web-1  | hello`), for one service as for several and whether or
  not it follows; `--no-log-prefix` prints the lines as the container wrote
  them. Before, one service's lines were bare, several services without
  `--follow` were shown under `==> web <==` headers, and `--follow` used
  the bare service name (`web | hello`) and dropped a carriage return at a
  line's end. A script reading `opossum logs web` line by line needs
  `--no-log-prefix` for bare lines (a last line the container left without
  a newline now ends with one). A Ctrl-C or a SIGTERM now ends
  `logs` with exit 130 and nothing said, as there, where `--follow` over
  several services exited 0, and a SIGTERM to one service's `--follow`
  exited 143 and left the runtime's `container logs -f` running
  (container 1.4.1).

### Fixed

- Two mounts at one container path are read as docker compose v5.5.0 reads
  them in more cases. Among `volumes:` the later entry is kept, whatever the
  types — two long-form tmpfs mounts, or a long-form tmpfs and a bind, used
  to reach container 1.4.1 as two mounts (of two tmpfs mounts it mounted the
  first), and a `nocopy` on the earlier entry no longer keeps the later
  volume from being filled from the image. An entry a later one replaces is
  not looked up afterwards, as there: an undefined volume there used to
  refuse `config`, an `external: true` volume that does not exist used to
  refuse `up` and `run` (`[OPSM-210]`), and a bind there used to have its
  host directory made; none of that happens now (the entry's own spelling
  is still checked). Of two service-level `tmpfs:` entries alike up to the
  first `=` the later is kept (`/t:size=1m`, then `/t:size=2m`), also across
  `-f` files and `extends`, where both used to be passed on.
- `up` and `run` refuse a `tmpfs:` entry whose options hold an empty one
  (`/t:exec,,size=1m`, `/t:exec,`, `/t:defaults,`) before anything is
  created, naming the service and the entry, after a missing `external`
  volume (and, for `up`, a missing `external` network). The docker engine
  29.7.2 refuses the container docker compose v5.5.0 creates for it
  (`invalid tmpfs option ""`); container 1.4.1 mounted it, so such a file
  started here and not there. `/t:` alone still mounts with the defaults,
  and `config` and `up --dry-run` still read such a file, as docker
  compose v5.5.1 does.
- A service's `tmpfs:` or `env_file:` written as a string (`tmpfs: /t`) in one
  `-f` file, in an included file, or in a service another `extends`, now
  merges with the other file's or service's value as a list, as docker compose
  v5.5.0 merges them. The later value used to replace the earlier one whenever
  either was a string, so a tmpfs mount or an env file from the earlier file
  was silently dropped.
- A service named twice on the command line (`opossum logs web web`) is
  handled once, in the position it was first named: `logs`, `stats`
  (`stats --host web web` used to show the container twice and count it
  twice in the total), `start`, `stop`, `restart`, `kill`, `build`, `pull`
  and `import` used to handle it once per mention. docker compose v5.5.0
  runs `logs`, `up`, `start`, `stop`, `restart`, `kill` and `pull` once for
  such a name too (its `stats` takes one service at most). And when `logs`
  or `stats` fails to read this project's containers, the error still names
  the containers the runtime gave no readable answer about, where it used to
  name them on stderr only.
- A service stopped by `opossum kill` stays stopped: the restart supervisor
  used to bring a `restart: always` (or `unless-stopped`, `on-failure`)
  service back seconds after it was killed, where it leaves one `stop`
  stopped. docker compose v5.5.0 does not restart a container `kill`
  stopped, whatever the signal, and restarts it on exit again once `start`
  has brought it back; `up`, `start` and `restart` bring supervision back
  here. And a kill container 1.4.1 fails now exits 1 and names the service
  when the container is then found running (`-s BOGUS`) or the runtime gives
  no readable answer about it, as docker compose exits 1 for both, where it
  used to exit 0; every service is still signalled.

## [0.30.0] - 2026-09-15

### Changed

- A tmpfs mount (`tmpfs:` or a `type: tmpfs` volume) is now mounted with
  docker's default options `nosuid,nodev,noexec`, put before the options the
  file writes. On container 1.4.1 a file in a tmpfs mount used to run, and a
  device node in it to open, where docker refuses both; an `exec`, `suid` or
  `dev` written on the mount (`/tmp:exec`) still wins, as on docker engine
  29.7.2. A container with a tmpfs mount is made again on the first `up` after
  this change, since what it is made with changed; others are left as they are.

### Fixed

- `opossum watch` no longer tells you to "check the container is running" when
  a sync was skipped because the container belongs to another project or the
  runtime would not say whose it is, nor that the container "may be gone" when
  a restart was skipped for the second reason. The warning now says the change
  was skipped: for another project's container it keeps the advice to give
  this project its own DNS domain, and when the runtime did not answer it says
  to save that file again (a sync) or that watch tries the restart again when a
  file under one of the service's sync+restart rules next changes (a restart).
- `opossum restart` no longer leaves services stopped when one of them fails to
  start: it used to stop every service, start them again in order, and give up
  at the first failure, so the services after it stayed down. `opossum start`
  gave up the same way and left the later services unstarted. Both now go on
  to the other services and report each failure, together with any container
  whose owner the runtime would not say. `opossum start` does not start a
  service whose dependency failed to start in the same run (as docker compose
  does when a dependency's container fails to start), and starts named
  services in dependency order.
- A volume's name under top-level `volumes:`, and the `source` of a
  `type: volume` mount, may now only contain letters, digits, `.`, `_` and
  `-`, as docker compose requires of those names. Any other character used to
  be passed to the runtime as written (for example ``-v demo_a/b:/y``), and a
  `type: volume` source with `:` in it was reported cut at the colon. Both are
  now refused when the file is loaded, with the name shown whole.
- The shared named volume warning from `opossum up`, and the matching
  suggestions in the `compose.opossum.yaml` that `opossum up
  --from-docker-compose` writes, list the volumes by the names written in the
  compose file, sorted character by character. A volume whose name starts with
  `.` used to be listed ahead of names it sorts after, such as `-m`.
- A short volume entry is now read as `SOURCE:TARGET` or `SOURCE:TARGET:MODE`,
  the way container 1.4.1 splits it (and docker compose, apart from reading a
  one-letter source as a Windows drive). One with four or more fields is
  refused as having too many colons, as docker compose refuses it, and one
  whose third field is a path (for example ``data:app:/y``, which container
  1.4.1 reads as the volume at `app` with mount options `/y`, then fails to
  start) is refused with the target it would have used. A long-form mount
  whose bind source or target contains `:` is refused too, instead of being
  split apart at the colon.
- A volume a service mounts whose name goes to the runtime as written, a
  declaration's `name:` or the key of an external volume without one, is
  refused when the file is loaded unless it starts with a letter or digit and
  continues with letters, digits, `_`, `.` or `-`. container 1.4.1 and the
  docker engine 29.7.2 both refuse to create any other name, and docker compose
  v5.5.0 checks neither place in `config`, so such a name used to be passed on
  to the runtime unchanged. A declaration no service mounts is not checked, as
  docker compose ignores it too.
- An anonymous volume whose path holds a character other than ASCII letters,
  digits, `_`, `-`, `/`, `.` and spaces (`/etc/apk+x`, `/data@2`,
  `/données`, a tab) no longer fails `opossum up` or `opossum run` with
  `invalid volume name`. The volume is named from the project, the service and
  the path, and container 1.4.1 creates only volume names of ASCII letters,
  digits, `_`, `.` and `-`; such a character in the path is now written `_`,
  as `/`, `.` and spaces already were. A path without one keeps the name it
  had, so a volume made by an earlier `up` is found again.
- `opossum up`, `opossum run` and `opossum run --audit` refuse a container
  whose name would be longer than the 63 characters container 1.4.1 takes,
  before they create or start anything, and say what to shorten. A service's
  container is named `<service>.<project>.<domain>` (`opossum` by default) and
  a one-off's `<service>-run.<project>.<domain>`, so a long directory name used
  as the project name was enough: `up` used to create the project's network,
  start the services that come before in startup order, and then fail with
  `is not a valid container ID`. The advice offers the service name, the
  project name and a shorter `--dns-domain`, or only the service name when
  there is no DNS domain and the container is named by the service alone. The
  name is not shortened, because peers look the service up by it. docker
  compose v5.5.0 runs such names. For `run`, a dependency's container name is
  refused when the dependencies are started, before any of them is created
  (for `run --audit`, after the workspace is snapshotted).
- A new volume whose name on the runtime is longer than 50 characters is now
  filled from the image: a named volume (its `name:`, or `<project>_<key>`) as
  well as an anonymous one (`<project>_<service>_<path>_<hash>`), for both
  `opossum up` and `opossum run`. opossum fills a new volume with a throwaway
  container named `seed-<volume>.opossum`, and container 1.4.1 refuses a
  container name longer than 63 characters (`is not a valid container ID`), so
  opossum warned `[OPSM-108] couldn't fill the new volume` and the volume
  mounted empty. Such a seed container is now named with the start of the
  volume's name and a hash of the whole name, within 63 characters; a volume
  whose seed name already fits keeps it.
- A declared network whose key container 1.4.1 does not take in a network name
  (upper case as in `backEnd`, a character outside a-z, 0-9, `.`, `_` and `-` as
  in `a+b`, or a trailing `-`, `.` or `_`) no longer fails `up` with
  `invalid network name`. The key is folded to lower case, with `-` for each
  refused character and the trailing ones dropped, so `backEnd` runs as
  `<project>-backend`; a key the runtime already takes keeps its network name.
- `opossum up` and `opossum run` (with or without `--audit`) refuse two networks
  that services of the project join and that fold to the same name (`backEnd`
  and `backend`), since docker compose v5.5.0 gives each its own network. This
  includes a network whose key folds to `net` (`net` itself, or `NET`) while
  another service is on the default network: both used to be the one network
  `<project>-net`, so those services could reach each other where docker compose
  keeps them apart. Every service of the file counts, including one behind a
  profile that is not active, since starting it later would put it on the shared
  network. `opossum down`, `destroy`, `ps` and `config` still read such a file.
- `opossum up` and `opossum run` (with or without `--audit`) also refuse a
  network they would create whose name would be longer than the 63 characters
  container 1.4.1 creates (the default `<project>-net` or a declared
  `<project>-<key>`; for `run`, the networks of its dependencies too, unless
  `--no-deps`), and say to shorten the project name or the key. Both refusals
  come before any container or network is removed, created or started, and,
  for `run --audit`, before the workspace is snapshotted, except a
  dependency's network that is too long, which is refused when the
  dependencies are started, after the snapshot.
- A network a service joins is refused when the file is loaded, instead of being
  passed on to the runtime, if it is external and its real name (its `name:`, or
  its key when it has none) is one container 1.4.1 cannot create, or if its key
  keeps no character once folded. A declaration no service joins is not checked.
- `opossum run --audit` no longer starts a dependency it names directly when
  that dependency is behind a profile that is not active. It refuses the run
  instead, the way `opossum run` already did
  (`service "web" depends on "db", whose profile is not active`), before it
  snapshots the workspace or starts anything. docker compose v5.5.0 refuses
  such a `run` as well, with or without `--no-deps`; opossum still lets it go
  ahead with `--no-deps`, without the dependency, as before. A dependency of a
  dependency behind such a profile was already refused, and still is, by the
  step that starts the dependencies (after the snapshot, for `run --audit`).
- `opossum up`, `opossum run` and `opossum run --audit` refuse, before they
  create or start anything, a service whose name container 1.4.1 cannot name a
  container with: one starting with `_`, `.` or `-`, holding a character other
  than ASCII letters, digits, `_`, `.` and `-` (`_web`, `a+b`), or, without a
  DNS domain, a single character. `up` used to create the network and start
  the services before it, then fail with `is not a valid container ID`. The
  refusal says to rename the service, or to use a DNS domain of those
  characters when the domain is what does not fit.
- `opossum up` also refuses two services it would start whose names differ
  only in case (`Com` and `com`): container 1.4.1 fails the second container
  with `failed to bootstrap container`, naming neither service. A file that
  never starts both (one behind a profile that is not active) still runs.
- `opossum up` warns (`[OPSM-209]`) about a service other services may not
  reach by name although its container runs: on container 1.4.1 a service
  name with upper case gets no DNS answer (`MyDb`), or another address when
  spelled like a top-level domain (`Web`), and one with `.` gets none
  from a musl image such as alpine, and an internet address when one exists
  (`web.dev`). The service still starts under the same name, so a project
  brought up before keeps its containers; rename it in lower case if another
  service reaches it by name. docker compose v5.5.0 looks up `MyDb` and
  `web.dev`.
- `opossum up` starts a service with an anonymous volume on a long path
  (`- /very/deep/…`) whose volume name would pass 255 characters: the path part
  of the name is cut to fit, keeping the start of the path and a hash of the
  whole path. container 1.4.1 refuses a longer volume name, so `up` used to
  warn that it could not fill the volume and then fail starting the service. A
  name that already fit is unchanged, so an existing volume is still found.
- `opossum up` and `opossum run` refuse, before they create or start anything,
  a service mounting a volume whose name is longer than the 255 characters
  container 1.4.1 creates: a volume's key under the project name, its `name:`,
  or an external volume's name. The refusal names the service, the volume and
  what to shorten. `opossum run --audit` refuses the one-off's own volumes
  before it snapshots the workspace, and its dependencies' when it starts them,
  after the snapshot. `up` used to create the network before failing to start
  the service; docker compose v5.5.0 fails creating a volume of such a key or
  `name:` too.
- A long-form tmpfs mount's `read_only`, `tmpfs.size` and `tmpfs.mode` now
  take effect: they are passed as the mount options `ro`, `size=` (in bytes)
  and `mode=` (in octal), which container 1.4.1 mounts the way docker engine
  29.7.2 mounts the long form. They used to be dropped — `read_only` without a
  word, the `tmpfs:` options as ignored fields. A size is read as a whole
  number of bytes or digits with a k, m, g, t or p unit, and a mode as a whole
  number (`0755`), as docker compose v5.5.0 reads them, and what it refuses is
  refused at load; a size or mode written as a YAML float (`1.5`, `1e3`, `08`),
  a size string in another spelling (`.5m`, `+1m`, `"-1"`) and a whole number
  out of range (a mode past 4294967295, a size past 9223372036854775807) are
  refused too, though docker compose reads some of them. A mount's `tmpfs:`
  that is not a mapping, and on a bind or volume mount a size or mode refused
  on a tmpfs mount, are refused as docker compose refuses them.
- A tmpfs mount written with the `defaults` option (`/tmp:defaults,size=1m`)
  now starts: the option is dropped before the mount reaches the runtime, as
  docker engine 29.7.2 reads it as nothing wherever it stands. container 1.4.1
  failed the mount with errno 22, so the service did not start.
- `opossum up`, `opossum run` and `opossum run --audit` refuse a volume
  declared `external: true` that does not exist (`[OPSM-210]`), before they
  create or remove anything, as docker compose v5.5.0 refuses it. On container
  1.4.1 the service used to start on a new, empty volume of that name. The
  refusal says to create the volume or drop `external: true`. `up` looks at the
  services it starts; `run` at the one-off's service and its dependencies all
  the way down, with `--no-deps` too, as docker compose does. A runtime that
  gives no volume list is not taken as the volume being missing.
- `opossum up --remove-orphans` refuses a missing external network
  (`[OPSM-205]`) before it removes an orphan, and says it before a missing
  external volume, as docker compose v5.5.0 does; it used to remove the orphans
  first.
- A declared volume named `NAME` (`name: NAME`) that does not exist yet is
  prepared and filled from the image like any other new volume; opossum used to
  read the header of container 1.4.1's volume list as that volume.

## [0.29.0] - 2026-09-14

### Added

- `ps`, `images` and `stats --no-stream` accept `--format json`, printing an
  array of objects that each carry the service name beside the columns, so a
  program no longer has to parse the table or match container names back to
  services. As in the table, a service with no container has no row in `ps`;
  a column the table shows as `-` is an empty string. `stats` has no row for a
  service whose container is stopped or absent, and refuses `--format json`
  without `--no-stream` or with `--host`.

## [0.28.0] - 2026-09-14

### Changed

- ``opossum run`` and ``opossum exec`` now exit with the container command's own exit code, as docker compose does: a one-off that exits 3 makes ``opossum run`` exit 3 (it used to exit 1 for every failure), so a script can tell how the command failed. ``run --audit`` exits with the code its report shows, or 1 when the report shows -1. A failure of opossum itself before the command runs — an unknown service, a dependency that does not start or complete — still exits 1, and so does a runtime process killed by a signal. A command that cannot be run (not found, not executable) exits 1 under ``opossum exec``, where docker compose exits 127 or 126, because the runtime reports it as 1. The audit report's exit code for a failure before the one-off runs, such as its build, is now -1; it used to show the failing runtime command's code.

### Fixed

- A long-form mount with `type: volume` and a source starting with `.` (for example `source: .hidden`) is now the declared volume of that name, created as `<project>_.hidden` the way docker compose creates it. It was refused before. The short spelling `.hidden:/data` is still a directory beside the compose file, as docker compose reads it, and `opossum config` prints the volume back in the long form.
- When the runtime cannot be asked about a container (the CLI fails for a reason other than "not found"), opossum no longer reads that as "gone": a failed `up`'s rollback and `destroy` say the removal could not be confirmed instead of reporting it done, and the restart supervisor keeps watching through the outage instead of counting it towards "nothing left to watch" — it logs the outage, and stops only when the runtime itself is down, saying so — rather than ending with a line claiming none of the containers exist while they are running.
- `up` no longer force-deletes a container whose owner the runtime could not report. When `container inspect` failed for a reason other than "not found", or gave an answer opossum could not read, on a container `up` was about to reuse, the check for another project's container read that as "no owner", and `up` deleted that container and ran its own in its place — even if it belonged to another project sharing the DNS domain — without saying so. `up` now stops before creating anything, names the container, and says what to check: `the runtime gave no readable answer about which project owns it, so it is left alone`.
- `down` no longer stops and deletes a container that belongs to another project. It found containers by name only, so when another project's container carried the same name — as happens with `--dns-domain ""`, where every project names its containers after the bare service — `down` removed it without saying so, even from a project that was never brought up. `down` now leaves such a container alone and prints `Leaving container <name> alone: it belongs to project "<other>"`; a container the runtime gives no readable answer about is left too, and `down` names it and exits non-zero once the rest of the stack is down. `run` likewise refuses, before starting anything, when the name of its one-off container is held by another project or cannot be answered for, instead of deleting it.
- ``opossum start``, ``stop``, ``kill`` and ``restart`` no longer act on another project's container that has the same name as one of this project's services (which happens with ``--dns-domain ""``, where containers are named by the bare service name). They leave it, say so, and act on the rest of the project; a container the runtime cannot say the owner of is left too, and named in the error. ``opossum exec`` and ``opossum cp`` refuse such a container instead of running a command or copying files inside it.
- When the restart supervisor stops for having nothing to watch, its log says none of the services has had a container of this project's, rather than that none exists: another project's container may still hold the name. It also names the new project when a service's name moves from one other project's container to another's.
- The restart supervisor no longer restarts another project's container that has taken the name of one of this project's services after ``opossum up`` (which can happen with ``--dns-domain ""``, where containers are named by the bare service name). It leaves the container, says so in the supervisor log, and does not count it as this project's.

## [0.27.1] - 2026-09-13

### Changed

- A service that mounts a named volume the file does not declare under top-level `volumes:` is now refused when the file is read, as docker compose refuses it (`service "db" refers to undefined volume "dbdata"`). Before, a misspelling on either side quietly created an empty volume under the misspelt name and mounted it, so a database could initialise fresh where existing data was expected. A path written as `/…`, `./…`, `../…` or `~/…` (or `.`/`..` itself — a bind mount) and a bare target (an anonymous volume) need no declaration; a file that relied on the old behaviour needs a one-line `volumes: {dbdata: {}}`.
- A volume source that starts with `.` or `~` is now read as a host path, as docker compose reads it: `.hidden:/y` bind-mounts the hidden directory beside the compose file and `~:/y` your home. Before, such a source named a volume — `.hidden/sub` even became a volume "name" containing a slash, which the runtime then rejected as a path that does not exist. A source written `~name…` is refused rather than resolved (docker compose reads it as `$HOME/name…`, which nobody means): write `~/name…` or an absolute path. In the long form the `type:` decides: `type: bind` with a bare `source: data` now bind-mounts the directory `data` beside the compose file (it was refused as an undeclared volume), and `type: volume` with a source starting with `.` is refused, as such a volume name cannot be carried here yet (docker compose creates it as `<project>_.hidden`).

### Fixed

- `up` no longer rolls back a container it did not create: when the runtime refuses a start because a container of that name already exists (created in the moment between `up` freeing the name and starting it, by something the project lock could not see), `up` reports the service up to date if that container is running with this compose file's configuration, and otherwise refuses and leaves it standing for you to inspect. A run-to-completion dependency in that position is always refused, since only running it would tell whether it completed.
- Pressing Ctrl-C during `up --foreground` (or while a run-to-completion dependency is running) no longer reports the attached service as a start failure with a pointer to `opossum logs`; `up` now says it was interrupted and rolls back, as it already did for a Ctrl-C between services.
- Pressing Ctrl-C while an `opossum run` one-off is running now stops the one-off container (and removes it under `--rm`) and reports the interruption; before, the process died of the signal with nothing said and the container kept running.
- A Ctrl-C during `up` while it is creating the project network, building an image, or filling a new volume from the image now reports the interruption instead of a runtime, builder, or seeding failure with advice that does not apply; an interrupted volume fill also takes back what it left behind (the throwaway seeding container, which `--rm` cannot remove when the run is killed from outside, and the half-filled volume), so the next `up` starts from nothing rather than from part of the image's content. The same applies to `opossum run` before its one-off starts (its network, its service's build, and a new volume's fill): the interruption is reported as such, and dependencies it had already started are left up, as before.
- Interrupting `opossum run` with Ctrl-C now checks that its container really stopped before saying so: `container stop` reports only whether the name exists, so opossum asks the runtime afterwards, and if the container is still running (or, with `--rm`, still there) it says that instead, with the command that finishes the job.
- `OPSM-103` (a named volume already attached elsewhere) no longer tells you to stop the holder when that holder is the volume's own seeding container, still filling it from the image for another `up` or `run`; it now says to wait for the fill, since stopping it would leave the volume half-filled and the next start would take it as already there.
- `opossum config` now writes a literal `$` back as `$$`, as docker compose config does. Its output loaded again therefore keeps the same values: a healthcheck's `$${POSTGRES_USER}` stays for the container's shell instead of expanding to nothing on the host, and `$$HOME` no longer turns into your machine's path. Before, values with `$` changed meaning each time the output was fed back in.
- A project volume declared with a `name:` is now created, mounted, seeded, listed by `volumes` and removed by `down -v` and `destroy` under that name, as docker compose does; before, the name was ignored and `<project>_<key>` was used everywhere, so a volume meant to be found by name — by a backup script, or shared with another project — silently lived somewhere else. `opossum config` writes the `name:` too. As on docker compose, a `name:` volume is the project's: `down -v` and `destroy` remove it — including one that already existed under that name, which earlier versions never touched — so declare a volume `external: true` to keep it out of the project's hands.
- `opossum config` now writes the top-level `volumes`, `secrets` and `configs` declarations and each service's `secrets`/`configs` references, so its output run back means what the input meant: an `external: true` volume stays external (real name, never seeded or removed) instead of becoming a namespaced, seeded volume of the project, and secret and config mounts are no longer dropped.
- `destroy` now lists a volume the compose file declares with a `name:` of its own when no service mounts it any more, alongside the `<project>_` leftovers it already reported: such a volume carries no project prefix, so it used to go unmentioned — and a teardown would say everything was gone while it stayed on disk.

## [0.27.0] - 2026-09-13

### Added

- `configs` is now read, the way docker compose reads it: a top-level config becomes a read-only file in the container — from a host file (`file:`), from text in the compose file (`content:`, interpolated like any value), or from a variable's value (`environment:`) — and a service names the ones it takes, at `/<name>` or at its own `target`. A reference to an undeclared config, a declaration with none or more than one of the three forms, and a `configs` that is not a list are refused with the reason; `external` configs are refused; `uid`, `gid` and `mode` are read and listed as ignored, as docker compose ignores them. Before this the key was ignored and only listed by `opossum config`.
- `mac_address` is now given to the container: the address goes to the service's first network (`container run --network <name>,mac=…`), so a service that must keep a fixed MAC — for a licence tied to it, or a DHCP reservation — gets it, as under docker compose. Any usual spelling is taken and passed in the colon form; an address that is not a 48-bit MAC, or one on a service with `network_mode: none`, is refused with the reason. Before this the key was ignored and only listed by `opossum config`.
- Service `labels` are now put on the container, in the mapping form or the list form (a bare `key` is the empty value, as docker compose reads it), with the values interpolated like the rest of the file, and shown by `opossum config`. opossum's own labels are added after them, so a label spelled like one of ours is overridden by ours — as docker compose's own labels win over a clash. A network declaration's `labels` go on the network when opossum creates it. Before this both were ignored and only listed by `opossum config`.
- `shm_size` and `ulimits` are now given to the container: `shm_size` sets the size of `/dev/shm` (written as `64M`, `1gb` or a byte count, as under docker compose), and `ulimits` sets resource limits, a number for soft and hard alike or `{soft, hard}` to set them apart — so a database or browser image that needs a larger `/dev/shm` or more open files gets it. A `shm_size` that is not a size, a `ulimits` that is not a mapping, and a limit that is not a whole number are refused with the reason. Before this both keys were ignored and only listed by `opossum config`.
- `opossum port <service> <container-port>` prints the host side of a published port on one line (`0.0.0.0:65345`), as `docker compose port` does, with `--protocol tcp|udp`. opossum maps `ports: - "3000"` to 3000 on the host when that is free and to a free port when it is not, so the host port is not always the one in the file; this is the way to read it from a script. A port that is not published is refused with the list of those that are, and a service whose container is absent or stopped with `service "web" is not running`.
- `opossum ls` lists the opossum projects on this machine — every project that has containers, found by the label opossum puts on them — as a NAME / STATUS table with a count of containers by state (`running(2)`, or `running(1), stopped(1)`), the way `docker compose ls` does, and needs no compose file. A project with no running container appears only with `--all`; `-q` prints names only and `--format json` an array of `{Name, Status}`. Until now the only way to see what was running across projects was to read `container ls` and pick the names apart.
- `opossum volumes [service…]` lists the volumes the file's services mount that exist on the runtime, under the names the runtime knows them by (`<project>_<volume>`), as a DRIVER / VOLUME NAME table, the way `docker compose volumes` does; named services narrow it to what they mount, `-q` prints names only and `--format json` an array of `{Name, Driver}`. A volume appears once a service mounting it has started; external volumes, and volumes the file no longer mounts, are not listed. Until now the only way to find a project's volumes was to read `container volume ls` and pick the prefixed names apart.
- A top-level network's `ipam.config` subnet is now given to the runtime (`container network create --subnet`, or `--subnet-v6` for an IPv6 one), so a project can pin the address range its network uses, as under docker compose. One IPv4 and one IPv6 subnet at most; a subnet not in CIDR form, or a second one of the same family, is refused at load naming the network, where docker compose refuses them when the network is made. A project network that already exists with another subnet is kept and `up` says so (`OPSM-207`): `down`, then `up`, recreates it. Until now `ipam` was listed as ignored.

### Changed

- `up` no longer warns about a build context under `/private/tmp` or reached through a symlink (the former `OPSM-301`/`OPSM-302` notes): since Apple `container` 1.4.1 the builder is sent the context as an archive and reads both, measured with a `COPY` from each. opossum does not resolve symlinks in the path (a relative context is still taken from the compose file's directory, as before).
- A key docker compose does not take — a typo such as `enviroment:` on a service, a `build.foo`, a top-level `servcies:` — is now refused before anything runs, naming the key and, for a top-level key or a declaration's, the line, as docker compose refuses it (`additional properties 'foo' not allowed`). Until now such a key was read past and listed among the ignored fields, so a service could start without its variables and the mistake surfaced later. A key docker compose takes that opossum does not act on is still listed rather than refused, and an `x-` key is taken anywhere. The keys come from the compose specification's schema.

### Fixed

- `--profile '*'` and `COMPOSE_PROFILES=*` now activate every profile, as docker compose reads them, so every gated service starts (a gated dependency resolves too). `*` is special only there: a partial pattern such as `to*` is an ordinary profile name, and a service declaring `profiles: ["*"]` stays gated until something activates it. A `profiles` written as one value instead of a list is refused naming the field, rather than in the decoder's words.
- Two `opossum up`s (or an `up` and a `down` or `destroy`) for one project no longer overlap: the second is refused at once with `[OPSM-208] another opossum command is changing project …`, naming the other's pid. Before this, an `up` started a second apart could lose the race for a container, fail, and roll back a service the other `up` had just reported as up to date — leaving the project short a service while that other `up` exited 0. The lock is held by the running command and released by the OS when it exits, so a command killed midway leaves nothing to clean up. The lock file lives under the project's state directory, which `up`, `down` and `destroy` now create if it is not there yet.

## [0.26.0] - 2026-09-09

### Added

- `extends` now reads a service from another file too (`extends: {file: base.yml, service: common}`), the way docker compose reads it: the named file is read from the project directory (the first `-f` file's, or the include entry's), the service's own `extends` is resolved first (a chain may run on into a third file, found from the named file's directory; a cycle through files is refused), and the paths it wrote relative to its file — `build`, a bind mount's source, `env_file`, `develop.watch` paths — resolve against that file's directory. Only the service comes over: the named file's top-level `volumes`, `networks` and `secrets` declarations do not. A missing file or service is refused naming the file. Measured against docker compose v5.5.0.
- A compose file may now `include:` other files, read the way docker compose reads them: each entry names a file (or, in the long form, one or several under `path`, with `project_directory` and `env_file`), read as a project of its own — its relative paths (including a nested `include` and a secret's `file`) count from its project directory, its own `include` and `extends` are resolved, and its variables come from that directory's `.env` (or the entry's `env_file`) under the including project's shell and `.env`; its services and its `volumes`/`networks`/`secrets`/`configs` declarations come over (not its `name`), merged in order under the including file, whose settings win where both define a service, and a service of the including file may `extends:` an included one. A file that is not there, an include that comes back to a file, and an `include:` that is not a list are refused naming the file. Until now a file with `include:` was refused outright.

### Changed

- Verified against Apple `container` 1.4.1: the command outputs opossum reads were re-taken on it and nothing it reads changed — `container system status` gained rows (client, host, server, paths, resource counts) but still carries the `status running` line the readiness check looks for, the JSON fields `inspect` and `ls --format json` are read from are all still there (only `/` is no longer escaped), and the error wordings are the same. The fake runtime used by the tests now prints the 1.4.1 status table. Nothing 1.4.1-specific needed fixing.
- `doctor` and the commands that check the runtime is up now read `container system status --format json` (Apple `container` 1.4.1) before the table, so the check does not depend on the table's rows; `doctor` reports the server's version and how many containers and images the system holds, and warns — with the restart as the fix — when the client and the server run different versions (an apiserver kept running across an upgrade). Where the JSON form is not there (1.3.1), the table is read as before.

### Fixed

- `env_file` written as an absolute path is read from that path. Before, the project directory was put in front of it, and the file was reported as not found under the project.
- `extends: {file: …}` now finds a relative path from the project directory — the first `-f` file's, or the include entry's — as docker compose does, where it was taken from the extending file's own directory (a file the named one extends in turn is still found from the named file's directory). And a service key an including file writes with nothing under it is the included service where one defines it, as in a later `-f` file, instead of a refusal.

## [0.25.0] - 2026-09-09

### Added

- A service may now `extends:` another service of the same file, read the way docker compose reads it: the named service's settings come first and the extending service's own go over them, merged by the rules a later `-f` file merges by (lists appended, mappings merged by key, `healthcheck` by sub-key); chains resolve in order, and a cycle or an undefined service is refused by name. With several `-f` files, each file's `extends` is resolved against the services that file defines, before the files are merged, as docker compose does. `extends: {file: …}` is still refused.

### Changed

- A value under a boolean field that is not a boolean (`read_only: "1"`, `init: maybe`, an env file's `required: 0`) is now refused naming the key and the line, with `true`/`false` as the way out, rather than in YAML's own words (`cannot unmarshal !!str into bool`); the value itself is not repeated, since it may have come from a variable. The YAML 1.1 `y`/`n` are read as booleans, as they were.
- A top-level `networks`, `volumes` or `secrets` written as a list or a single value is now refused naming the key and the line, with `name: {…}` as the way out, rather than in YAML's own words with a Go type standing in for the field.

### Fixed

- Escapes inside a quoted `.env` or `env_file` value are now read the way docker compose reads them: between double quotes `\"`, `\\`, `\$` stand for the character itself and `\n`, `\t`, `\r` for the control character (`KEY="say \"hi\""` is `say "hi"`); between single quotes only `\'` is read. Before this every backslash was kept as written.
- In the older map form of `external` (`external: {name: x}`), a `name` written as a number, a boolean, a list, a mapping or nothing is now refused at load the way docker compose refuses it, naming the line and how to quote it, rather than read as the name `42` or as no name.
- With several `-f` files, a later file's `external: true` no longer drops the name an earlier file gave in the map form (`external: {name: x}`): the map form is read as `external: true` with `name: x` before the merge, the way docker compose reads it, so the name survives, a later `name:` still wins, and a later map with a different name is refused as a conflict, as docker compose refuses it.
- A short-form port with a part written as nothing (`::80`, `:8080:80`, `80/`, `80:80/`) is now read as the ports that are there, the way docker compose reads it, rather than passed to the runtime as written, where `container run -p` refused it at `up`.
- A short-form port with an IPv6 host address written without brackets (`::1:8080:80`, which docker compose reads as the address `::1`) is now passed to the runtime bracketed (`[::1]:8080:80`), the spelling `container run -p` takes, rather than as written, where the runtime refused it at `up`.
- A top-level `name:` or `version:` that is not a string (`name: 42`, a bare `name:`), a top-level `networks:`, `volumes:`, `secrets:` or `configs:` with nothing under it, and `configs` that is not a mapping are now refused at load the way docker compose refuses them, rather than read as the project `42`, the directory's name, or nothing. A file with `include:` is refused by name — opossum does not read it, and the files it names were left out of the project in silence; pass them with `-f` instead.

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
  refusals do — `` ports entry 2 of 2 has no target — write the container
  port, as in `target: 80` `` — where it used to say only "port entry is
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
  for it, `up` now fails fast with `` container is not running … check `opossum
  logs <svc>` `` instead of an opaque "healthcheck did not pass".
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
  (`docs/real-runtime-review.md`) confirms
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

[Unreleased]: https://github.com/suruseas/opossum/compare/v0.42.1...HEAD
[0.42.1]: https://github.com/suruseas/opossum/compare/v0.42.0...v0.42.1
[0.42.0]: https://github.com/suruseas/opossum/compare/v0.41.0...v0.42.0
[0.41.0]: https://github.com/suruseas/opossum/compare/v0.40.1...v0.41.0
[0.40.1]: https://github.com/suruseas/opossum/compare/v0.40.0...v0.40.1
[0.40.0]: https://github.com/suruseas/opossum/compare/v0.39.0...v0.40.0
[0.39.0]: https://github.com/suruseas/opossum/compare/v0.38.0...v0.39.0
[0.38.0]: https://github.com/suruseas/opossum/compare/v0.37.0...v0.38.0
[0.37.0]: https://github.com/suruseas/opossum/compare/v0.36.0...v0.37.0
[0.36.0]: https://github.com/suruseas/opossum/compare/v0.35.0...v0.36.0
[0.35.0]: https://github.com/suruseas/opossum/compare/v0.34.1...v0.35.0
[0.34.1]: https://github.com/suruseas/opossum/compare/v0.34.0...v0.34.1
[0.34.0]: https://github.com/suruseas/opossum/compare/v0.33.0...v0.34.0
[0.33.0]: https://github.com/suruseas/opossum/compare/v0.32.0...v0.33.0
[0.32.0]: https://github.com/suruseas/opossum/compare/v0.31.0...v0.32.0
[0.31.0]: https://github.com/suruseas/opossum/compare/v0.30.0...v0.31.0
[0.30.0]: https://github.com/suruseas/opossum/compare/v0.29.0...v0.30.0
[0.29.0]: https://github.com/suruseas/opossum/compare/v0.28.0...v0.29.0
[0.28.0]: https://github.com/suruseas/opossum/compare/v0.27.1...v0.28.0
[0.27.1]: https://github.com/suruseas/opossum/compare/v0.27.0...v0.27.1
[0.27.0]: https://github.com/suruseas/opossum/compare/v0.26.0...v0.27.0
[0.26.0]: https://github.com/suruseas/opossum/compare/v0.25.0...v0.26.0
[0.25.0]: https://github.com/suruseas/opossum/compare/v0.24.8...v0.25.0
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
