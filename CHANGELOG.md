# Changelog

All notable changes to mhelm. This project follows the
"auto-migrate with a stderr warning, don't hard-fail" backward-compat
ethos; behaviour changes ship as a new minor. See
[UPGRADING.md](UPGRADING.md) for migration steps.

## v0.7.0

Behaviour change: the Action now commits the lockfile (implementing the
contract the README always documented), and stale wrappers are now
impossible by construction.

### Added

- **The Action commits the lockfile.** New `commit-lock` CLI subcommand
  (`mhelm commit-lock [dir]`, flags `--push`, `--git-name`,
  `--git-email`, `--message`) and a new `commit` action input
  (**default `true`**, mirror mode only). After mirror+wrap+sign the
  Action stages `<dir>/chart-lock.json` (+ `image-values.yaml` when
  present) and commits/pushes it to the **checked-out** branch. The
  guard stages first then runs `git diff --cached --quiet`, so a
  brand-new untracked lock on the first run is committed (previously the
  hand-rolled `git diff --quiet` pattern silently discarded it) and an
  unchanged lock is a clean no-op. Commit message carries `[skip ci]`;
  push is preceded by `git pull --rebase --autostash` to survive a
  concurrent human push.
- **Wrap inputs fingerprint.** `mhelm wrap` records
  `chart-lock.json#wrap.inputsDigest` — a stable hash over normalized
  `chart.json`, every `discoveryValues` / `wrap.valuesFiles` /
  `wrap.extraManifests` file, the resolved upstream chart digest, and
  the lock-schema version. `mhelm wrap` **fails closed** when these
  inputs change while `wrap.version` is unchanged; new
  `--allow-version-reuse` escape hatch. `mhelm discover --check`
  recomputes the fingerprint and **exits 2** on mismatch, so the PR gate
  is exhaustive over wrap inputs, not just the discovered image set.
- `UPGRADING.md`, this `CHANGELOG.md`, and
  [`docs/lockfile-and-staleness.md`](docs/lockfile-and-staleness.md).

### Changed

- README's "the Action automatically commits lockfile updates" claim is
  now **true** (it was previously unimplemented); the minimal workflow
  needs no consumer-side git glue. Documented `commit`,
  `wrap.inputsDigest`, the `[skip ci]` behaviour, the drift-vs-mirror
  commit boundary, and exit codes.
- Example [`mirror.yml`](examples/workflows/mirror.yml) drops its
  `create-pull-request` step and `pull-requests: write` permission — the
  Action now owns the commit. Example workflow action refs bumped
  `v0.6.0` → `v0.7.0`.
- Defined edge semantics: `discover --check` with no committed lock →
  exit 2 ("would create"); `drift` with no committed lock → explicit
  non-zero error with a "run `mhelm mirror` first" hint, never a silent
  no-op (including under `--exit-zero`).

### Backward compatibility

- A pre-v0.7 lock with no `wrap.inputsDigest` is recomputed with a
  one-time stderr warning and never hard-fails on first encounter;
  `discover --check` reports `skip` until the next `mhelm mirror`
  records it.
- Consumers who hand-rolled a commit step must delete it (or set
  `commit: false`) to avoid a double commit — see UPGRADING.md.
