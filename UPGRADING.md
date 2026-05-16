# Upgrading mhelm

mhelm follows an "auto-migrate with a stderr warning, don't hard-fail"
backward-compat ethos: old `chart.json` / `chart-lock.json` shapes keep
working and are migrated in memory. The notes below call out the few
places a consumer must change something on a major/minor bump.

## v0.7 → v0.8

Purely additive — re-pinning to v0.8.0 needs no changes. v0.8.0 adds
the optional `mirror.excludeImages` field (the inverse of
`mirror.extraImages`): a `[{repo, reason}]` drop list for an image
discovery *finds* but the cluster never runs (e.g. a Windows-only image
on a Linux cluster), which previously hard-failed the Action's sign step
because syft/grype default to `linux/amd64`.

**Optional, only if you adopt it:** after adding `excludeImages` to a
`chart.json`, run `mhelm discover` (or let the Action's mirror run) to
refresh `chart-lock.json`. The discovered set changed, so until you do,
`mhelm discover --check` (the dry-run gate) correctly exits 2 reporting
the lock stale.

## v0.6 → v0.7

v0.7.0 makes the long-documented "the Action commits lockfile updates"
contract real and makes stale wrappers impossible by construction.

### 1. The Action now commits the lockfile — delete your hand-rolled commit step

Before v0.7.0, `action.yml` never committed: any consumer following the
README's minimal workflow produced signed OCI artifacts but **never
committed `chart-lock.json`**, so the deploy (which reads the committed
lock) could never run. Consumers who noticed hand-rolled a commit step —
and the obvious guard, `git diff --quiet -- <dir>/chart-lock.json`, is
blind to untracked files, so on the *first* run it reported "no changes"
and discarded the freshly generated lock.

As of v0.7.0 the Action runs `mhelm commit-lock` after mirror+wrap+sign,
in **mirror mode only**, gated by the new `commit` input
(**default `true`**). It stages first then `git diff --cached --quiet`,
so the untracked-first-run case is committed correctly and an unchanged
lock is a clean no-op.

**Action required when you re-pin to v0.7.0:**

- If you added your **own** commit step (a `git add` + `git commit`, or a
  `peter-evans/create-pull-request` adding `chart-lock.json`), you must
  **either delete it** (let mhelm own the commit — recommended) **or set
  `commit: false`** on the action. Doing both **double-commits**.
- The example [`mirror.yml`](examples/workflows/mirror.yml) dropped its
  `create-pull-request` step and `pull-requests: write` permission
  accordingly. Diff yours against it.
- Ensure the job checks out with a push-capable token
  (`actions/checkout`'s default `persist-credentials: true`) and the
  workflow grants `permissions: contents: write`. `fetch-depth: 0` is
  recommended so the pre-push `git pull --rebase --autostash` rebases
  cleanly against a concurrent push.
- `dry-run` and `drift` still never commit. Drift's branch+PR flow
  remains the caller's responsibility — `commit` covers mirror only.

The commit message carries `[skip ci]` so it does not re-trigger a
path-filtered mirror workflow.

### 2. `wrap.inputsDigest` — stale wrappers now fail closed

`mhelm wrap` now records a `wrap.inputsDigest` in `chart-lock.json`: a
stable hash over normalized `chart.json`, every `discoveryValues` /
`wrap.valuesFiles` / `wrap.extraManifests` file, the resolved upstream
chart digest, and the lock-schema version.

- **`mhelm wrap` fails closed** if those inputs change while
  `wrap.version` is unchanged ("same version must mean same bytes").
  Remediation: **bump `wrap.version`** in `chart.json`, or pass
  `--allow-version-reuse` for a deliberate in-place re-release.
- **`mhelm discover --check`** (the PR gate / `dry-run`) recomputes the
  fingerprint and **exits 2** on mismatch. A PR that legitimately changes
  a wrap input is expected to go red here until it lands and the mirror
  republishes; bump `wrap.version` in the same PR.
- A pre-v0.7 lock has **no** `wrap.inputsDigest`. It is recomputed with a
  one-time stderr warning and **never hard-fails on first encounter**;
  the version-reuse guarantee engages from the next `mhelm mirror`.

No `chart.json` change is required for this — but if you have a bespoke
"remember to bump wrap.version" convention or a separate dry-run
staleness gate workflow, you can drop it: mhelm now guarantees both.

### 3. New / changed surface

- New CLI subcommand: `mhelm commit-lock [dir]`
  (`--push`, `--git-name`, `--git-email`, `--message`) — usable from
  non-Actions CI or locally.
- New `mhelm wrap` flag: `--allow-version-reuse`.
- New action input: `commit` (default `true`).
- New lockfile field: `chart-lock.json#wrap.inputsDigest`.

## v0.1.0 → v0.2.0+ (schema)

The flat v0.1.0 `chart.json` / `chart-lock.json` (no `apiVersion`,
top-level `upstream`/`downstream`/`valuesFiles`/`trustedIdentities`) is
still auto-migrated in memory with a stderr warning; the file on disk is
never rewritten. Migrate to the nested `apiVersion: mhelm.io/v1alpha1`
shape at your convenience. `wrap.imageOverrides` and `wrap.name` are
removed (warned, ignored) — image rewrites are derived from
`chart-lock.json#mirror.images[].valuesPaths[]` and the wrapper reuses
the mirrored chart's name under the `platform/` namespace.
