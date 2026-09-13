- `ps`, `images`, and `stats --no-stream` now support `--format json`, joining
  the service name onto each row so a caller no longer has to re-derive it
  from `container ls`'s `opossum.project` label.
