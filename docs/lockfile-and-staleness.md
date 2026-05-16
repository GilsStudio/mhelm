# Lockfile commit & staleness contract (v0.7+)

This page is the normative spec for **who commits `chart-lock.json`** and
**how staleness is guaranteed**. It exists because a real outage traced
back to a tool-level contract defect: `mhelm-mirror` ran green and pushed
every artifact, but no `chart-lock.json` was ever committed, so the
deploy (which reads the committed lock) could never run.

## Who commits the lock

| Phase | Who commits | Model |
|---|---|---|
| `mirror` (full pipeline) | **mhelm** (`commit` input, default `true`) | direct commit + push to the checked-out branch (HEAD) |
| `dry-run` | nobody | reads only; writes/commits nothing |
| `drift` | **the caller** | branch + PR (see [`drift.yml`](../examples/workflows/drift.yml)) — deliberately *not* mhelm |
| local / non-Actions CI | you, via `mhelm commit-lock` | same guard, opt-in `--push` |

mhelm owning the mirror commit means consumers never write (and never
mis-write) git glue. `mhelm commit-lock` is the single implementation;
`action.yml` and local flows both call it.

## The git guard (why staging order matters)

```
git add -- <dir>/chart-lock.json          # stage FIRST
[ -f <dir>/image-values.yaml ] && git add -- <dir>/image-values.yaml
git diff --cached --quiet                  # THEN ask "did anything change?"
  exit 0 -> nothing staged differs from HEAD -> no-op, no empty commit
  exit 1 -> staged changes (incl. a brand-new untracked file) -> commit
```

The naive consumer pattern — `git diff --quiet -- <dir>/chart-lock.json`
*before* staging — is **blind to untracked files**. On the first run the
lock does not exist in HEAD yet, so that guard reports "no changes" and
the freshly generated lock is discarded with the runner. Staging before
the diff is the entire fix; it is unit-tested against real temp git
repos in `internal/commitlock`.

Other guarantees `mhelm commit-lock` provides:

- **`[skip ci]`** in the default commit message
  (`chore(mhelm): update chart-lock.json for <dir> [skip ci]`) so the
  lockfile commit does not recursively re-trigger a path-filtered mirror
  workflow. The lock path is also outside typical `**/chart.json` path
  filters — belt and suspenders.
- **Checked-out HEAD, not `main`.** `workflow_dispatch` on a feature
  branch commits to *that* branch.
- **Concurrent-push safety.** `git pull --rebase --autostash` runs before
  `git push`, closing the non-fast-forward window against a concurrent
  human push.
- **No empty commits.** The no-change case is a clean exit 0.

Requires `permissions: contents: write` and a checkout with a
push-capable token (`actions/checkout`'s default
`persist-credentials: true`).

## Staleness as a tool guarantee (wrap charts)

`mhelm wrap` writes `lockfile.wrap` from `wrap.valuesFiles` +
`wrap.extraManifests`, but `wrap.version` is a **manual** bump. Before
v0.7, `discover --check` only caught discover-stage drift (the image set
/ digests via `discoveryValues`); a wrap-input change that didn't move
the image set — a `helm/values.yml` toggle, an extra-manifest edit —
left the published wrapper **stale and undetected**.

v0.7 makes that impossible by construction with a single anchor,
`chart-lock.json#wrap.inputsDigest`: a stable SHA-256 over

1. normalized `chart.json` (parsed through the chartfile struct, so
   whitespace / key-order churn does not move it),
2. the content of every `mirror.discoveryValues` file,
3. the content of every `wrap.valuesFiles` file,
4. the content of every `wrap.extraManifests` file,
5. the resolved upstream chart digest,
6. the mhelm lock-schema version.

(Hashed by content with the declared list order preserved, so it is
portable across machines and checkout roots.)

### `mhelm wrap` — fail closed

Let `fp` be the recomputed fingerprint and `prior` the committed
`wrap` block:

| Prior state | Decision |
|---|---|
| no `wrap` block | first wrap — record `fp`, proceed |
| `wrap` block, no `inputsDigest` (pre-v0.7) | recompute, **one-time stderr warning, do not hard-fail**; record `fp` |
| `inputsDigest == fp` | idempotent re-wrap — proceed |
| `wrap.version` differs from the published one | new immutable release — proceed |
| same `wrap.version`, `inputsDigest != fp` | **error, non-zero** unless `--allow-version-reuse` |

The fail-closed check runs **before** the wrapper is pushed, so a stale
wrapper never reaches the registry. Auto-suffixing the published tag was
rejected: it would diverge from the operator-declared `wrap.version` and
surprise deployers. Fail-closed keeps the human in control while making
the footgun loud.

### `mhelm discover --check` — exhaustive PR gate

`discover --check` recomputes `fp` and compares to the committed
`wrap.inputsDigest`:

- match → `wrap.inputsDigest: OK`
- mismatch → `wrap.inputsDigest: would change` → **exit 2**
- pre-v0.7 lock (no committed digest) → `wrap.inputsDigest: skip` — does
  **not** fail the gate; it engages once `mhelm wrap` records the anchor

A PR that legitimately changes a wrap input is expected to go red here
until it lands and the mirror republishes — bump `wrap.version` in the
same PR. This is the intended "exhaustive over wrap inputs" behaviour.

## Exit codes

| Command | Situation | Exit |
|---|---|---|
| `discover --check` | in sync | `0` |
| `discover --check` | operational error (network, parse) | `1` |
| `discover --check` | no committed lock ("would create") | `2` |
| `discover --check` | lock / image-values would change | `2` |
| `discover --check` | wrap inputs ≠ committed `inputsDigest` | `2` |
| `drift` | no committed lock | non-zero error + "run `mhelm mirror` first" hint — never a silent no-op, even with `--exit-zero` |
| `wrap` | inputs changed, `wrap.version` reused | non-zero error unless `--allow-version-reuse` |

## Downstream payoff

Once a consumer re-pins to v0.7.0 it can **delete** its hand-rolled
commit step and the buggy `git diff --quiet` guard, and **drop** any
bespoke staleness / dry-run gate workflow — mhelm guarantees both. Every
future-migrated component inherits correctness instead of re-deriving it.
