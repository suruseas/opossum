# Articles

Drafts for external posts (e.g. dev.to). Written in product language for a
general audience.

- `29-compose-projects-on-apple-container.md` — survey post: 29 awesome-compose
  samples run unmodified on Apple `container` via opossum, results categorized
  (14 as-is / 4 one-line / rest not runtime-caused), with the failure analysis
  as the main content. Front-matter tags: `docker, macos, devops, containers`.
  - `assets/cover-29-compose-survey.svg` / `.png` — cover image (1000×420,
    results bar; PNG is the 2× render to upload as dev.to `cover_image`).
- `run-docker-compose-on-apple-container.md` — intro post: install opossum via
  Homebrew and run an existing `docker-compose.yml` with `opossum up`, with real
  command output (`up` / `ps` / `logs` / `stats` / `down`). Front-matter tags:
  `docker, macos, containers, devtools`. Works as the "getting started" link
  target from the survey post.
  - `assets/docker-compose.yml` — the sample stack used in the post.

Front matter uses dev.to's format (`title`, `published: false`, `tags`,
`cover_image`). Flip `published` to `true` when posting.
