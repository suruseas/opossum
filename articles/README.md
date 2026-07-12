# Articles

Drafts for external posts (dev.to, Zenn). Written in product language for a
general audience.

- `29-compose-projects-on-apple-container.md` — "should you switch yet?" post,
  two parts: Part 1 compatibility (29 awesome-compose samples unmodified — 14
  as-is / 4 one-line / rest not runtime-caused) and Part 2 performance (per-VM
  memory ~250-400MB, crossover ~3 containers, throwaway 4-10× slower). Honest
  verdict: pieces are here, but not yet for a daily multi-service stack —
  sweet spot is small/occasional/idle. Tags: `docker, macos, devops, containers`.
  - `assets/cover-29-compose-survey.svg` / `.png` — cover image (1000×420,
    the survey results bar; PNG is the 2× render for dev.to `cover_image`).
- `zenn-29-compose-on-apple-container-ja.md` — Japanese (Zenn) version of the
  same two-part "should you switch yet?" post, keeping the up-front three-way
  comparison (Docker Desktop / docker engine inside a persistent `container` VM
  / native via opossum) linking the full-compat VM route as complementary. Same
  honest verdict as the EN post. Zenn front matter (`emoji`, `type: tech`,
  `topics`, `published`); publish via the user's Zenn-connected repo, no cover
  image needed (emoji is the cover).
- `run-docker-compose-on-apple-container.md` — intro post: install opossum via
  Homebrew and run an existing `docker-compose.yml` with `opossum up`, with real
  command output (`up` / `ps` / `logs` / `stats` / `down`). Front-matter tags:
  `docker, macos, containers, devtools`. Works as the "getting started" link
  target from the survey post.
  - `assets/docker-compose.yml` — the sample stack used in the post.

Front matter uses each platform's format — dev.to (`title`, `published`,
`tags`, `cover_image`) or Zenn (`title`, `emoji`, `type`, `topics`,
`published`). Flip `published` to `true` when posting.
